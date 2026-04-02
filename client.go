package wrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Client represents a WebSocket client
type Client struct {
	ClientChannel                     // the "main" client channel with no name
	connReqMu         sync.Mutex      // protects Context, Conn, and request stuff
	ctx               context.Context // cancelled when the connection is closed
	ctxCancel         func(error)     // called when the connection is closed
	conn              Conn            // WebSocket connection; set `nil` on close
	requestID         int             // auto-incrementing request ID
	requestResponseCh map[int]chan messageResponse
	inboundCancelsMu  sync.Mutex
	inboundCancels    map[int]func(error) // cancel funcs for inbound requests
	handlersMu        sync.Mutex
	handlers          map[handlerName]any
	handlersOnce      map[handlerName]any
	dataMu            sync.Mutex
	data              map[string]any
	server            *Server // server associated with the Client
}

func newClient(conn Conn, server *Server) *Client {
	// Create client
	c := &Client{
		// ClientChannel, ctx, and ctxCancel are set below
		conn:              conn,
		requestResponseCh: make(map[int]chan messageResponse),
		inboundCancels:    make(map[int]func(error)),
		handlers:          make(map[handlerName]any),
		handlersOnce:      make(map[handlerName]any),
		data:              make(map[string]any),
		server:            server,
	}

	// Create a context that is cancelled when the connection is closed.
	// I know it is generally frowned upon to store the Context in a struct, but
	// we are using it as a signal to cancel readMessages and for request
	// cancellation. I also feel like this approach is slightly better than
	// using a channel; previously a goroutine per client was created simply to
	// read from a close channel and cancel the readMessages Context. It feels
	// a bit wasteful.
	c.ctx, c.ctxCancel = context.WithCancelCause(context.Background())
	c.ctx = context.WithValue(c.ctx, ClientKey, c)

	// set reference back to client, so channel methods work properly
	c.ClientChannel.client = c
	return c
}

// Close closes the client connection and removes it from the list of clients
// connected to the server.
func (c *Client) Close(status StatusCode, reason string) error {
	if c.server != nil {
		c.server.clientsMu.Lock()
		delete(c.server.clients, c)
		c.server.clientsMu.Unlock()
	}
	return c.closeWithoutLock(status, reason)
}

// closeWithoutLock closes the client connection without locking the server's
// clientsMu lock. This is used when the server is closing all clients.
func (c *Client) closeWithoutLock(status StatusCode, reason string) error {
	c.connReqMu.Lock()
	conn := c.conn
	if conn == nil {
		c.connReqMu.Unlock()
		// Connection was already closed
		return nil
	}
	// Clear c.conn to indicate connection is closed
	c.ctxCancel(fmt.Errorf("client closed (status: %v)", status))
	c.conn = nil
	// Abort all pending outbound requests for this client.
	for _, respCh := range c.requestResponseCh {
		respCh <- messageResponse{nil, fmt.Errorf("connection closed")}
		close(respCh)
	}
	clear(c.requestResponseCh)
	c.connReqMu.Unlock()
	// Emit "close" events and close the connection
	c.emitClose(status, reason)
	if c.server != nil {
		c.server.emitClose(c, status, reason)
	}
	return conn.Close(status, reason)
}

// Get returns the data for the client at the specified key
func (c *Client) Get(key string) any {
	c.dataMu.Lock()
	defer c.dataMu.Unlock()
	return c.data[key]
}

// Set sets the data for the client at the specified key
func (c *Client) Set(key string, value any) {
	c.dataMu.Lock()
	defer c.dataMu.Unlock()
	c.data[key] = value
}

// Of returns a channel for the given name
func (c *Client) Of(name string) ClientChannel {
	return ClientChannel{
		name:   name,
		client: c,
	}
}

// sendReject sends a reject / error response to a request
func (c *Client) sendReject(ctx context.Context, requestID *int, err error) error {
	if requestID == nil {
		return fmt.Errorf("requestID is required")
	} else if err == nil {
		return fmt.Errorf("error is required")
	}
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return nil // ignore message if connection is closed
	}
	return conn.WriteMessage(ctx, &Message{
		RequestID: requestID,
		// Write as JS error
		ResponseJSError: true,
		ResponseError: map[string]any{
			"message": err.Error(),
		},
	})
}

