package wrapper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// errRebound is the context cancellation cause set by Bind when it attaches a
// new connection. readMessages detects it to exit silently instead of treating
// the context cancellation as a real connection error.
var errRebound = errors.New("client bound to new connection")

// requestIDKeyType is the context key type for injecting request IDs into
// handler contexts.
type requestIDKeyType struct{}

// requestIDKey is the context key used to inject the request ID into the
// handler context. Used by Client.Channel to retrieve the request ID.
var requestIDKey = requestIDKeyType{}

// anonChannelState holds the state of an anonymous channel.
type anonChannelState struct {
	channel *ClientChannel
	ctx     context.Context
	cancel  context.CancelCauseFunc
	closed  bool // true after Close() or closeFromRemote() is called
}

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
	anonChannelsMu    sync.Mutex
	anonChannels      map[string]*anonChannelState // keyed by channel ID (request ID as string)
	closedChannelsMu  sync.Mutex
	closedChannels    map[string]bool // tracks closed named channels
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
		inboundCancels:    make(map[int]func(error)),
		handlers:          make(map[handlerName]any),
		handlersOnce:      make(map[handlerName]any),
		data:              make(map[string]any),
		anonChannels:      make(map[string]*anonChannelState),
		closedChannels:    make(map[string]bool),
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
	// cancellation. I also feel like this approach is slightly better than
	// using a channel; previously a goroutine per client was created simply to
	// read from a close channel and cancel the readMessages Context. It feels
	// a bit wasteful.
	c.ctx, c.ctxCancel = context.WithCancelCause(context.Background())
	c.ctx = context.WithValue(c.ctx, ClientKey, c)
	c.connReqMu.Unlock()

	if oldConn != nil {
		_ = oldConn.Close(StatusGoingAway, errRebound.Error())
	}

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
		respCh <- messageResponse{nil, fmt.Errorf("connection closed")}
		close(respCh)
	}
	clear(c.requestResponseCh)
	c.connReqMu.Unlock()
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
	if conn == nil {
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("connection is closed")
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

// Channel creates or retrieves an anonymous channel tied to the current request
// context. Returns nil if called outside of a request handler context. Calling
// Channel with the same context returns the same *ClientChannel (idempotent).
func (c *Client) Channel(ctx context.Context) *ClientChannel {
	reqID, ok := ctx.Value(requestIDKey).(int)
	if !ok {
		return nil
	}
	id := strconv.Itoa(reqID)

	c.anonChannelsMu.Lock()
	if state, exists := c.anonChannels[id]; exists {
		c.anonChannelsMu.Unlock()
		return state.channel
	}

	// Derive channel context from the client's connection context (not the
	// handler context) so the anonymous channel outlives the handler call.
	c.connReqMu.Lock()
	clientCtx := c.ctx
	c.connReqMu.Unlock()

	chCtx, chCancel := context.WithCancelCause(clientCtx)
	ch := &ClientChannel{name: id, client: c, anonymous: true}
	c.anonChannels[id] = &anonChannelState{
		channel: ch,
		ctx:     chCtx,
		cancel:  chCancel,
	}
	c.anonChannelsMu.Unlock()

	// Watch for context cancellation to auto-close the channel
	go c.watchAnonChannelContext(id, chCtx)

	return ch
}

// sendAnonEvent sends an event on an anonymous channel
func (c *Client) sendAnonEvent(ctx context.Context, anonID string, arguments ...any) error {
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return fmt.Errorf("connection is closed")
	}
	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return err
		}
		jsonArgs[i] = buf
	}
	return conn.WriteMessage(ctx, &Message{
		AnonChannel: anonID,
		Arguments:   jsonArgs,
	})
}

