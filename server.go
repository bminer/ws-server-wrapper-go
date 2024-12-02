package wrapper

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// HandlerName represents the name of a WebSocket event handler
type HandlerName struct {
	Channel string
	Event   string
}

// Server represents a WebSocket server
type Server struct {
	ServerChannel
	clientsMu       sync.RWMutex
	clients         map[*Client]struct{}
	handlersMu      sync.RWMutex
	handlers        map[HandlerName]any
	handlersOnce    map[HandlerName]any
	requestMu       sync.RWMutex
	requestID       int
	pendingRequests map[int]chan Response
}

// NewServer creates a new WebSocket server
func NewServer() *Server {
	wss := &Server{
		clients: make(map[*Client]struct{}),
	}
	// set reference back to server, so channel methods work properly
	wss.ServerChannel.server = wss
	return wss
}

// AddClient adds a new client connection to the server
func (server *Server) AddClient(ctx context.Context, conn Conn) {
	server.clientsMu.Lock()
	server.clients[conn] = struct{}{}
	server.clientsMu.Unlock()

	go server.readMessages(ctx, conn)
}

func (server *Server) readMessages(ctx context.Context, conn Conn) {
	for {
		var msg Message
		err := conn.ReadMessage(&msg)
		if err != nil {
			log.Printf("Error reading message: %v", err)
			conn.Close()
			server.clientsMu.Lock()
			delete(server.clients, conn)
			server.clientsMu.Unlock()
			return
		}

		if message.IgnoreIfFalse != nil && *message.IgnoreIfFalse == false {
			continue // ignore message
		}

		if message.ID != 0 && message.Data != nil {
			server.handleResponse(conn, message)
		} else if message.ID != 0 && message.Error != nil {
			server.handleRejection(conn, message)
		} else if message.ID != 0 {
			server.handleRequest(conn, message)
		} else {
			server.handleEvent(conn, message)
		}
	}
}

// handleEvent handles an response message for a pending request. Returns true
// if the message was handled, false otherwise.
func (server *Server) handleResponse(msg Message) bool {
	if msg.RequestID == nil {
		return false
	}
	server.requestMu.Lock()
	defer server.requestMu.Unlock()
	if ch, ok := server.pendingRequests[requestID]; ok {
		ch <- response
		return true
	}
	return false
}

// Of returns a channel on the server
func (srv *Server) Of(channel string) ServerChannel {
	return ServerChannel{
		name:   channel,
		server: srv,
	}
}

type ServerChannel struct {
	name   string
	server *WebSocketServer
}

func (c ServerChannel) On(eventName string, handler any) {
	c.server.handlersMu.Lock()
	c.server.handlers[handlerName{Channel: c.name, Event: eventName}] = handler
	c.server.handlersMu.Unlock()
}

func (c ServerChannel) Once(eventName string, handler any) {
	c.server.handlersMu.Lock()
	c.server.handlers[handlerName{Channel: c.name, Event: eventName}] = handler
	c.server.handlersMu.Unlock()
}

// Emit broadcasts an event to all clients
func (c ServerChannel) Emit(eventName string, arguments ...any) {
	c.server.clientsMu.RLock()
	defer c.server.clientsMu.RUnlock()
	for client := range c.server.clients {
		msg := Message{
			Channel:   c.name,
			Event:     eventName,
			Arguments: arguments,
		}
		err := client.conn.WriteMessage(&msg)
		if err != nil {
			c.server.RemoveClient(conn, err)
		}
	}
}

// RequestClient sends a request to the specified client
func (c ServerChannel) RequestClient(
	ctx context.Context,
	client *Client,
	arguments ...any,
) Response {
	if len(arguments) == 0 {
		return Response{Error: fmt.Errorf("no event name provided")}
	}

	// Configure the response handler
	c.server.requestMu.Lock()
	requestID := c.server.requestID
	c.server.pendingRequests[requestID] = make(chan Response, 1)
	c.server.requestID++
	c.server.requestMu.Unlock()
	defer func() {
		c.server.requestMu.Lock()
		delete(c.server.pendingRequests, requestID)
		c.server.requestMu.Unlock()
	}

	// Send the request
	err := client.conn.WriteMessage(ctx, &Message{
		Channel:   c.name,
		Arguments: arguments,
	})
	if err != nil {
		return Response{Error: fmt.Errorf("error sending request: %w", err)}
	}

	// Wait for the response
	select {
	case res := <-c.server.pendingRequests[requestID]:
		return res
	case <-ctx.Done():
		c.server.requestMu.Lock()
		delete(c.server.pendingRequests, requestID)
		c.server.requestMu.Unlock()
		return Response{Error: ctx.Err()}
	}
}
