package wrapper

import (
	"context"
	"fmt"
)

// AnonymousChannel is a request-scoped channel created when a handler returns
// *AnonymousChannel in response to a request. It allows streaming or
// multi-message patterns over a single WebSocket connection.
type AnonymousChannel struct {
	ClientChannel // inherits name (channel ID string) and Name()
	ctx           context.Context
	ctxCancel     context.CancelCauseFunc
}

// newAnonymousChannel creates an AnonymousChannel with the given ID and client.
// The channel's context is derived from the client connection context so it is
// automatically cancelled when the connection closes.
func newAnonymousChannel(
	ctx context.Context,
	id string,
	c *Client,
) *AnonymousChannel {
	ctx, ctxCancel := context.WithCancelCause(ctx)
	return &AnonymousChannel{
		ClientChannel: ClientChannel{name: id, client: c},
		ctx:           ctx,
		ctxCancel:     ctxCancel,
	}
}

// On adds an event handler for the specified event on this anonymous channel.
// See ClientChannel.On for handler signature details.
func (ch *AnonymousChannel) On(eventName string, handler any) *AnonymousChannel {
	c := ch.client
	if c != nil {
		key := handlerName{Channel: ch.name, Anonymous: true, Event: eventName}
		registerClientHandler(c, key, handler, false)
	}
	return ch
}

// Once adds a one-time event handler for the specified event on this anonymous
// channel. See ClientChannel.On for handler signature details.
func (ch *AnonymousChannel) Once(eventName string, handler any) *AnonymousChannel {
	c := ch.client
	if c != nil {
		key := handlerName{Channel: ch.name, Anonymous: true, Event: eventName}
		registerClientHandler(c, key, handler, true)
	}
	return ch
}

// Emit sends an event on this anonymous channel. The passed context can be used
// to cancel writing the message to the client. The first argument must be
// the event name string. Returns an error if the channel is closed or if the
// message could not be sent.
func (ch *AnonymousChannel) Emit(ctx context.Context, arguments ...any) error {
	c := ch.client
	if c == nil {
		return ChannelClosedError{Channel: ch.name}
	}
	_, err := checkEventName(arguments)
	if err != nil {
		return err
	}
	return c.sendEvent(ctx, ch.name, true, arguments...)
}

// Request sends a request on this anonymous channel and returns the response.
// The passed context can be used to cancel the request. The first argument must
// be the event name string.
func (ch *AnonymousChannel) Request(
	ctx context.Context, arguments ...any,
) (any, error) {
	c := ch.client
	if c == nil {
		return nil, ChannelClosedError{Channel: ch.name}
	}
	eventName, err := checkEventName(arguments)
	if err != nil {
		return nil, err
	}
	_ = eventName
	return c.sendRequest(ctx, ch.name, true, arguments...)
}

// Close removes all event handlers for this anonymous channel and cancels its
// context. It does NOT send an abort message to the remote end; use Abort for
// that. Close is idempotent and safe to call multiple times.
func (ch *AnonymousChannel) Close() error {
	return ch.closeWithCause(context.Canceled)
}

// closeWithCause closes the anonymous channel with a specific cause. This is
// used internally when the connection closes or when an abort is received from
// the remote end; it does not send an abort message.
func (ch *AnonymousChannel) closeWithCause(cause error) error {
	c := ch.client
	if c == nil {
		return nil // already closed
	}
	ch.client = nil

	ch.ctxCancel(cause)

	c.handlersMu.Lock()
	closeHandlersForChannel(ch.name, true, c.handlers, c.handlersOnce)
	c.handlersMu.Unlock()

	c.connReqMu.Lock()
	delete(c.anonChans, ch.name)
	c.connReqMu.Unlock()
	return nil
}

// Abort sends an abort message to the remote end and then closes the channel.
// The provided error is sent as the abort reason. If err is nil, it defaults
// to context.Canceled.
func (ch *AnonymousChannel) Abort(err error) error {
	c := ch.client
	if c == nil {
		return nil // already closed
	}
	if err == nil {
		err = context.Canceled
	}
	sendErr := c.sendAnonCancel(ch.ctx, ch.name, err)
	closeErr := ch.closeWithCause(err)
	if sendErr != nil {
		return fmt.Errorf("sending cancellation: %w", sendErr)
	}
	return closeErr
}

// Context returns a context that is cancelled when this anonymous channel is
// closed or aborted. The cancellation cause reflects the abort reason when the
// remote end sends an abort message.
func (ch *AnonymousChannel) Context() context.Context {
	return ch.ctx
}
