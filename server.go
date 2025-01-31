package wrapper

import (
	"fmt"
	"sync"
)

// Server represents a server that accepts WebSocket connections, handles
// inbound messages, and sends messages to connected clients. Rather than
// listening for connections itself, the Accept method takes any connection that
// implements the Conn interface. This allows the server to be used with any
// WebSocket library. Various adapter libraries are available in the adapters
// subdirectory.
type Server struct {
	ServerChannel     // the "main" server channel with no name
	clientsMu         sync.Mutex
	clients           map[*Client]struct{} // set to nil when server is closed
	handlersMu        sync.Mutex
	handlers          map[handlerName]any
	handlersOnce      map[handlerName]any
	requestMu         sync.Mutex
	requestID         int
	requestResponseCh map[int]chan messageResponse
}

// NewServer creates a new server.
func NewServer() *Server {
	s := &Server{
		clients:           make(map[*Client]struct{}),
		handlers:          make(map[handlerName]any),
		handlersOnce:      make(map[handlerName]any),
		requestResponseCh: make(map[int]chan messageResponse),
	}
	// set reference back to client, so channel methods work properly
	s.ServerChannel.server = s
	return s
}

// Accept adds a new client connection to the server. conn only needs to
// implement the Conn interface.
func (s *Server) Accept(conn Conn) error {
	s.clientsMu.Lock()

	if s.clients == nil {
		s.clientsMu.Unlock()
		return fmt.Errorf("server is closed and cannot accept connections")
	}

	// Add client to the set
	client := newClient(conn, s)
	s.clients[client] = struct{}{}
	s.clientsMu.Unlock()

	go client.readMessages()
	s.emitOpen(client)
	return nil
}

// Close closes the server and all connected clients. Returns the first error
// encountered while closing clients.
func (s *Server) Close() error {
	var clientErr error

	s.clientsMu.Lock()
	clients := s.clients
	s.clients = nil // stop accepting new connections
	s.clientsMu.Unlock()

	// Close all client connections
	for client := range clients {
		if err := client.closeWithoutLock(
			StatusGoingAway, "server is closing",
		); err != nil && clientErr == nil {
			clientErr = err
		}
	}

	// Abort all pending requests
	s.requestMu.Lock()
	for _, respCh := range s.requestResponseCh {
		respCh <- messageResponse{nil, fmt.Errorf("request aborted")}
		close(respCh)
	}
	clear(s.requestResponseCh)
	s.requestMu.Unlock()

	return clientErr
}

// Of returns a channel for the given name
func (s *Server) Of(name string) *ServerChannel {
	return &ServerChannel{
		name:   name,
		server: s,
	}
}

// emitOpen calls the "open" and "connect" event handlers on the main channel
func (s *Server) emitOpen(c *Client) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(OpenHandler); ok {
				f(c)
				return true
			}
			return false
		},
		&s.handlersMu, s.handlers, s.handlersOnce,
		"open", "connect",
	)
}

// emitError calls the "error" event handler on the main channel
func (s *Server) emitError(c *Client, err error) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(ErrorHandler); ok {
				f(c, err)
				return true
			}
			return false
		},
		&s.handlersMu, s.handlers, s.handlersOnce,
		"error",
	)
}

// emitClose calls the "close" and "disconnect" event handlers on the main
// channel
func (s *Server) emitClose(c *Client, status StatusCode, reason string) bool {
	return emitReserved(
		func(f any) bool {
			if f, ok := f.(CloseHandler); ok {
				f(c, status, reason)
				return true
			}
			return false
		},
		&s.handlersMu, s.handlers, s.handlersOnce,
		"close", "disconnect",
	)
}