// sendResolve sends a resolve / data response to a request
func (c *Client) sendResolve(ctx context.Context, requestID *int, data any) error {
	if requestID == nil {
		return fmt.Errorf("requestID is required")
	}
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return nil // ignore message if connection is closed
	}
	return conn.WriteMessage(ctx, &Message{
		RequestID:    requestID,
		ResponseData: data,
	})
}

// sendEvent sends an event to the client
func (c *Client) sendEvent(ctx context.Context, channel string, arguments ...any) error {
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return fmt.Errorf("connection is closed")
	}
	// Encode arguments as JSON
	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return err
		}
		jsonArgs[i] = buf
	}
	// Send event to client
	return conn.WriteMessage(ctx, &Message{
		Channel:   channel,
		Arguments: jsonArgs,
	})
}

// sendRequest sends a request to the client and returns the response
func (c *Client) sendRequest(
	ctx context.Context, channel string, arguments ...any,
) (any, error) {
	c.connReqMu.Lock()
	conn := c.conn
	ctxClient := c.ctx
	c.connReqMu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("connection is closed")
	}

	// Encode arguments as JSON
	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return nil, err
		}
		jsonArgs[i] = buf
	}

	// Create channel for message response
	respCh := make(chan messageResponse, 1)

	// Add channel to client's pending requests and get unique request ID
	c.connReqMu.Lock()
	c.requestID++
	requestID := c.requestID
	if c.requestResponseCh[requestID] != nil {
		// should never happen
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("request ID %d already in use", requestID)
	} else {
		c.requestResponseCh[requestID] = respCh
		c.connReqMu.Unlock()
	}

	// Send request to client
	err := conn.WriteMessage(ctx, &Message{
		Channel:   channel,
		Arguments: jsonArgs,
		RequestID: &requestID,
	})
	if err != nil {
		c.connReqMu.Lock()
		delete(c.requestResponseCh, requestID)
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("sending request: %w", err)
	}

	// Wait for response
	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("response channel closed unexpectedly")
		}
		return resp.Data, resp.Error
	case <-ctxClient.Done():
		return nil, fmt.Errorf("awaiting response: %w", context.Cause(ctxClient))
	case <-ctx.Done():
		return nil, fmt.Errorf("awaiting response: %w", context.Cause(ctx))
	}
}

// readMessages reads messages from the client connection and handles them.
// Cancel the context to stop reading messages
func (c *Client) readMessages() {
	// Read messages from the client connection
	c.connReqMu.Lock()
	conn := c.conn
	ctx := c.ctx
	c.connReqMu.Unlock()
	if conn == nil {
		// Connection was closed before readMessages had a chance to start.
		return
	}
	for {
		var msg Message
		err := conn.ReadMessage(ctx, &msg)
		if err != nil {
			// Emit error and close client
			err = fmt.Errorf("read message: %w", err)
			c.emitError(err)
			if c.server != nil {
				c.server.emitError(c, err)
			}
			c.Close(StatusInternalError, err.Error())
			return
		}

		select {
		case <-ctx.Done():
			return // client closed
		default:
		}

		// Emit only valid messages
		msg.processed = make(chan struct{})
		c.emitMessage(msg)

		if msg.IgnoreIfFalse != nil && !*msg.IgnoreIfFalse {
			close(msg.processed)
			continue // ignore message
		}

		// Note: handleMessage will close `msg.processed`
		err = c.handleMessage(ctx, msg)
		if err != nil {
			// Emit error and close client
			err = fmt.Errorf("handle message: %w", err)
			c.emitError(err)
			if c.server != nil {
				c.server.emitError(c, err)
			}
			c.Close(StatusInternalError, err.Error())
			return
		}
	}
}

// emitError calls the "error" event handler on the main channel
func (c *Client) emitError(err error) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(ErrorHandler); ok {
				f(c, err)
				return true
			}
			return false
		},
		&c.handlersMu, c.handlers, c.handlersOnce,
		"error",
	)
}

// emitMessage calls the "message" event handler on the main channel
func (c *Client) emitMessage(msg Message) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(MessageHandler); ok {
				f(c, msg)
				return true
			}
			return false
		},
		&c.handlersMu, c.handlers, c.handlersOnce,
		"message",
	)
}

// emitClose calls the "close" and "disconnect" event handlers on the main
// channel
func (c *Client) emitClose(status StatusCode, reason string) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(CloseHandler); ok {
				f(c, status, reason)
				return true
			}
			return false
		},
		&c.handlersMu, c.handlers, c.handlersOnce,
		"close", "disconnect",
	)
}

