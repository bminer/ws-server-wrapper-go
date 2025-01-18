package wrapper

import "context"

// ClientChannel is a channel on which events can be sent and received. Events
// emitted or requests sent are sent to the specific client's channel on the
// remote end. See ClientChannel.On for more information about how received
// events are handled.
type ClientChannel struct {
	name   string
	client *Client
}

// On adds an event handler for the specified event to the channel. When an
// event or request is received from a client, only a single handler is called.
// The priority of handlers called is as follows:
//
// 1. Handlers added via ClientChannel.Once
//
// 2. Handlers added via ClientChannel.On
//
// 3. Handlers added via ServerChannel.Once
//
// 4. Handlers added via ServerChannel.On
//
// Therefore, if a handler is added to a ClientChannel, the corresponding
// handler on the ServerChannel will never be called for that particular client.
func (c ClientChannel) On(eventName string, handler any) ClientChannel {
	c.client.handlersMu.Lock()
	c.client.handlers[handlerName{Channel: c.name, Event: eventName}] = handler
	c.client.handlersMu.Unlock()
	return c
}

// Once adds a one-time event handler for the specified event to the channel.
// See ClientChannel.On for more information about how event handlers are
// called.
func (c ClientChannel) Once(eventName string, handler any) ClientChannel {
	c.client.handlersMu.Lock()
	c.client.handlersOnce[handlerName{Channel: c.name, Event: eventName}] = handler
	c.client.handlersMu.Unlock()
	return c
}

// Emit sends an event to the client on the specified channel. The passed
// context can be used to cancel writing the message to the client. The second
// argument is the event name that tells the remote end which event handler to
// call. Returns an error if there was an error sending the message to the
// client.
func (c ClientChannel) Emit(ctx context.Context, arguments ...any) error {
	return c.client.sendEvent(ctx, c.name, arguments...)
}

// Request sends a request to the client and returns the response. The passed
// context can be used to cancel the request. The second argument is the event
// name that tells the remote end which request handler to call.
func (c ClientChannel) Request(
	ctx context.Context, arguments ...any,
) (response any, err error) {
	return c.client.sendRequest(ctx, c.name, arguments...)
}

// ServerChannel is a channel on which events can be sent and received. Events
// emitted or requests sent are sent to all connected clients to the channel of
// the same name on the remote end. See ClientChannel.On for more information
// about how received events are handled.
type ServerChannel struct {
	name   string
	server *Server
}

// On adds an event handler for the specified event to the channel. See
// ClientChannel.On for more information about how event handlers are called.
func (c ServerChannel) On(eventName string, handler any) ServerChannel {
	c.server.handlersMu.Lock()
	c.server.handlers[handlerName{Channel: c.name, Event: eventName}] = handler
	c.server.handlersMu.Unlock()
	return c
}

// Once adds a one-time event handler for the specified event to the channel.
// See ClientChannel.On for more information about how event handlers are
// called.
func (c ServerChannel) Once(eventName string, handler any) ServerChannel {
	c.server.handlersMu.Lock()
	c.server.handlersOnce[handlerName{Channel: c.name, Event: eventName}] = handler
	c.server.handlersMu.Unlock()
	return c
}

// Emit sends an event to all clients on the specified channel. The passed
// context can be used to cancel writing the message to the client. The second
// argument is the event name that tells the remote end which event handler to
// call. Returns a slice of ClientErrors that contains an entry if an error
// occurred when sending the message to a specific client.
func (c ServerChannel) Emit(
	ctx context.Context, arguments ...any,
) (errs []ClientError) {
	c.server.clientsMu.Lock()
	defer c.server.clientsMu.Unlock()
	for client := range c.server.clients {
		err := client.conn.WriteMessage(ctx, &Message{
			Channel:   c.name,
			Arguments: arguments,
		})
		if err != nil {
			errs = append(errs, ClientError{
				Client: client,
				error:  err,
			})
		}
	}
	return
}
