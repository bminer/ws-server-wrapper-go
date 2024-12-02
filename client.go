package wrapper

import (
	"context"
	"fmt"
	"sync"
)

// Conn is an interface for a raw WebSocket connection
type Conn interface {
	// ReadMessage reads a single message from the connection
	ReadMessage(ctx context.Context, v *Message) error
	// WriteMessage writes a message to the connection
	WriteMessage(ctx context.Context, v *Message) error
	// Close performs the WebSocket close handshake with the given status code and reason
	Close(statusCode int, reason string) error
}

// Client represents a WebSocket client
type Client struct {
	ClientChannel
	conn         Conn // client WebSocket connection
	server       *Server
	mutex        *sync.RWMutex
	handlers     map[HandlerName]any
	handlersOnce map[HandlerName]any
}

func newClient(conn Conn, server *Server) *client {
	c := &client{
		conn:            conn,
		server:          server,
		mutex:           &sync.RWMutex{},
		handlers:        make(map[HandlerName]any),
		handlersOnce:    make(map[HandlerName]any),
		pendingRequests: make(map[int]chan Response),
	}
	// set reference back to client, so channel methods work properly
	c.ClientChannel.client = c
	return c
}

// Of returns a channel for the given name
func (c Client) Of(name string) ClientChannel {
	return ClientChannel{
		name:   name,
		client: c,
	}
}

// Abort cancels all pending requests
func (c Client) Abort() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for requestID, handler := range c.pendingRequests {
		handler(nil, fmt.Errorf("request was aborted"))
	}
	clear(c.pendingRequests)
}

// sendReject sends a reject / error response to a request
func (c Client) sendReject(ctx context.Context, requestID *int, err error) error {
	return c.conn.WriteMessage(ctx, &Message{
		RequestID:     requestID,
		ResponseError: err.Error(),
	})
}

// sendResolve sends a resolve / data response to a request
func (c Client) sendResolve(ctx context.Context, requestID *int, data any) error {
	return c.conn.WriteMessage(ctx, &Message{
		RequestID:    requestID,
		ResponseData: data,
	})
}

// handleMessage processes an inbound message for this client. Returns an error
// if there was an error sending the response to the client.
func (c Client) handleMessage(ctx context.Context, msg Message) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	eventName := msg.EventName()
	if eventName != "" {
		// Process inbound event/request
		handlerName := HandlerName{Channel: msg.Channel, Event: eventName}
		handler, ok := c.handlersOnce[handlerName]
		if ok {
			delete(c.handlersOnce, handlerName)
		} else {
			handler, ok = c.handlers[handlerName]
			if !ok {
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
		}
		// Call handler with arguments
		result, err := CallHandlerWithArguments(
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
		return nil // ignore invalid message
	}
	c.server.handleResponse(msg)
	resChan := c.server.pendingRequests[*msg.RequestID]
	if resChan == nil {
		return nil // ignore invalid message
	}

	// Process response to prior request
	resChan <- msg.Response()
}

type ClientChannel struct {
	name   string
	client *Client
}

func (c ClientChannel) On(eventName string, handler any) ClientChannel {
	c.client.mutex.Lock()
	c.client.handlers[HandlerName{Channel: c.name, Event: eventName}] = handler
	c.client.mutex.Unlock()
	return c
}

func (c ClientChannel) Once(eventName string, handler any) ClientChannel {
	c.client.mutex.Lock()
	c.client.handlersOnce[HandlerName{Channel: c.name, Event: eventName}] = handler
	c.client.mutex.Unlock()
	return c
}

func (c ClientChannel) Emit(ctx context.Context, arguments ...any) error {
	return c.client.conn.WriteMessage(ctx, &Message{
		Channel:   c.name,
		Arguments: arguments,
	})
}

// Request sends a request to the client and returns the response. The first
// argument is the context, the second argument is the event name, and the
// remaining arguments are the event handler arguments.
func (c ClientChannel) Request(
	ctx context.Context, arguments ...any,
) Response {
	return c.client.server.Of(c.name).RequestClient(
		ctx, c.client, arguments...,
	)
}