// handleMessage processes an inbound message for this client. Returns an error
// if there was an error sending the response to the client.
func (c *Client) handleMessage(ctx context.Context, msg Message) error {
	// We may create a request-specific cancellable context later
	var cancel context.CancelCauseFunc
	// Get message event name if any
	eventName := msg.EventName()
	if eventName != "" {
		// Process inbound event/request
		handlerID := handlerName{Channel: msg.Channel, Event: eventName}

		// Get client-specific handler
		c.handlersMu.Lock()
		handler, ok := c.handlersOnce[handlerID]
		if ok {
			delete(c.handlersOnce, handlerID)
		} else {
			handler = c.handlers[handlerID]
		}
		c.handlersMu.Unlock()

		// Get server's handler Context function
		var handlerCtxFunc HandlerContextFunc
		if c.server != nil {
			c.server.handlersMu.Lock()
			handlerCtxFunc = c.server.handlerCtxFunc
			// Get server handler if no client handler exists
			if handler == nil {
				handler, ok = c.server.handlersOnce[handlerID]
				if ok {
					delete(c.server.handlersOnce, handlerID)
				} else {
					handler = c.server.handlers[handlerID]
				}
			}
			c.server.handlersMu.Unlock()
		}

		// Handle missing handler
		if handler == nil {
			defer close(msg.processed)
			err := fmt.Errorf(
				"no event listener for '%s' on channel '%s'",
				eventName, msg.Channel,
			)
			if msg.Channel == "" {
				err = fmt.Errorf("no event listener for '%s'", eventName)
			}
			// Send error response if it's a request
			if msg.RequestID != nil {
				return c.sendReject(ctx, msg.RequestID, err)
			}
			// Otherwise, ignore this message
			return nil
		}

		// Wrap context for handler execution
		handlerCtx := ctx
		if handlerCtxFunc != nil {
			handlerCtx = handlerCtxFunc(ctx, msg.Channel, eventName)
		}
		// For inbound requests, create a request-specific cancellable context
		// to allow a protocol-level cancellation message to cancel the handler.
		if msg.RequestID != nil {
			handlerCtx, cancel = context.WithCancelCause(handlerCtx)
			// Save the CancelCauseFunc for the request
			c.inboundCancelsMu.Lock()
			c.inboundCancels[*msg.RequestID] = cancel
			c.inboundCancelsMu.Unlock()
		}

		// Call handler with arguments
		go func() {
			defer close(msg.processed)
			result, err := callHandler(
				handlerCtx, handler, msg.HandlerArguments(),
			)
			// We are done running the handler, so cancel the handler context
			if cancel != nil {
				cancel(context.Canceled)
			}

			if msg.RequestID == nil {
				// Silently ignore the response if it's not a request
				return
			}

			// Clean up inbound cancellation
			c.inboundCancelsMu.Lock()
			delete(c.inboundCancels, *msg.RequestID)
			c.inboundCancelsMu.Unlock()

			if err != nil {
				// Send error response
				err = c.sendReject(ctx, msg.RequestID, err)
			} else {
				// Send data response
				err = c.sendResolve(ctx, msg.RequestID, result)
			}
			if err != nil {
				// Emit error and close client
				err = fmt.Errorf("responding to request: %w", err)
				c.emitError(err)
				if c.server != nil {
					c.server.emitError(c, err)
				}
				c.Close(StatusInternalError, err.Error())
			}
		}()
		return nil
	}
	defer close(msg.processed)

	// Try processing response to prior request
	if msg.RequestID == nil {
		return nil // ignore message with invalid request ID
	}

	// Handle request cancellation message (ws-wrapper v4)
	if msg.CancelReason != nil {
		c.inboundCancelsMu.Lock()
		cancel, ok := c.inboundCancels[*msg.RequestID]
		if ok {
			delete(c.inboundCancels, *msg.RequestID)
		}
		c.inboundCancelsMu.Unlock()
		if ok {
			cancel(msg.CancelCause())
		}
		return nil
	}

	// Get request handler
	c.connReqMu.Lock()
	respCh, ok := c.requestResponseCh[*msg.RequestID]
	if ok {
		delete(c.requestResponseCh, *msg.RequestID)
	}
	c.connReqMu.Unlock()
	if respCh == nil {
		return nil // ignore message with invalid request ID
	}

	// Process response
	res, err := msg.Response()
	respCh <- messageResponse{res, err}
	close(respCh)

	return nil
}