// sendAnonRequest sends a request on an anonymous channel and returns the response
func (c *Client) sendAnonRequest(
	ctx context.Context, anonID string, arguments ...any,
) (any, error) {
	c.connReqMu.Lock()
	conn := c.conn
	ctxClient := c.ctx
	if conn == nil {
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("connection is closed")
	}
	respCh := make(chan messageResponse, 1)
	c.requestID++
	requestID := c.requestID
	if c.requestResponseCh[requestID] != nil {
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("request ID %d already in use", requestID)
	}
	c.requestResponseCh[requestID] = respCh
	c.connReqMu.Unlock()

	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return nil, err
		}
		jsonArgs[i] = buf
	}
	err := conn.WriteMessage(ctx, &Message{
		AnonChannel: anonID,
		Arguments:   jsonArgs,
		RequestID:   &requestID,
	})
	if err != nil {
		c.connReqMu.Lock()
		delete(c.requestResponseCh, requestID)
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("sending request: %w", err)
	}

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

// sendRequestChannel sends a request expecting an anonymous channel response.
// The response must contain an AnonChannel field; otherwise an error is returned.
func (c *Client) sendRequestChannel(
	ctx context.Context, channel string, arguments ...any,
) (*ClientChannel, error) {
	c.connReqMu.Lock()
	conn := c.conn
	ctxClient := c.ctx
	if conn == nil {
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("connection is closed")
	}
	respCh := make(chan messageResponse, 1)
	c.requestID++
	requestID := c.requestID
	if c.requestResponseCh[requestID] != nil {
		c.connReqMu.Unlock()
		return nil, fmt.Errorf("request ID %d already in use", requestID)
	}
	c.requestResponseCh[requestID] = respCh
	c.connReqMu.Unlock()

	jsonArgs := make([]json.RawMessage, len(arguments))
	for i, arg := range arguments {
		buf, err := json.Marshal(arg)
		if err != nil {
			return nil, err
		}
		jsonArgs[i] = buf
	}
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

	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("response channel closed unexpectedly")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		// resp.Data should be the anonymous channel signal
		anonCh, ok := resp.Data.(*ClientChannel)
		if !ok || anonCh == nil {
			return nil, fmt.Errorf(
				"expected anonymous channel response, got normal value",
			)
		}
		return anonCh, nil
	case <-ctxClient.Done():
		return nil, fmt.Errorf("awaiting response: %w", context.Cause(ctxClient))
	case <-ctx.Done():
		return nil, fmt.Errorf("awaiting response: %w", context.Cause(ctx))
	}
}

// sendResolveAnon sends an anonymous channel creation response {i, h: "1"}
func (c *Client) sendResolveAnon(ctx context.Context, requestID *int) error {
	if requestID == nil {
		return fmt.Errorf("requestID is required")
	}
	c.connReqMu.Lock()
	conn := c.conn
	c.connReqMu.Unlock()
	if conn == nil {
		return nil
	}
	id := strconv.Itoa(*requestID)
	return conn.WriteMessage(ctx, &Message{
		RequestID:   requestID,
		AnonChannel: id,
	})
}

// sendAnonCancel sends {h, x} cancellation message for an anonymous channel
func (c *Client) sendAnonCancel(anonID string) {
	c.connReqMu.Lock()
	conn := c.conn
	ctx := c.ctx
	c.connReqMu.Unlock()
	if conn == nil {
		return
	}
	// Best-effort send; ignore errors (connection may already be dead)
	_ = conn.WriteMessage(ctx, &Message{
		AnonChannel: anonID,
		CancelReason: map[string]any{
			"message": "channel closed",
		},
		ResponseJSError: true,
	})
}

// closeAnonChannel closes an anonymous channel locally and sends {h, x} to
// remote. Idempotent — second call is a no-op.
func (c *Client) closeAnonChannel(anonID string) {
	c.anonChannelsMu.Lock()
	state, exists := c.anonChannels[anonID]
	if !exists || state.closed {
		c.anonChannelsMu.Unlock()
		return
	}
	state.closed = true
	c.anonChannelsMu.Unlock()

	// Cancel the channel context
	state.cancel(fmt.Errorf("anonymous channel closed"))

	// Send {h, x} to remote
	c.sendAnonCancel(anonID)

	// Remove handlers
	c.removeAnonChannelHandlers(anonID)
}

