package wrapper

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
)

// This example shows how to use create a ws-server-wrapper that echoes a string
// sent to the "echo" event handler.
func ExampleServer_echo() {
	// Create ws-wrapper-server and "echo" event handler. Note that the event
	// handler function may optionally have a context as its first argument,
	// it must always return 2 values, and the second value must implement
	// error.
	wsServer := NewServer()
	wsServer.On("echo", func(s string) (string, error) {
		// If a ws-server client sends a "echo" request, it will receive the
		// echoed response.
		return s, nil
	})

	// Create HTTP handler function that upgrades the HTTP connection to a
	// WebSocket using the github.com/coder/websocket package.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Use your favorite WebSocket library to upgrade the HTTP request to
		//    a WebSocket connection...
		// 2. Use an adapter (or write your own) to write and parse ws-wrapper
		//    messages.
		// 3. Call `wsServer.Accept` to attach the underlying WebSocket
		//    connection to `wsServer`. It will start listening for inbound
		//    messages.
		// See full example in the coder adapter.
	})

	// Start the HTTP server
	log.Fatal(http.ListenAndServe("localhost:8080", h))
}

// Demonstrates how to add various event handlers.
func ExampleServerChannelOn() {
	wsServer := NewServer()

	// Simple event handler that does not return a response
	// Note: Since no channel name is provided, this handler is attached to the
	// "main" channel with no name.
	wsServer.On("print", func(s string) (struct{}, error) {
		fmt.Println(s)
		return struct{}{}, nil
	})

	// Simple request handler that echoes whatever was received
	wsServer.On("echo", func(s string) (string, error) {
		return s, nil
	})

	// Request handler that reads a file from disk. CAUTION: Be sure to sanitize
	// user input.
	// Note: json.Marshall will return a base64 string to the client because
	// `[]byte` is returned by the handler.
	wsServer.On("readFile", func(filename string) ([]byte, error) {
		return ioutil.ReadFile(filename)
	})
}

// Demonstrates adding event handlers to a particular channel.
func ExampleServerChannelOn_channel() {
	wsServer := NewServer()

	// Adds a "readFile" event handler to the "io" channel.
	wsServer.Of("io").On("readFile", func(filename string) ([]byte, error) {
		return ioutil.ReadFile(filename)
	})
}
