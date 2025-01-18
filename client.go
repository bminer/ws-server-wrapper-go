package wrapper

import (
	"context"
	"fmt"
	"sync"
)

// Client represents a WebSocket client
type Client struct {
	ClientChannel
	closeCh      chan struct{} // closed when the client is closed
	conn         Conn          // WebSocket connection
	server       *Server
	handlersMu   sync.Mutex
	handlers     map[handlerName]any
	handlersOnce map[handlerName]any
	dataMu       sync.Mutex
	data         map[string]any
}

func newClient(conn Conn, server *Server) *Client {
	c := &Client{
		closeCh:      make(chan struct{}),
		conn:         conn,
		server:       server,
		handlers:     make(map[handlerName]any),
		handlersOnce: make(map[handlerName]any),
		data:         make(map[string]any),
	}
	// set reference back to client, so channel methods work properly
	c.ClientChannel.client = c
	return c
}

// Close closes the client connection and removes it from the list of clients
// connected to the server.
func (c *Client) Close(status StatusCode, reason string) error {
	close(c.closeCh)
	c.server.clientsMu.Lock()
	defer c.server.clientsMu.Unlock()
	delete(c.server.clients, c)
	return c.conn.Close(status, reason)
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
	return c.conn.WriteMessage(ctx, &Message{
		RequestID:     requestID,
		ResponseError: err.Error(),
	})
}

// sendResolve sends a resolve / data response to a request
func (c *Client) sendResolve(ctx context.Context, requestID *int, data any) error {
	return c.conn.WriteMessage(ctx, &Message{
		RequestID:    requestID,
		ResponseData: data,
	})
}

// sendEvent sends an event to the client
func (c *Client) sendEvent(ctx context.Context, channel string, arguments ...any) error {
	return c.conn.WriteMessage(ctx, &Message{
		Channel:   channel,
		Arguments: arguments,
	})
}

// sendRequest sends a request to the client and returns the response
func (c *Client) sendRequest(
	ctx context.Context, channel string, arguments ...any,
) (any, error) {
	// Create channel for message response
	respCh := make(chan MessageResponse, 1)

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
	err := c.conn.WriteMessage(ctx, &Message{
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
	case <-c.closeCh:
		return nil, fmt.Errorf("awaiting response: client closed")
	case <-ctx.Done():
		return nil, fmt.Errorf("awaiting response: %w", ctx.Err())
	}
}

// readMessages reads messages from the client connection and handles them.
// Cancel the context to stop reading messages
func (c *Client) readMessages() {
	// Create a context that is cancelled when the client is closed
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-c.closeCh
		cancel()
	}()

	// Read messages from the client connection
	for {
		var msg Message
		err := c.conn.ReadMessage(ctx, &msg)
		if err != nil {
			// Close the client
			err = fmt.Errorf("message read: %w", err)
			c.Close(StatusProtocolError, err.Error())
			return
		}

		if msg.IgnoreIfFalse != nil && *msg.IgnoreIfFalse == false {
			continue // ignore message
		}

		c.handleMessage(ctx, msg)
	}
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

		// Call handler with arguments
		result, err := callHandler(
			ctx, handler, msg.HandlerArguments(),
		)
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
	respCh <- MessageResponse{res, err}
	close(respCh)

	return nil
}
