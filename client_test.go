package wrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"sync"
	"testing"
	"time"
)

// mockConn is a simple in-memory Conn implementation for testing. It simulates
// a WebSocket connection without any network I/O:
//
//   - Inbound messages (server ← client) are injected via send().
//   - Outbound messages (server → client) are forwarded to writeCh so tests
//     can block on waitWritten() until a specific response arrives.
//   - Calling Close() closes closeCh, which unblocks any pending ReadMessage
//     call, causing it to return context.Canceled.
type mockConn struct {
	readCh  chan Message  // inbound messages fed by send()
	writeCh chan Message  // mirrors each WriteMessage call for waitWritten()
	closeCh chan struct{} // closed once to signal connection close
}

func newMockConn() *mockConn {
	return &mockConn{
		readCh:  make(chan Message, 16),
		writeCh: make(chan Message, 16),
		closeCh: make(chan struct{}),
	}
}

// ReadMessage blocks until a message is available on readCh, the connection is
// closed, or the context is cancelled.
func (m *mockConn) ReadMessage(ctx context.Context, msg *Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closeCh:
		return context.Canceled
	case *msg = <-m.readCh:
		return nil
	}
}

// WriteMessage forwards the message to writeCh. Returns an error if the
// context is cancelled or the channel is full.
func (m *mockConn) WriteMessage(ctx context.Context, msg *Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.writeCh <- *msg:
		return nil
	default:
		return fmt.Errorf("message dropped")
	}
}

// Close unblocks any pending ReadMessage call. Idempotent — safe to call
// multiple times.
func (m *mockConn) Close(_ StatusCode, _ string) error {
	select {
	case <-m.closeCh:
	default:
		close(m.closeCh)
	}
	return nil
}

func (m *mockConn) CloseNow() error {
	return m.Close(0, "")
}

// send enqueues a message to be returned by the next ReadMessage call,
// simulating an inbound message from the remote client.
func (m *mockConn) send(msg Message) {
	m.readCh <- msg
}

// waitWritten blocks until a message is written to the conn or the timeout expires.
func (m *mockConn) waitWritten(t *testing.T, timeout time.Duration) Message {
	t.Helper()
	select {
	case msg := <-m.writeCh:
		return msg
	case <-time.After(timeout):
		t.Fatal("timed out waiting for written message")
		return Message{}
	}
}

