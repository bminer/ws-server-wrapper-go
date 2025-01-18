package wrapper

import "context"

// Conn represents a WebSocket connection that can read and write messages.
// ReadMessage may not be called concurrently, and one must always read from the
// connection to properly handle control frames.
type Conn interface {
	// ReadMessage reads a message from the WebSocket connection. The context
	// is used to cancel the read operation.
	ReadMessage(ctx context.Context, msg *Message) error
	// WriteMessage writes a message to the WebSocket connection. The context
	// is used to cancel the write operation.
	WriteMessage(ctx context.Context, msg *Message) error
	// Close closes the WebSocket connection with the given status code and
	// reason.
	Close(statusCode StatusCode, reason string) error
	// CloseNow closes the WebSocket connection immediately without waiting for
	// any pending operations to complete.
	CloseNow() error
}
