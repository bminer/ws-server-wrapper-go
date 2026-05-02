package wrapper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// errRebound is the context cancellation cause set by Bind when it attaches a
// new connection. readMessages detects it to exit silently instead of treating
// the context cancellation as a real connection error.
var errRebound = errors.New("client bound to new connection")

// errClosed is returned when an operation is attempted on a closed connection.
var errClosed = errors.New("connection closed")

// Client represents a WebSocket client
type Client struct {
	ClientChannel                     // the "main" client channel with no name
	connReqMu         sync.Mutex      // protects Context, Conn, requests, and anonChans
	ctx               context.Context // cancelled when the connection is closed
	ctxCancel         func(error)     // called when the connection is closed
	conn              Conn            // WebSocket connection; set `nil` on close
	requestID         int             // auto-incrementing request ID
	requestResponseCh map[int]chan messageResponse
	anonChans         map[int]*AnonymousChannel
	inboundCancelsMu  sync.Mutex
	inboundCancels    map[int]func(error) // cancel funcs for inbound requests
	handlersMu        sync.Mutex          // protects handlers and handlersOnce
	handlers          map[handlerName]any
	handlersOnce      map[handlerName]any
	dataMu            sync.Mutex
	data              map[string]any
	server            *Server // server associated with the Client
}

// NewClient creates a new Client not associated with any Server. Register
// event handlers on the returned Client, then call Client.Bind to attach a
// WebSocket connection and begin processing messages.
//
// Pass a non-nil conn to bind immediately (useful when no "open" handler is
// needed):
//
//	client := wrapper.NewClient(coder.Wrap(wsConn))
//
// Pass nil when you want to register handlers first — the recommended pattern
// because it ensures no inbound message can arrive before a handler is in
// place:
//
//	client := wrapper.NewClient(nil)
//	client.On("open", func(c *wrapper.Client) { /* ... */ })
//	client.On("news", func(headline string) error { /* ... */ return nil })
//	conn, _ := websocket.Dial(ctx, "ws://example.com/ws", nil)
//	client.Bind(coder.Wrap(conn))
func NewClient(conn Conn) *Client {
	// Create client
	c := &Client{
		// ClientChannel is set below
		// ctx, ctxCancel, and conn are assigned in Bind method
		requestResponseCh: make(map[int]chan messageResponse),
		anonChans:         make(map[int]*AnonymousChannel),
		inboundCancels:    make(map[int]func(error)),
		handlers:          make(map[handlerName]any),
		handlersOnce:      make(map[handlerName]any),
		data:              make(map[string]any),
		// server is set only by Server.Accept
	}
	// Set channel reference back to client, so channel methods work properly
	c.ClientChannel.client = c

	// Optionally bind the connection
	if conn != nil {
		c.Bind(conn)
	}
	return c
}

// Bind attaches conn to the Client and starts reading inbound messages. It can
// be called on a freshly created Client to establish the initial connection or
// called again after a disconnect to reconnect; all registered event handlers
// are preserved across calls.
//
// If the Client already has an active connection, it is closed with
// StatusGoingAway before the new connection is attached, and any pending
// outbound requests are cancelled.
//
// Bind fires the "open"/"connect" event handlers synchronously before
// returning. This guarantees that any handlers registered inside the "open"
// callback are registered before any inbound messages are processed.
//
// To implement reconnection, call Bind again inside the "close" handler. Use
// the userClosed parameter to distinguish a user-initiated close from a
// connection drop — only reconnect when userClosed is false:
//
//	client.On("close", func(c *wrapper.Client, status wrapper.StatusCode, reason string, userClosed bool) {
//	    if userClosed {
//	        return // don't reconnect when the user explicitly closed
//	    }
//	    conn, err := websocket.Dial(ctx, "ws://example.com/ws", nil)
//	    if err == nil {
//	        c.Bind(coder.Wrap(conn))
//	    }
//	})
func (c *Client) Bind(conn Conn) {
	c.connReqMu.Lock()
	oldConn := c.conn
	c.conn = conn
	// Cancel the old context, so the old readMessages goroutine exits silently.
	if c.ctxCancel != nil {
		c.ctxCancel(errRebound)
	}

	// Abort any pending outbound requests; their responses will never arrive
	// on the new connection.
	for _, respCh := range c.requestResponseCh {
		respCh <- messageResponse{nil, errRebound}
		close(respCh)
	}
	clear(c.requestResponseCh)

	// Create a context that is cancelled when the connection is closed.
	// I know it is generally frowned upon to store the Context in a struct, but
	// we are using it as a signal to cancel readMessages and for request
	// cancellation.
	c.ctx, c.ctxCancel = context.WithCancelCause(context.Background())
	c.ctx = context.WithValue(c.ctx, clientKey{}, c)
	prevAnonChans := c.anonChans
	c.anonChans = make(map[int]*AnonymousChannel)
	c.connReqMu.Unlock()

	// Close previous connection / anonymous channels
	if oldConn != nil {
		_ = oldConn.Close(StatusGoingAway, errRebound.Error())
	}
	c.handlersMu.Lock()
	for _, ch := range prevAnonChans {
		closeHandlersForChannel("", ch.id, c.handlers, c.handlersOnce)
		ch.closeDetached(errRebound)
	}
	c.handlersMu.Unlock()

	// Fire "open" handlers synchronously before launching readMessages so that
	// any handlers registered inside the "open" callback are in place before
	// the first inbound message can arrive.
	c.emitOpen()
	if c.server != nil {
		c.server.emitOpen(c)
	}
	go c.readMessages()
}

