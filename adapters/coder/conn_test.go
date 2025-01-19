package coder

import (
	"log"
	"net/http"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/coder/websocket"
)

func Example() {
	// Create ws-wrapper-server and "echo" event handler. Note that the event
	// handler function may optionally have a context as its first argument,
	// it must always return 2 values, and the second value must implement
	// error.
	wsServer := wrapper.NewServer()
	wsServer.On("echo", func(s string) (string, error) {
		// If a ws-server client sends a "echo" request, it will receive the
		// echoed response.
		return s, nil
	})

	// Create HTTP handler function that upgrades the HTTP connection to a
	// WebSocket using the github.com/coder/websocket package.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// options go here (i.e. cross-origin setting)...
		})
		if err != nil {
			// websocket.Accept already writes the HTTP response
			return
		}
		// Attach the WebSocket connection to ws-server-wrapper and start
		// listening for inbound messages.
		wsServer.Accept(Wrap(conn))
	})

	// Start the HTTP server
	log.Fatal(http.ListenAndServe("localhost:8080", h))
}
