package main

import (
	"context"
	"log"
	"net"
	"net/http"

	wrapper "github.com/bminer/ws-server-wrapper-go"
	"github.com/bminer/ws-server-wrapper-go/adapters/coder"
	"github.com/coder/websocket"
)

func main() {
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
			log.Println("WebSocket connection error:", err)
			return
		}
		log.Println("WebSocket connection accepted")
		// Attach the WebSocket connection to ws-server-wrapper and start
		// listening for inbound messages.
		wsServer.Accept(coder.Wrap(conn))
	})

	// Start the HTTP server
	listener, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Println("Listening on ws://localhost:8080/")
		log.Fatal(http.Serve(listener, h))
	}()

	// Try connecting
	ctx := context.Background()
	client, _, err := websocket.Dial(ctx, "ws://localhost:8080/", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close(websocket.StatusNormalClosure, "client closed")

	// Send "echo" request
	// Normally this would be done by a WebSocket on a web browser using the
	// ws-wrapper library.
	log.Println("Sending echo request")
	reqJSON := []byte(`{"a":["echo","Hello, world!"],"i":1}`)
	err = client.Write(ctx, websocket.MessageText, reqJSON)
	if err != nil {
		log.Fatal(err)
	}

	// Read "echo" response
	msgType, res, err := client.Read(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Response (%s): %s", msgType, res)
	if msgType != websocket.MessageText {
		log.Fatal("Expected text message type")
	}
}