// closeAnonChannelFromRemote closes an anonymous channel due to receiving
// {h, x} from the remote end. Does NOT send {h, x} back.
func (c *Client) closeAnonChannelFromRemote(anonID string, cause error) {
	c.anonChannelsMu.Lock()
	state, exists := c.anonChannels[anonID]
	if !exists || state.closed {
		c.anonChannelsMu.Unlock()
		return
	}
	state.closed = true
	c.anonChannelsMu.Unlock()

	// Cancel the channel context with the remote's cause
	state.cancel(cause)

	// Remove handlers
	c.removeAnonChannelHandlers(anonID)
}

// removeAnonChannelHandlers removes all handlers for the given anonymous channel
func (c *Client) removeAnonChannelHandlers(anonID string) {
	c.handlersMu.Lock()
	for key := range c.handlers {
		if key.Anonymous && key.Channel == anonID {
			delete(c.handlers, key)
		}
	}
	for key := range c.handlersOnce {
		if key.Anonymous && key.Channel == anonID {
			delete(c.handlersOnce, key)
		}
	}
	c.handlersMu.Unlock()
}

// closeNamedChannel closes a named channel by removing its handlers and marking
// it as closed.
func (c *Client) closeNamedChannel(name string) {
	c.closedChannelsMu.Lock()
	c.closedChannels[name] = true
	c.closedChannelsMu.Unlock()

	c.handlersMu.Lock()
	for key := range c.handlers {
		if !key.Anonymous && key.Channel == name {
			delete(c.handlers, key)
		}
	}
	for key := range c.handlersOnce {
		if !key.Anonymous && key.Channel == name {
			delete(c.handlersOnce, key)
		}
	}
	c.handlersMu.Unlock()
}

// isAnonChannelClosed returns whether the anonymous channel is closed
func (c *Client) isAnonChannelClosed(anonID string) bool {
	c.anonChannelsMu.Lock()
	defer c.anonChannelsMu.Unlock()
	state, exists := c.anonChannels[anonID]
	if !exists {
		return true // channel doesn't exist, treat as closed
	}
	return state.closed
}

// isChannelClosed returns whether a named channel is closed
func (c *Client) isChannelClosed(name string) bool {
	c.closedChannelsMu.Lock()
	defer c.closedChannelsMu.Unlock()
	return c.closedChannels[name]
}

// watchAnonChannelContext watches the anonymous channel context and calls
// closeAnonChannel when it is cancelled (idempotent).
func (c *Client) watchAnonChannelContext(anonID string, ctx context.Context) {
	<-ctx.Done()
	c.closeAnonChannel(anonID)
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
	// Handle anonymous channel event/request messages.
	// Note: creation responses ({i, h} with no event name) are handled below
	// in the standard response section.
	if msg.AnonChannel != "" && (msg.EventName() != "" || msg.CancelReason != nil) {
		return c.handleAnonMessage(ctx, msg)
	}

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
			// Inject request ID into handler context for Client.Channel(ctx)
			handlerCtx = context.WithValue(handlerCtx, requestIDKey, *msg.RequestID)
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
			} else if ch, ok := result.(*ClientChannel); ok && ch != nil && ch.anonymous {
				// Handler returned an anonymous channel — send {i, h}
				err = c.sendResolveAnon(ctx, msg.RequestID)
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
				c.close(StatusInternalError, err.Error(), false, false)
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
		// Ignore {i, x} for request IDs that have an open anonymous channel
		c.anonChannelsMu.Lock()
		anonID := strconv.Itoa(*msg.RequestID)
		_, hasAnon := c.anonChannels[anonID]
		c.anonChannelsMu.Unlock()
		if hasAnon {
			return nil // anonymous channel has its own lifecycle via {h, x}
		}

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

	// Check if this is an anonymous channel creation response {i, h}
	if msg.AnonChannel != "" {
		// Create local anonymous channel for the requestor side
		anonID := strconv.Itoa(*msg.RequestID)
		chCtx, chCancel := context.WithCancelCause(ctx)
		ch := &ClientChannel{name: anonID, client: c, anonymous: true}
		c.anonChannelsMu.Lock()
		c.anonChannels[anonID] = &anonChannelState{
			channel: ch,
			ctx:     chCtx,
			cancel:  chCancel,
		}
		c.anonChannelsMu.Unlock()
		go c.watchAnonChannelContext(anonID, chCtx)
		respCh <- messageResponse{Data: ch}
		close(respCh)
		return nil
	}

	// Process response
	res, err := msg.Response()
	respCh <- messageResponse{res, err}
	close(respCh)

	return nil
}

