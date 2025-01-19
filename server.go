package wrapper

import (
	"sync"
)

// Server represents a server that accepts WebSocket connections, handles
// inbound messages, and sends messages to connected clients. Rather than
// listening for connections itself, the Accept method takes any connection that
// implements the Conn interface. This allows the server to be used with any
// WebSocket library. Various adapter libraries are available in the adapters
// subdirectory.
type Server struct {
	ServerChannel                   // the "main" server channel with no name
	closeCh           chan struct{} // closed when the server is closed
	clientsMu         sync.Mutex
	clients           map[*Client]struct{}
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
		closeCh:           make(chan struct{}),
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
func (s *Server) Accept(conn Conn) {
	client := newClient(conn, s)
	s.clientsMu.Lock()
	s.clients[client] = struct{}{}
	s.clientsMu.Unlock()

	go client.readMessages()
}

// Close closes the server and all connected clients. Returns the first error
// encountered while closing clients.
func (s *Server) Close() error {
	var clientErr error
	close(s.closeCh)

	// Close all client connections
	for client := range s.clients {
		if err := client.Close(
			StatusGoingAway, "server is closing",
		); err != nil && clientErr == nil {
			clientErr = err
		}
	}

	// Abort all pending requests
	// ...

	return clientErr
}

// Of returns a channel for the given name
func (s *Server) Of(name string) *ServerChannel {
	return &ServerChannel{
		name:   name,
		server: s,
	}
}
