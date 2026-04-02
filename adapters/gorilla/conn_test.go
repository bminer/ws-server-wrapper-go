package gorilla

import (
	"log"
	"net/http"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func Example() {
	// Create a ws-wrapper server and register an "echo" request handler.
	wsServer := wrapper.NewServer()
	wsServer.On("echo", func(s string) (string, error) {
		return s, nil
	})

	// Create an HTTP handler that upgrades the connection to WebSocket using
	// gorilla/websocket and hands it off to ws-server-wrapper.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// upgrader.Upgrade already wrote the HTTP error response
			return
		}
		// Attach the WebSocket connection to ws-server-wrapper and start
		// listening for inbound messages.
		if err = wsServer.Accept(Wrap(conn)); err != nil {
			// wsServer.Accept already closed the conn
			return
		}
	})

	// Start the HTTP server
	log.Fatal(http.ListenAndServe("localhost:8080", h))
}
