package coder

import (
	"context"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Wrap wraps a coder/websocket Conn as a wrapper.Conn
func Wrap(c *websocket.Conn) wrapper.Conn {
	return conn{c}
}

// conn wraps a coder/websocket.Conn
type conn struct {
	*websocket.Conn
}

// ReadMessage reads a single message from the connection
func (c conn) ReadMessage(ctx context.Context, msg *wrapper.Message) error {
	return wsjson.Read(ctx, c.Conn, msg)
}

// WriteMessage writes a message to the connection
func (c conn) WriteMessage(ctx context.Context, msg *wrapper.Message) error {
	return wsjson.Write(ctx, c.Conn, msg)
}

// Close performs the WebSocket close handshake with the given status code and reason
func (c conn) Close(statusCode wrapper.StatusCode, reason string) error {
	return c.Conn.Close(websocket.StatusCode(statusCode), reason)
}

// CloseNow closes the WebSocket connection without attempting a close handshake
func (c conn) CloseNow() error {
	return c.Conn.CloseNow()
}
