package wrapper

import "context"

type contextKey string

// ClientKey is the value to be passed to the context's Value method to return
// the *wrapper.Client object that emitted the event.
const ClientKey = contextKey("client")

// ClientFromContext returns the WebSocket client from the given context.
// Returns nil if not available.
func ClientFromContext(ctx context.Context) *Client {
	c, ok := ctx.Value(ClientKey).(*Client)
	if !ok {
		return nil
	}
	return c
}
