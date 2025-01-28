package wrapper

import (
	"context"
	"fmt"
)

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
//
// handler must be a function with arbitrary parameters, but it must return
// one or two values: a request result and an error. The request result is
// optional and may be of any type, but the error is required and must implement
// the error interface. When the event handler is called, the arguments of the
// event are converted to the types expected by handler. If the types do not
// match, the handler is not called and an error is returned to the client.
// Optionally, the handler can provide an additional parameter for the
// context.Context of the request. Call ctx.Value(ClientKey) to return the
// *wrapper.Client object for the client that emitted the event.
//
// There are also reserved events can occur on the main channel of a client:
//
//   - "error" - called when an error occurs on the client. The handler is
//     passed the error as a single argument and has the form
//     `func(*Client, error)`
//
//   - "message" - called when a message is received from the client. The
//     handler is passed the Message as a single argument and has the form
//     `func(*Client, Message)`. The handler may not modify the message; this
//     event is primarily for logging purposes.
//
//   - "close" or "disconnect" - called when the client disconnects. The handler
//     is passed the status code and reason and has the form
//     `func(*Client, StatusCode, string)`
//
// The reserved events that can occur on the main channel of a server include:
//
//   - "open" or "connect" - called when a new client connects. The handler is
//     passed the client and has the form `func(*Client)`
//
//   - "error"
//
//   - "close" or "disconnect" - called when any client disconnects.
//
// If event handlers do not conform to the expected function signature, On will
// panic.
//
// If On is called multiple times for the same event name, the last handler
// will be used. If handler is nil, the event handler is removed.
func (c ClientChannel) On(eventName string, handler any) ClientChannel {
	if err := checkHandler(c.name, eventName, handler); err != nil {
		panic(err)
	}
	key := handlerName{Channel: c.name, Event: eventName}
	c.client.handlersMu.Lock()
	if handler == nil {
		delete(c.client.handlers, key)
	} else {
		c.client.handlers[key] = handler
	}
	c.client.handlersMu.Unlock()
	return c
}

// Once adds a one-time event handler for the specified event to the channel.
// See ClientChannel.On for more information about how event handlers are
// called.
func (c ClientChannel) Once(eventName string, handler any) ClientChannel {
	if err := checkHandler(c.name, eventName, handler); err != nil {
		panic(err)
	}
	key := handlerName{Channel: c.name, Event: eventName}
	c.client.handlersMu.Lock()
	if handler == nil {
		delete(c.client.handlersOnce, key)
	} else {
		c.client.handlersOnce[key] = handler
	}
	c.client.handlersMu.Unlock()
	return c
}

// checkEventName ensures the event name is valid and returns it as a string.
func checkEventName(arguments []any) (string, error) {
	if len(arguments) < 1 {
		return "", fmt.Errorf("event name is required")
	}
	name, ok := arguments[0].(string)
	if !ok {
		return "", fmt.Errorf("event name must be a string")
	}
	return name, nil
}

// Emit sends an event to the client on the specified channel. The passed
// context can be used to cancel writing the message to the client. The second
// argument is the event name that tells the remote end which event handler to
// call. Returns an error if there was an error sending the message to the
// client.
func (c ClientChannel) Emit(ctx context.Context, arguments ...any) error {
	eventName, err := checkEventName(arguments)
	if err != nil {
		return err
	}
	if c.name == "" && IsReservedEvent(eventName) {
		return fmt.Errorf(
			"cannot emit reserved event '%s' on main channel", eventName,
		)
	}
	return c.client.sendEvent(ctx, c.name, arguments...)
}

// Request sends a request to the client and returns the response. The passed
// context can be used to cancel the request. The second argument is the event
// name that tells the remote end which request handler to call.
func (c ClientChannel) Request(
	ctx context.Context, arguments ...any,
) (response any, err error) {
	eventName, err := checkEventName(arguments)
	if err != nil {
		return nil, err
	}
	if c.name == "" && IsReservedEvent(eventName) {
		return nil, fmt.Errorf(
			"cannot emit reserved event '%s' on main channel", eventName,
		)
	}
	return c.client.sendRequest(ctx, c.name, arguments...)
}

// Name returns the name of the channel
func (c ClientChannel) Name() string {
	return c.name
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
	if err := checkHandler(c.name, eventName, handler); err != nil {
		panic(err)
	}
	key := handlerName{Channel: c.name, Event: eventName}
	c.server.handlersMu.Lock()
	if handler == nil {
		delete(c.server.handlers, key)
	} else {
		c.server.handlers[key] = handler
	}
	c.server.handlersMu.Unlock()
	return c
}

// Once adds a one-time event handler for the specified event to the channel.
// See ClientChannel.On for more information about how event handlers are
// called.
func (c ServerChannel) Once(eventName string, handler any) ServerChannel {
	if err := checkHandler(c.name, eventName, handler); err != nil {
		panic(err)
	}
	key := handlerName{Channel: c.name, Event: eventName}
	c.server.handlersMu.Lock()
	if handler == nil {
		delete(c.server.handlersOnce, key)
	} else {
		c.server.handlersOnce[key] = handler
	}
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
	eventName, err := checkEventName(arguments)
	if err != nil {
		errs = append(errs, ClientError{Client: nil, error: err})
		return
	}
	if c.name == "" && IsReservedEvent(eventName) {
		errs = append(errs, ClientError{Client: nil, error: fmt.Errorf(
			"cannot emit reserved event '%s' on main channel", eventName,
		)})
		return
	}
	c.server.clientsMu.Lock()
	defer c.server.clientsMu.Unlock()

	for client := range c.server.clients {
		err := client.sendEvent(ctx, c.name, arguments...)
		if err != nil {
			errs = append(errs, ClientError{
				Client: client,
				error:  err,
			})
		}
	}
	return
}

// Name returns the name of the channel
func (c ServerChannel) Name() string {
	return c.name
}
