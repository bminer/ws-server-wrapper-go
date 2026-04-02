// This package enables the use of the github.com/gorilla/websocket package
// with the ws-server-wrapper library. It provides an adapter between a
// *websocket.Conn from github.com/gorilla/websocket and a Conn in the
// ws-server-wrapper library.
//
// Note: gorilla/websocket allows one concurrent reader and one concurrent
// writer. This adapter serializes all outbound writes with an internal mutex,
// so callers do not need to take any special precautions.
package gorilla

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/gorilla/websocket"
)

// Wrap wraps a *websocket.Conn from github.com/gorilla/websocket as a
// wrapper.Conn that can be passed to wrapper.Server.Accept or
// wrapper.Client.Bind.
func Wrap(c *websocket.Conn) wrapper.Conn {
	return &conn{Conn: c}
}

// conn implements the wrapper.Conn interface for a gorilla *websocket.Conn.
type conn struct {
	*websocket.Conn
	writeMu sync.Mutex
}

// ReadMessage reads a single JSON message from the connection. It respects
// context cancellation by expiring the read deadline, causing the underlying
// gorilla ReadMessage call to unblock.
func (c *conn) ReadMessage(ctx context.Context, msg *wrapper.Message) error {
	// If context can be cancelled
	if ctx.Done() != nil {
		// Spawn a goroutine to monitor cancellation / ReadMessage completion
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				// Interrupt the blocking read by setting a read deadline in the
				// past. This is an idomatic way to abort the read.
				_ = c.Conn.SetReadDeadline(time.Unix(0, 1))
				// Wait for ReadMessage to finish (probably return an error)
				<-done
				// Clear the deadline by setting it to the zero value
				_ = c.Conn.SetReadDeadline(time.Time{})
			case <-done:
				// Read completed normally
			}
		}()
	}

	// Note: message type is ignored
	_, data, err := c.Conn.ReadMessage()
	if err != nil {
		// Prioritize context cancellation error
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return json.Unmarshal(data, msg)
}

// WriteMessage encodes msg as JSON and sends it as a WebSocket text frame.
// A write mutex ensures at most one writer is active at a time, as required by
// gorilla/websocket.
func (c *conn) WriteMessage(ctx context.Context, msg *wrapper.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	if ctx.Done() == nil {
		defer c.writeMu.Unlock()
	} else {
		// Spawn a goroutine to monitor cancellation / WriteMessage completion
		// Note: `writeMu` unlocked when goroutine completes
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				// Interrupt the blocking write by setting a deadline in the past.
				_ = c.Conn.SetWriteDeadline(time.Unix(0, 1))
				// Wait for WriteMessage to finish
				<-done
				// Clear the deadline by setting it to the zero value
				_ = c.Conn.SetWriteDeadline(time.Time{})
			case <-done:
				// Write completed normally
			}
			c.writeMu.Unlock()
		}()
	}
	writeErr := c.Conn.WriteMessage(websocket.TextMessage, data)
	// Prioritize context cancellation error
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return writeErr
}

// Close performs the WebSocket close handshake with the given status code and
// reason, then closes the underlying network connection. WriteControl is used
// for the close frame because gorilla allows it to be called concurrently with
// other write methods.
func (c *conn) Close(statusCode wrapper.StatusCode, reason string) error {
	msg := websocket.FormatCloseMessage(int(statusCode), reason)
	err := c.Conn.WriteControl(
		websocket.CloseMessage, msg, time.Now().Add(5*time.Second),
	)
	// Prioritize WriteControl error
	if closeErr := c.Conn.Close(); err == nil {
		err = closeErr
	}
	return err
}

// CloseNow closes the underlying network connection immediately without
// attempting a WebSocket close handshake.
func (c *conn) CloseNow() error {
	return c.Conn.Close()
}