// TestServerOpenHandler verifies the server-level "open" event fires
// synchronously when a client connects via Accept.
func TestServerOpenHandler(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedClient *Client
	server.On("open", func(c *Client) {
		capturedClient = c
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	if capturedClient == nil {
		t.Fatal("open handler did not fire when client connected")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestServerCloseHandler verifies the server-level "close" event fires when
// a client's connection is closed.
func TestServerCloseHandler(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	closeFired := make(chan struct{}, 1)
	server.On("close", func(c *Client, status StatusCode, reason string) {
		closeFired <- struct{}{}
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	conn.Close(StatusNormalClosure, "done")

	select {
	case <-closeFired:
	case <-time.After(2 * time.Second):
		t.Fatal("server close handler did not fire")
	}
}

// TestServerClose verifies that Server.Close closes all clients and prevents
// new connections from being accepted.
func TestServerClose(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("unexpected error closing server: %v", err)
	}

	conn2 := newMockConn()
	if err := server.Accept(conn2); err == nil {
		t.Fatal("expected error accepting connection on closed server")
	}
}

// TestClientCloseDirectly verifies that calling Client.Close fires the
// server-level close handler with the correct status code.
func TestClientCloseDirectly(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedClient *Client
	server.On("open", func(c *Client) {
		capturedClient = c
	})

	closeFired := make(chan StatusCode, 1)
	server.On("close", func(c *Client, status StatusCode, reason string) {
		if c == capturedClient {
			closeFired <- status
		}
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	if capturedClient == nil {
		t.Fatal("open handler did not fire")
	}

	if err := capturedClient.Close(StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}

	select {
	case status := <-closeFired:
		if status != StatusNormalClosure {
			t.Fatalf("expected StatusNormalClosure, got %v", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server close handler did not fire")
	}
}

// TestEchoRequest verifies the basic request/response cycle: a client sends
// a request, the server handler runs, and the response is sent back.
func TestEchoRequest(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("echo", func(s string) (string, error) {
		return "echo: " + s, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"echo"`), []byte(`"hello"`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != reqID {
		t.Fatalf("expected response for request %d, got %+v", reqID, resp)
	}
	if resp.ResponseData != "echo: hello" {
		t.Fatalf("expected response data 'echo: hello', got %v", resp.ResponseData)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestHandlerReturnsError verifies that when a handler returns an error,
// the client receives a reject/error response.
func TestHandlerReturnsError(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("fail", func() (string, error) {
		return "", fmt.Errorf("handler failed")
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"fail"`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.ResponseError == nil {
		t.Fatal("expected error response, got none")
	}
	// Expect error to be encoded as JS Error
	if !resp.ResponseJSError {
		t.Fatal("expected error encoded as JS error")
	}
	if exp := map[string]any{
		"message": "handler failed",
	}; !reflect.DeepEqual(exp, resp.ResponseError) {
		t.Fatalf("expected %v, got %v", exp, resp.ResponseError)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestEventFireAndForget verifies that events sent without a request ID
// invoke the handler without sending any response.
func TestEventFireAndForget(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	called := make(chan struct{})
	server.On("notify", func(s string) (string, error) {
		close(called)
		return "response ignored when not a request", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	// No RequestID — fire and forget.
	conn.send(Message{
		Arguments: []json.RawMessage{[]byte(`"notify"`), []byte(`"hello"`)},
	})

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called within timeout")
	}

	// No response should have been written.
	select {
	case msg := <-conn.writeCh:
		t.Fatalf("unexpected response message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestMissingHandlerReturnsError verifies that a request for an event with
// no registered handler returns an error response to the client.
func TestMissingHandlerReturnsError(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 5
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"nonexistent"`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != reqID {
		t.Fatalf("expected error response for request %d, got %+v", reqID, resp)
	}
	if resp.ResponseError == nil {
		t.Fatal("expected error response, got none")
	}
	if !resp.ResponseJSError {
		t.Fatal("expected error encoded as JS error")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestOnceHandler verifies that a handler registered with Once is called only
// once; subsequent identical requests receive an error response.
func TestOnceHandler(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	callCount := 0
	server.Once("ping", func() (string, error) {
		callCount++
		return "pong", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	// Issue two ping requests back to back
	reqID1 := 1
	conn.send(Message{
		RequestID: &reqID1,
		Arguments: []json.RawMessage{[]byte(`"ping"`)},
	})
	reqID2 := 2
	conn.send(Message{
		RequestID: &reqID2,
		Arguments: []json.RawMessage{[]byte(`"ping"`)},
	})

	// Collect both responses — they may arrive in any order because req2's
	// "no handler" error is sent synchronously while req1's "pong" is sent
	// from a goroutine.
	ra := conn.waitWritten(t, time.Second)
	rb := conn.waitWritten(t, time.Second)

	responses := map[int]Message{}
	for _, r := range []Message{ra, rb} {
		if r.RequestID == nil {
			t.Fatalf("response missing request ID: %+v", r)
		}
		responses[*r.RequestID] = r
	}

	resp1, ok1 := responses[reqID1]
	resp2, ok2 := responses[reqID2]
	if !ok1 || !ok2 {
		t.Fatalf("did not receive responses for both requests; got IDs: %v",
			responses)
	}

	if resp1.ResponseData != "pong" {
		t.Fatalf("expected 'pong' for req1, got %v", resp1.ResponseData)
	}
	if resp2.ResponseError == nil {
		t.Fatal("expected error for req2, got none")
	}
	if !resp2.ResponseJSError {
		t.Fatal("expected error encoded as JS error")
	}

	if callCount != 1 {
		t.Fatalf("expected handler to be called once, got %d", callCount)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestChannelRouting verifies that events sent on a named channel are routed
// to the correct channel handler and not the main-channel handler.
func TestChannelRouting(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	mainCalled := make(chan struct{}, 1)
	ioCalled := make(chan struct{}, 1)

	server.On("action", func() (string, error) {
		mainCalled <- struct{}{}
		return "main", nil
	})
	server.Of("io").On("action", func() (string, error) {
		ioCalled <- struct{}{}
		return "io", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	// Request on the "io" channel.
	reqID1 := 1
	conn.send(Message{
		Channel:   "io",
		RequestID: &reqID1,
		Arguments: []json.RawMessage{[]byte(`"action"`)},
	})
	resp1 := conn.waitWritten(t, time.Second)
	if resp1.ResponseData != "io" {
		t.Fatalf("expected 'io', got %v", resp1.ResponseData)
	}
	select {
	case <-ioCalled:
	default:
		t.Fatal("io handler was not called")
	}
	select {
	case <-mainCalled:
		t.Fatal("main handler should not have been called for io channel event")
	default:
	}

	// Request on the main channel.
	reqID2 := 2
	conn.send(Message{
		RequestID: &reqID2,
		Arguments: []json.RawMessage{[]byte(`"action"`)},
	})
	resp2 := conn.waitWritten(t, time.Second)
	if resp2.ResponseData != "main" {
		t.Fatalf("expected 'main', got %v", resp2.ResponseData)
	}
	select {
	case <-mainCalled:
	default:
		t.Fatal("main handler was not called")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestClientGetSet verifies that arbitrary data can be stored on and retrieved
// from a Client.
func TestClientGetSet(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedClient *Client
	server.On("open", func(c *Client) {
		capturedClient = c
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	if capturedClient == nil {
		t.Fatal("open handler did not fire")
	}

	capturedClient.Set("key", "value")
	if got := capturedClient.Get("key"); got != "value" {
		t.Fatalf("expected 'value', got %v", got)
	}
	if got := capturedClient.Get("missing"); got != nil {
		t.Fatalf("expected nil for missing key, got %v", got)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestClientFromContext verifies that ClientFromContext returns the correct
// *Client from inside an event handler.
func TestClientFromContext(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	clientInHandler := make(chan *Client, 1)
	server.On("whoami", func(ctx context.Context) (string, error) {
		clientInHandler <- ClientFromContext(ctx)
		return "ok", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"whoami"`)},
	})

	// The handler sends to clientInHandler before returning, and the response
	// is sent after the handler returns — so waitWritten guarantees the channel
	// has been written to by the time it unblocks.
	conn.waitWritten(t, time.Second)
	if c := <-clientInHandler; c == nil {
		t.Fatal("ClientFromContext returned nil")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestSetHandlerContext verifies that a HandlerContextFunc set via
// SetHandlerContext is applied before each handler invocation.
func TestSetHandlerContext(t *testing.T) {
	type ctxKey string

	server := NewServer()
	conn := newMockConn()

	server.SetHandlerContext(func(ctx context.Context, channel, event string) context.Context {
		return context.WithValue(ctx, ctxKey("event"), event)
	})

	gotValue := make(chan string, 1)
	server.On("greet", func(ctx context.Context) (string, error) {
		v, _ := ctx.Value(ctxKey("event")).(string)
		gotValue <- v
		return "hello", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"greet"`)},
	})

	// Same happens-before guarantee as TestClientFromContext.
	conn.waitWritten(t, time.Second)
	if v := <-gotValue; v != "greet" {
		t.Fatalf("expected context value 'greet', got %q", v)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestSetHandlerContextCancellation verifies that when SetHandlerContext
// returns an already-cancelled context, the handler detects the cancellation
// and context.Cause reflects the reason passed to cancel.
func TestSetHandlerContextCancellation(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	wantCause := fmt.Errorf("handler context cancelled by middleware")
	server.SetHandlerContext(func(ctx context.Context, channel, event string) context.Context {
		ctx, cancel := context.WithCancelCause(ctx)
		cancel(wantCause)
		return ctx
	})

	handlerCause := make(chan error, 1)
	server.On("work", func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			handlerCause <- context.Cause(ctx)
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "done", nil
		}
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"work"`)},
	})

	// Response is sent after the handler returns; the handler sends to
	// handlerCause before returning, so waitWritten guarantees it's ready.
	conn.waitWritten(t, 2*time.Second)
	if got := <-handlerCause; got != wantCause {
		t.Fatalf("expected cause %v, got %v", wantCause, got)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestHandlerCancellationReason verifies that the reason string in a
// cancellation message is surfaced as context.Cause inside the handler.
// Note: handleMessage stores the cancel func in inboundCancels before
// launching the handler goroutine, so no handlerStarted signal is needed —
// by the time readMessages loops to the cancellation message the cancel func
// is already registered.
func TestHandlerCancellationReason(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	handlerCause := make(chan error, 1)
	server.On("slow", func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			handlerCause <- context.Cause(ctx)
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "done", nil
		}
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"slow"`)},
	})
	conn.send(Message{
		RequestID:    &reqID,
		CancelReason: "user aborted",
	})

	// Response is sent after the handler returns; the handler sends to
	// handlerCause before returning, so waitWritten guarantees it's ready.
	conn.waitWritten(t, 2*time.Second)
	if got := <-handlerCause; got == nil || got.Error() != "user aborted" {
		t.Fatalf("expected cause 'user aborted', got %v", got)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestHandlerCancellationAfterCompletion verifies that a cancellation message
// arriving after the handler has already finished is a harmless no-op.
func TestHandlerCancellationAfterCompletion(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("fast", func(ctx context.Context) (string, error) {
		return "result", nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 7
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"fast"`)},
	})

	// Wait for the handler to complete and its inboundCancels entry to be removed.
	conn.waitWritten(t, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Send a late cancellation — should be a no-op (no panic, no error).
	conn.send(Message{
		RequestID:    &reqID,
		CancelReason: "Request aborted",
	})
	time.Sleep(50 * time.Millisecond)

	conn.Close(StatusNormalClosure, "done")
}

// ExampleNewClient shows how to create a standalone WebSocket client. Handlers
// are registered before Bind is called, so no inbound message can arrive
// before the handlers are in place. The same client can be reused for
// reconnection by calling Bind again with a new connection.
func ExampleNewClient() {
	client := NewClient()

	client.On("open", func(c *Client) {
		log.Println("connected to server")
	})
	client.On("close", func(c *Client, status StatusCode, reason string) {
		log.Println("disconnected:", reason)
		// To reconnect, dial a new connection and call Bind again:
		// conn, _ := websocket.Dial(ctx, "ws://example.com/ws", nil)
		// c.Bind(coder.Wrap(conn))
	})
	client.On("news", func(headline string) error {
		log.Println("breaking news:", headline)
		return nil
	})

	// For this runnable example we use a mockConn in place of a real dial.
	conn := newMockConn()
	client.Bind(conn) // "open" fires here; reading starts here

	_ = client.Emit(context.Background(), "subscribe", "sports")
	conn.Close(StatusNormalClosure, "done")
}

// TestNewClient_NoConn verifies that NewClient without a conn creates a client
// whose event handlers can be registered and whose Bind can be called later.
func TestNewClient_NoConn(t *testing.T) {
	client := NewClient()

	openFired := make(chan struct{})
	client.On("open", func(c *Client) {
		if c != client {
			t.Errorf("open handler received wrong client")
		}
		close(openFired)
	})

	conn := newMockConn()
	client.Bind(conn)

	select {
	case <-openFired:
	case <-time.After(time.Second):
		t.Fatal("open handler did not fire after Bind")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestNewClient_WithConn verifies the shorthand form: passing a conn directly
// to NewClient fires the "open" handler immediately.
func TestNewClient_WithConn(t *testing.T) {
	openFired := make(chan struct{})
	conn := newMockConn()

	client := NewClient() // handlers registered before Bind for safety
	client.On("open", func(c *Client) {
		close(openFired)
	})
	client.Bind(conn)

	select {
	case <-openFired:
	case <-time.After(time.Second):
		t.Fatal("open handler did not fire")
	}
	_ = client
	conn.Close(StatusNormalClosure, "done")
}

// TestBind_OpenFiresBeforeRead verifies that all "open" handlers complete
// before any inbound message is processed, eliminating the registration race.
func TestBind_OpenFiresBeforeRead(t *testing.T) {
	conn := newMockConn()

	// Queue an inbound event before Bind is called.
	conn.send(Message{
		Arguments: []json.RawMessage{[]byte(`"news"`), []byte(`"headline"`)},
	})

	var order []string
	var mu sync.Mutex
	record := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}

	done := make(chan struct{})
	client := NewClient()
	client.On("open", func(c *Client) {
		record("open")
		// Register the "news" handler inside the open callback — this is the
		// pattern that would race if open fired after readMessages started.
		c.On("news", func(headline string) error {
			record("news")
			close(done)
			return nil
		})
	})
	client.Bind(conn)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("news handler was not called")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "open" || order[1] != "news" {
		t.Fatalf("expected [open news], got %v", order)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestBind_Reconnect verifies that a client can reconnect: calling Bind again
// inside the "close" handler attaches a new connection, fires "open" again,
// and the client resumes processing messages on the new connection.
func TestBind_Reconnect(t *testing.T) {
	conn1 := newMockConn()
	conn2 := newMockConn()

	opens := make(chan struct{}, 2)
	reconnected := make(chan struct{})

	client := NewClient()
	client.On("open", func(c *Client) {
		opens <- struct{}{}
	})
	client.On("close", func(c *Client, status StatusCode, reason string) {
		select {
		case <-reconnected:
			// second close — don't rebind
		default:
			// first close — reconnect on conn2
			close(reconnected)
			c.Bind(conn2)
		}
	})

	client.Bind(conn1)

	// Wait for first open.
	select {
	case <-opens:
	case <-time.After(time.Second):
		t.Fatal("first open did not fire")
	}

	// Close conn1 to trigger reconnection.
	conn1.Close(StatusNormalClosure, "disconnected")

	// Wait for reconnection (second open).
	select {
	case <-opens:
	case <-time.After(2 * time.Second):
		t.Fatal("second open (reconnect) did not fire")
	}

	// Close conn2 to finish the test cleanly.
	conn2.Close(StatusNormalClosure, "done")
}

// TestBind_PendingRequestsAborted verifies that in-flight outbound requests
// are cancelled with an error when Bind is called with a new connection.
func TestBind_PendingRequestsAborted(t *testing.T) {
	conn1 := newMockConn()
	conn2 := newMockConn()

	client := NewClient()
	client.Bind(conn1)

	// Start a request on conn1 that will never receive a response.
	reqErr := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), "slow")
		reqErr <- err
	}()

	// Give the goroutine a moment to register the pending request.
	time.Sleep(20 * time.Millisecond)

	// Rebind to conn2 — this should abort the pending request.
	client.Bind(conn2)

	select {
	case err := <-reqErr:
		if err == nil {
			t.Fatal("expected request to be aborted with an error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for aborted request error")
	}

	conn2.Close(StatusNormalClosure, "done")
}

// TestNewClient_HandleInboundEvent verifies that a NewClient handles an
// inbound event from the remote server correctly.
func TestNewClient_HandleInboundEvent(t *testing.T) {
	called := make(chan string, 1)

	client := NewClient()
	client.On("greet", func(name string) error {
		called <- name
		return nil
	})

	conn := newMockConn()
	client.Bind(conn)

	conn.send(Message{
		Arguments: []json.RawMessage{[]byte(`"greet"`), []byte(`"world"`)},
	})

	select {
	case got := <-called:
		if got != "world" {
			t.Fatalf("expected 'world', got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler not called within timeout")
	}

	conn.Close(StatusNormalClosure, "done")
}

