package wrapper

import "context"

// clientKey is the context key to inject the *wrapper.Client object that
// emitted the event into event handler contexts.
type clientKey struct{}

// ClientFromContext returns the WebSocket client from the given context.
// Returns nil if not available.
func ClientFromContext(ctx context.Context) *Client {
	c, ok := ctx.Value(clientKey{}).(*Client)
	if !ok {
		return nil
	}
	return c
}

// anonChannelKey is the context key used to inject the anonymous channel
// factory function into event handler contexts.
type anonChannelKey struct{}

// Channel returns the *AnonymousChannel for the current request handler
// context, creating it lazily on the first call. It returns nil if called
// outside of a request handler.
func Channel(ctx context.Context) *AnonymousChannel {
	factory, _ := ctx.Value(anonChannelKey{}).(func() *AnonymousChannel)
	if factory == nil {
		return nil
	}
	return factory()
}