// Close closes the active connection, aborts pending requets, and fires the
// "close"/"disconnect" event handlers synchronously before returning. If this
// Client is associated with a Server, Close removes it from the Server's set of
// connected clients.
func (c *Client) Close(status StatusCode, reason string) error {
	return c.close(status, reason, true, false)
}

// close closes the active connection. Internal calls to close the Client should
// use this method only. The public Close method is reserved for user-facing
// code.
func (c *Client) close(
	status StatusCode,
	reason string,
	userClosed bool,
	serverClosing bool,
) error {
	// Note: Server.Close sets userClosed to false
	if !serverClosing && c.server != nil {
		c.server.clientsMu.Lock()
		delete(c.server.clients, c)
		c.server.clientsMu.Unlock()
	}

	// Get active connection
	c.connReqMu.Lock()
	conn := c.conn
	if conn == nil {
		c.connReqMu.Unlock()
		// Connection was already closed
		return nil
	}
	// Cancel context and clear c.conn to indicate connection is closed
	c.ctxCancel(fmt.Errorf("client closed (status: %v)", status))
	c.conn = nil
	// Abort all pending outbound requests for this client.
	for _, respCh := range c.requestResponseCh {
		respCh <- messageResponse{nil, errClosed}
		close(respCh)
	}
	clear(c.requestResponseCh)
	anonChans := c.anonChans
	c.anonChans = make(map[int]*AnonymousChannel)
	c.connReqMu.Unlock()

	// Close all anonymous channels (snapshot-clear-then-close to avoid deadlock).
	c.handlersMu.Lock()
	for _, ch := range anonChans {
		closeHandlersForChannel("", ch.id, c.handlers, c.handlersOnce)
		ch.closeDetached(errClosed)
	}
	c.handlersMu.Unlock()

	// Emit "close" events and close the connection
	c.emitClose(status, reason, userClosed)
	if c.server != nil {
		c.server.emitClose(c, status, reason, userClosed)
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

// sendCancel sends a cancellation signal for a request and removes it from
// requestResponseCh.
func (c *Client) sendCancel(
	ctx context.Context,
	requestID *int,
	reason error,
) error {
	if reason == nil {
		reason = errors.New("Request aborted")
	}
	c.connReqMu.Lock()
	_, ok := c.requestResponseCh[*requestID]
	delete(c.requestResponseCh, *requestID)
	conn := c.conn
	c.connReqMu.Unlock()
	if !ok || conn == nil {
		// request complete or connection closed
		return nil
	}
	return conn.WriteMessage(ctx, &Message{
		RequestID: requestID,
		// Write as JS error
		ResponseJSError: true,
		CancelReason: map[string]any{
			"message": reason.Error(),
		},
	})
}

// sendAnonCancel sends an anonymous channel abort message to the client and
// closes the anonymous channel, mirroring how sendCancel removes an outbound
// request from requestResponseCh.
func (c *Client) sendAnonCancel(ctx context.Context, chanID int, reason error) error {
	if reason == nil {
		reason = context.Canceled
	}
	c.connReqMu.Lock()
	ch := c.anonChans[chanID]
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		if ch != nil {
			ch.closeWithCause(reason)
		}
		return errClosed
	}
	err := conn.WriteMessage(ctx, &Message{
		AnonymousChannel: chanID,
		ResponseJSError:  true,
		CancelReason: map[string]any{
			"message": reason.Error(),
		},
	})
	if ch != nil {
		ch.closeWithCause(reason)
	}
	return err
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

// sendResolveAnon sends an anonymous channel creation response {i, h:1} to
// the client. The channel ID is inferred from i on the remote end.
func (c *Client) sendResolveAnon(ctx context.Context, requestID *int) error {
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.WriteMessage(ctx, &Message{
		RequestID:        requestID,
		AnonymousChannel: 1,
	})
}

// sendEvent sends an event to the client
func (c *Client) sendEvent(ctx context.Context, channel string, anonID int, arguments ...any) error {
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return errClosed
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
	msg := &Message{Arguments: jsonArgs}
	if anonID != 0 {
		msg.AnonymousChannel = anonID
	} else {
		msg.Channel = channel
	}
	return conn.WriteMessage(ctx, msg)
}

// sendRequest sends a request to the client and returns the response
func (c *Client) sendRequest(
	ctx context.Context, channel string, anonID int, arguments ...any,
) (any, error) {
	c.connReqMu.Lock()
	conn := c.conn
	ctxClient := c.ctx
	if conn == nil {
		c.connReqMu.Unlock()
		return nil, errClosed
	}
	// Create channel for message response
	respCh := make(chan messageResponse, 1)
	// Add channel to client's pending requests and get unique request ID
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

	// Encode arguments as JSON
	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return nil, err
		}
		jsonArgs[i] = buf
	}

	// Send request to client
	msg := &Message{Arguments: jsonArgs, RequestID: &requestID}
	if anonID != 0 {
		msg.AnonymousChannel = anonID
	} else {
		msg.Channel = channel
	}
	err := conn.WriteMessage(ctx, msg)
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
		cancelCause := context.Cause(ctx)
		_ = c.sendCancel(ctxClient, &requestID, cancelCause)
		return nil, fmt.Errorf("awaiting response: %w", cancelCause)
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
		// If the context is cancelled
		if ctx.Err() != nil {
			// Connection was lost; exit silently
			return
		} else if err != nil {
			// Emit error and close connection
			err = fmt.Errorf("read message: %w", err)
			c.emitError(err)
			if c.server != nil {
				c.server.emitError(c, err)
			}
			c.close(StatusInternalError, err.Error(), false, false)
			return
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
		if ctx.Err() != nil {
			// Connection was lost; exit silently
			return
		} else if err != nil {
			// Emit error and close connection
			err = fmt.Errorf("handle message: %w", err)
			c.emitError(err)
			if c.server != nil {
				c.server.emitError(c, err)
			}
			c.close(StatusInternalError, err.Error(), false, false)
			return
		}
	}
}