// handleAnonMessage handles a message destined for an anonymous channel.
// Only called for messages with AnonChannel set and either an event name or
// a CancelReason.
func (c *Client) handleAnonMessage(ctx context.Context, msg Message) error {
	anonID := msg.AnonChannel

	// Handle {h, x} cancellation from remote
	if msg.CancelReason != nil {
		defer close(msg.processed)
		c.closeAnonChannelFromRemote(anonID, msg.CancelCause())
		return nil
	}

	eventName := msg.EventName()

	// Look up handler for this anonymous channel
	handlerID := handlerName{Channel: anonID, Event: eventName, Anonymous: true}

	c.handlersMu.Lock()
	handler, ok := c.handlersOnce[handlerID]
	if ok {
		delete(c.handlersOnce, handlerID)
	} else {
		handler = c.handlers[handlerID]
	}
	c.handlersMu.Unlock()

	if handler == nil {
		defer close(msg.processed)
		err := fmt.Errorf(
			"no event listener for '%s' on anonymous channel '%s'",
			eventName, anonID,
		)
		if msg.RequestID != nil {
			return c.sendReject(ctx, msg.RequestID, err)
		}
		return nil
	}

	// Get handler context from the anonymous channel state
	c.anonChannelsMu.Lock()
	state := c.anonChannels[anonID]
	c.anonChannelsMu.Unlock()

	handlerCtx := ctx
	if state != nil {
		handlerCtx = state.ctx
	}

	// Get server's handler Context function
	if c.server != nil {
		c.server.handlersMu.Lock()
		if c.server.handlerCtxFunc != nil {
			handlerCtx = c.server.handlerCtxFunc(handlerCtx, anonID, eventName)
		}
		c.server.handlersMu.Unlock()
	}

	var cancel context.CancelCauseFunc
	if msg.RequestID != nil {
		handlerCtx, cancel = context.WithCancelCause(handlerCtx)
		c.inboundCancelsMu.Lock()
		c.inboundCancels[*msg.RequestID] = cancel
		c.inboundCancelsMu.Unlock()
	}

	go func() {
		defer close(msg.processed)
		result, err := callHandler(handlerCtx, handler, msg.HandlerArguments())
		if cancel != nil {
			cancel(context.Canceled)
		}

		if msg.RequestID == nil {
			return
		}

		c.inboundCancelsMu.Lock()
		delete(c.inboundCancels, *msg.RequestID)
		c.inboundCancelsMu.Unlock()

		if err != nil {
			err = c.sendReject(ctx, msg.RequestID, err)
		} else {
			err = c.sendResolve(ctx, msg.RequestID, result)
		}
		if err != nil {
			err = fmt.Errorf("responding to request: %w", err)
			c.emitError(err)
			if c.server != nil {
				c.server.emitError(c, err)
			}
			c.close(StatusInternalError, err.Error(), false, false)
		}
	}()
	return nil
}
