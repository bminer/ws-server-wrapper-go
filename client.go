package wrapper

import (
	"context"
	"fmt"
	"sync"
)

// Client represents a WebSocket client
type Client struct {
	ClientChannel                 // the "main" client channel with no name
	ctx           context.Context // cancelled when the client is closed
	ctxCancel     func(error)     // called when the client is closed
	conn          Conn            // WebSocket connection; set to nil when closed
	server        *Server
	handlersMu    sync.Mutex
	handlers      map[handlerName]any
	handlersOnce  map[handlerName]any
	dataMu        sync.Mutex
	data          map[string]any
}

func newClient(conn Conn, server *Server) *Client {
	// Create client
	c := &Client{
		conn:         conn,
		server:       server,
		handlers:     make(map[handlerName]any),
		handlersOnce: make(map[handlerName]any),
		data:         make(map[string]any),
	}

	// Create a context that is cancelled when the client is closed and stores
	// the client itself.
	ctxClient, cancel := context.WithCancelCause(context.Background())
	ctxClient = context.WithValue(ctxClient, ClientKey, c)
	// I know it is generally frowned upon to store the Context in a struct, but
	// we are using it as a signal to cancel readMessages and for request
	// cancellation. I also feel like this approach is slightly better than
	// using a channel; previously a goroutine per client was created simply to
	// read from a close channel and cancel the readMessages Context. It feels
	// a bit wasteful.
	c.ctx = ctxClient
	c.ctxCancel = cancel

	// set reference back to client, so channel methods work properly
	c.ClientChannel.client = c
	return c
}

// Close closes the client connection and removes it from the list of clients
// connected to the server.
func (c *Client) Close(status StatusCode, reason string) error {
	c.server.clientsMu.Lock()
	delete(c.server.clients, c)
	c.server.clientsMu.Unlock()
	return c.closeWithoutLock(status, reason)
}

// closeWithoutLock closes the client connection without locking the server's
// clientsMu lock. This is used when the server is closing all clients.
func (c *Client) closeWithoutLock(status StatusCode, reason string) error {
	c.ctxCancel(fmt.Errorf("client closed (status: %v)", status))
	// Clear c.conn to indicate connection is closed
	c.dataMu.Lock()
	if c.conn == nil {
		// Connection already closed
		return nil
	}
	conn := c.conn
	c.conn = nil
	c.dataMu.Unlock()
	c.emitClose(status, reason)
	c.server.emitClose(c, status, reason)
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
	c.dataMu.Lock()
	conn := c.conn
	c.dataMu.Unlock()
	if conn == nil {
		return nil // ignore message if connection is closed
	}
	return conn.WriteMessage(ctx, &Message{
		RequestID:     requestID,
		ResponseError: err.Error(),
	})
}

// sendResolve sends a resolve / data response to a request
func (c *Client) sendResolve(ctx context.Context, requestID *int, data any) error {
	if requestID == nil {
		return fmt.Errorf("requestID is required")
	}
	c.dataMu.Lock()
	conn := c.conn
	c.dataMu.Unlock()
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
	c.dataMu.Lock()
	conn := c.conn
	c.dataMu.Unlock()
	if conn == nil {
		return fmt.Errorf("connection is closed")
	}
	return conn.WriteMessage(ctx, &Message{
		Channel:   channel,
		Arguments: arguments,
	})
}

// sendRequest sends a request to the client and returns the response
func (c *Client) sendRequest(
	ctx context.Context, channel string, arguments ...any,
) (any, error) {
	c.dataMu.Lock()
	conn := c.conn
	ctxClient := c.ctx
	c.dataMu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("connection is closed")
	}

	// Create channel for message response
	respCh := make(chan messageResponse, 1)

	// Add channel to server's pending requests and get unique request ID
	c.server.requestMu.Lock()
	c.server.requestID++
	requestID := c.server.requestID
	if c.server.requestResponseCh[requestID] != nil {
		// should never happen
		c.server.requestMu.Unlock()
		return nil, fmt.Errorf("request ID %d already in use", requestID)
	} else {
		c.server.requestResponseCh[requestID] = respCh
		c.server.requestMu.Unlock()
	}

	// Send request to client
	err := conn.WriteMessage(ctx, &Message{
		Channel:   channel,
		Arguments: arguments,
		RequestID: &requestID,
	})
	if err != nil {
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
	c.dataMu.Lock()
	conn := c.conn
	ctx := c.ctx
	c.dataMu.Unlock()
	for {
		var msg Message
		err := conn.ReadMessage(ctx, &msg)
		if err != nil {
			// Emit error and close client
			err = fmt.Errorf("read message: %w", err)
			c.emitError(err)
			c.server.emitError(c, err)
			c.Close(StatusInternalError, err.Error())
			return
		}

		select {
		case <-ctx.Done():
			return // client closed
		default:
		}

		// Emit only valid messages
		c.emitMessage(msg)

		if msg.IgnoreIfFalse != nil && *msg.IgnoreIfFalse == false {
			continue // ignore message
		}

		err = c.handleMessage(ctx, msg)
		if err != nil {
			// Emit error and close client
			err = fmt.Errorf("handle message: %w", err)
			c.emitError(err)
			c.server.emitError(c, err)
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
	eventName := msg.EventName()
	if eventName != "" {
		// Process inbound event/request
		handlerID := handlerName{Channel: msg.Channel, Event: eventName}

		// Get client-specific handler
		c.handlersMu.Lock()
		handlerCtxFunc := c.server.handlerCtxFunc
		handler, ok := c.handlersOnce[handlerID]
		if ok {
			delete(c.handlersOnce, handlerID)
		} else {
			handler, ok = c.handlers[handlerID]
		}
		c.handlersMu.Unlock()

		// Get server handler if no client handler exists
		if handler == nil {
			c.server.handlersMu.Lock()
			handler, ok = c.server.handlersOnce[handlerID]
			if ok {
				delete(c.server.handlersOnce, handlerID)
			} else {
				handler, ok = c.server.handlers[handlerID]
			}
			c.server.handlersMu.Unlock()
		}

		// Handle missing handler
		if handler == nil {
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
		var cancel context.CancelFunc
		if handlerCtxFunc != nil {
			handlerCtx, cancel = handlerCtxFunc(ctx, msg.Channel, eventName)
		}

		// Call handler with arguments
		result, err := callHandler(
			handlerCtx, handler, msg.HandlerArguments(),
		)
		if cancel != nil {
			cancel()
		}
		if msg.RequestID == nil {
			// Silently ignore the response if it's not a request
			return nil
		}
		if err != nil {
			// Send error response
			return c.sendReject(ctx, msg.RequestID, err)
		}
		// Send data response
		return c.sendResolve(ctx, msg.RequestID, result)
	}

	// Try processing response to prior request
	if msg.RequestID == nil {
		return nil // ignore message with invalid request ID
	}

	// Get request handler
	c.server.requestMu.Lock()
	respCh, ok := c.server.requestResponseCh[*msg.RequestID]
	if ok {
		delete(c.server.requestResponseCh, *msg.RequestID)
	}
	c.server.requestMu.Unlock()
	if respCh == nil {
		return nil // ignore message with invalid request ID
	}

	// Process response
	res, err := msg.Response()
	respCh <- messageResponse{res, err}
	close(respCh)

	return nil
}