// emitOpen fires the "open" and "connect" event handlers registered on the
// Client itself.
func (c *Client) emitOpen() bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(OpenHandler); ok {
				f(c)
				return true
			}
			return false
		},
		&c.handlersMu, c.handlers, c.handlersOnce,
		"open", "connect",
	)
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
func (c *Client) emitClose(s StatusCode, reason string, userClosed bool) bool {
	return emitReserved(
		func(f any) bool {
			switch f := f.(type) {
			case CloseHandler:
				f(c, s, reason, userClosed)
				return true
			case CloseHandlerOld:
				f(c, s, reason)
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
	// Get message event name if any
	eventName := msg.EventName()
	if eventName != "" {
		return c.handleEvent(ctx, msg, eventName, msg.Channel, msg.AnonymousChannel)
	}
	// defer close(msg.processed) cannot go to the top of this function because
	// handleEvent creates a goroutine that is responsible for closing msg.processed.
	defer close(msg.processed)

	// Inbound anonymous channel abort: {h, x} (no RequestID needed)
	if msg.AnonymousChannel != 0 && msg.CancelReason != nil {
		c.connReqMu.Lock()
		ch, ok := c.anonChans[msg.AnonymousChannel]
		c.connReqMu.Unlock()
		if ok {
			ch.closeWithCause(msg.CancelCause())
		}
		return nil
	}

	// Try processing response to prior request
	if msg.RequestID == nil {
		return nil // ignore message with no request ID
	}

	// Anonymous channel creation response: {i, h} (no Arguments; CancelReason
	// was handled above so it is nil here)
	if msg.AnonymousChannel != 0 {
		chanID := *msg.RequestID
		c.connReqMu.Lock()
		respCh, ok := c.requestResponseCh[*msg.RequestID]
		if ok {
			delete(c.requestResponseCh, *msg.RequestID)
		}
		anon := newAnonymousChannel(ctx, chanID, c)
		c.anonChans[chanID] = anon
		c.connReqMu.Unlock()
		if respCh == nil {
			return nil
		}
		respCh <- messageResponse{anon, nil}
		close(respCh)
		return nil
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

// handleEvent handles an inbound event or request on any channel.
// chanID is the string channel name for named channels, or "" for anonymous
// channels (anonID is used for map lookups and abort messages in that case).
func (c *Client) handleEvent(
	ctx context.Context, msg Message, eventName string,
	chanID string, anonID int,
) error {
	var cancel context.CancelCauseFunc

	// For anonymous channels, verify the channel exists; send abort if not.
	if anonID != 0 {
		c.connReqMu.Lock()
		_, ok := c.anonChans[anonID]
		c.connReqMu.Unlock()
		if !ok {
			defer close(msg.processed)
			err := fmt.Errorf("anonymous channel %d not found", anonID)
			_ = c.sendAnonCancel(ctx, anonID, err)
			if msg.RequestID != nil {
				return c.sendReject(ctx, msg.RequestID, err)
			}
			return nil
		}
	}

	// Look up handler (client-specific first). For anonymous channels chanID is
	// "" so the Channel field is empty and AnonymousChannel holds the numeric ID.
	handlerID := handlerName{Channel: chanID, AnonymousChannel: anonID, Event: eventName}
	c.handlersMu.Lock()
	handler, ok := c.handlersOnce[handlerID]
	if ok {
		delete(c.handlersOnce, handlerID)
	} else {
		handler = c.handlers[handlerID]
	}
	c.handlersMu.Unlock()

	// For regular channels, get handlerCtxFunc and fall back to server handlers.
	var handlerCtxFunc HandlerContextFunc
	if c.server != nil {
		c.server.handlersMu.Lock()
		handlerCtxFunc = c.server.handlerCtxFunc
		if anonID == 0 && handler == nil {
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
		var err error
		if anonID != 0 {
			err = fmt.Errorf("no event listener for '%s' on anonymous channel %d", eventName, anonID)
		} else if chanID == "" {
			// chanID is "" for the main (unnamed) channel
			err = fmt.Errorf("no event listener for '%s'", eventName)
		} else {
			err = fmt.Errorf("no event listener for '%s' on channel '%s'", eventName, chanID)
		}
		if msg.RequestID != nil {
			return c.sendReject(ctx, msg.RequestID, err)
		}
		return nil
	}

	// Wrap context for handler execution
	handlerCtx := ctx
	if handlerCtxFunc != nil {
		handlerCtx = handlerCtxFunc(ctx, chanID, eventName)
	}
	// For inbound requests, create a cancellable context and inject the
	// anonymous channel factory so handlers can call Channel(ctx).
	var anonChan *AnonymousChannel
	var newChanID int
	if msg.RequestID != nil {
		handlerCtx, cancel = context.WithCancelCause(handlerCtx)
		c.inboundCancelsMu.Lock()
		c.inboundCancels[*msg.RequestID] = cancel
		c.inboundCancelsMu.Unlock()

		newChanID = *msg.RequestID
		handlerCtx = context.WithValue(handlerCtx, anonChannelKey{}, func() *AnonymousChannel {
			if anonChan == nil {
				anonChan = newAnonymousChannel(ctx, newChanID, c)
			}
			return anonChan
		})
	}

	go func() {
		defer close(msg.processed)
		result, err := callHandler(handlerCtx, handler, msg.HandlerArguments())
		if cancel != nil {
			cancel(context.Canceled)
		}

		// For event messages (not requests), clean up any factory channel that was
		// created but not returned, then return. inboundCancels is only populated
		// for requests, so there is nothing to remove here.
		if msg.RequestID == nil {
			if anonChan != nil {
				anonChan.closeWithCause(context.Canceled)
			}
			return
		}

		c.inboundCancelsMu.Lock()
		delete(c.inboundCancels, *msg.RequestID)
		c.inboundCancelsMu.Unlock()

		// Handle request response: check the handler error first, then decide
		// whether the handler returned the factory anonymous channel.
		// Pointer identity is used intentionally: the handler must return the
		// exact *AnonymousChannel obtained from Channel(ctx) — not a copy or a
		// different channel with the same ID.
		var sendErr error
		if err != nil {
			// Handler returned an error; clean up any factory channel created
			if anonChan != nil {
				anonChan.closeWithCause(context.Canceled)
			}
			sendErr = c.sendReject(ctx, msg.RequestID, err)
		} else if result == anonChan && anonChan != nil {
			// Handler returned the factory anonymous channel; register and notify
			c.connReqMu.Lock()
			c.anonChans[newChanID] = anonChan
			c.connReqMu.Unlock()
			sendErr = c.sendResolveAnon(ctx, msg.RequestID)
		} else {
			// Handler returned a regular value (or nil / a different anon channel);
			// close the factory channel if one was created
			if anonChan != nil {
				anonChan.closeWithCause(context.Canceled)
			}
			sendErr = c.sendResolve(ctx, msg.RequestID, result)
		}
		if sendErr != nil {
			sendErr = fmt.Errorf("responding to request: %w", sendErr)
			c.emitError(sendErr)
			if c.server != nil {
				c.server.emitError(c, sendErr)
			}
			c.close(StatusInternalError, sendErr.Error(), false, false)
		}
	}()
	return nil
}
