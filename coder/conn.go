package coder

import (
	"context"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type conn struct {
	websocket.Conn
}

// ReadMessage reads a single message from the connection
func (c *conn) ReadMessage(ctx context.Context, msg *wrapper.Message) error {
	return wsjson.Read(ctx, c, msg)
}

// WriteMessage writes a message to the connection
func (c *conn) WriteMessage(ctx context.Context, msg *wrapper.Message) error {
	return wsjson.Write(ctx, c, msg)
}

// Close performs the WebSocket close handshake with the given status code and reason
func (c *conn) Close(statusCode int, reason string) error {
	return c.Conn.Close(coder.StatusCode(statusCode), reason)
}
