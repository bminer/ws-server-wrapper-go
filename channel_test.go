package wrapper

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// failingConn is a mockConn whose writes always fail with err.
type failingConn struct {
	*mockConn
	err error
}

func (f *failingConn) WriteMessage(ctx context.Context, msg *Message) error {
	return f.err
}

// TestServerChannelEmitBroadcasts verifies that ServerChannel.Emit sends the
// event to every connected client on the named channel.
func TestServerChannelEmitBroadcasts(t *testing.T) {
	server := NewServer()
	conns := []*mockConn{newMockConn(), newMockConn()}
	for _, conn := range conns {
		if err := server.Accept(conn); err != nil {
			t.Fatal(err)
		}
	}

	if err := server.Of("chat").Emit(context.Background(), "msg", "hi"); err != nil {
		t.Fatalf("unexpected Emit error: %v", err)
	}

	for i, conn := range conns {
		msg := conn.waitWritten(t, time.Second)
		if msg.Channel != "chat" || msg.EventName() != "msg" {
			t.Fatalf("client %d: expected chat/msg, got %+v", i, msg)
		}
		args := msg.HandlerArguments()
		if len(args) != 1 || string(args[0]) != `"hi"` {
			t.Fatalf("client %d: expected args [\"hi\"], got %s", i, args)
		}
	}

	server.Close()
}

// TestServerChannelEmitMainChannel verifies that emitting on the server's
// main channel sends a message with no channel name.
func TestServerChannelEmitMainChannel(t *testing.T) {
	server := NewServer()
	conn := newMockConn()
	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	if err := server.Emit(context.Background(), "news"); err != nil {
		t.Fatalf("unexpected Emit error: %v", err)
	}
	msg := conn.waitWritten(t, time.Second)
	if msg.Channel != "" || msg.EventName() != "news" {
		t.Fatalf("expected main channel 'news' event, got %+v", msg)
	}

	server.Close()
}

// TestServerChannelEmitNoClients verifies that Emit succeeds when no clients
// are connected.
func TestServerChannelEmitNoClients(t *testing.T) {
	server := NewServer()
	if err := server.Of("chat").Emit(context.Background(), "msg"); err != nil {
		t.Fatalf("expected nil error with no clients, got %v", err)
	}
}

// TestServerChannelEmitValidation verifies that ServerChannel.Emit rejects
// invalid event names and reserved events on the main channel.
func TestServerChannelEmitValidation(t *testing.T) {
	server := NewServer()
	ctx := context.Background()

	if err := server.Emit(ctx); err == nil {
		t.Error("expected error when event name is missing")
	}
	if err := server.Emit(ctx, 42); err == nil {
		t.Error("expected error when event name is not a string")
	}
	if err := server.Emit(ctx, "open"); err == nil {
		t.Error("expected error when emitting reserved event on main channel")
	}
	if err := server.Of("room").Emit(ctx, "open"); err != nil {
		t.Errorf("reserved event names are allowed on named channels, got %v", err)
	}
}

// TestServerChannelEmitClientError verifies that a failed write to one client
// is returned as a ClientError while other clients still receive the event.
func TestServerChannelEmitClientError(t *testing.T) {
	server := NewServer()
	writeErr := errors.New("write failed")
	bad := &failingConn{mockConn: newMockConn(), err: writeErr}
	good := newMockConn()
	if err := server.Accept(bad); err != nil {
		t.Fatal(err)
	}
	if err := server.Accept(good); err != nil {
		t.Fatal(err)
	}

	err := server.Emit(context.Background(), "news")
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected joined error to wrap write error, got %v", err)
	}
	var clientErr ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if clientErr.Client == nil {
		t.Fatal("expected ClientError to reference the failing client")
	}
	if clientErr.Error() != writeErr.Error() {
		t.Fatalf("expected %q, got %q", writeErr.Error(), clientErr.Error())
	}

	if msg := good.waitWritten(t, time.Second); msg.EventName() != "news" {
		t.Fatalf("expected healthy client to receive 'news', got %+v", msg)
	}

	server.Close()
}

// TestClientChannelEmit verifies that ClientChannel.Emit sends the event on
// the named channel.
func TestClientChannelEmit(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	if err := client.Of("room").Emit(context.Background(), "hello", 1); err != nil {
		t.Fatalf("unexpected Emit error: %v", err)
	}
	msg := conn.waitWritten(t, time.Second)
	if msg.Channel != "room" || msg.EventName() != "hello" {
		t.Fatalf("expected room/hello, got %+v", msg)
	}
	if args := msg.HandlerArguments(); len(args) != 1 || string(args[0]) != "1" {
		t.Fatalf("expected args [1], got %s", args)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestClientChannelValidation verifies that ClientChannel.Emit and Request
// reject invalid event names and reserved events on the main channel.
func TestClientChannelValidation(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)
	ctx := context.Background()

	if err := client.Emit(ctx); err == nil {
		t.Error("Emit: expected error when event name is missing")
	}
	if err := client.Emit(ctx, 42); err == nil {
		t.Error("Emit: expected error when event name is not a string")
	}
	if err := client.Emit(ctx, "close"); err == nil {
		t.Error("Emit: expected error for reserved event on main channel")
	}
	if _, err := client.Request(ctx); err == nil {
		t.Error("Request: expected error when event name is missing")
	}
	if _, err := client.Request(ctx, "open"); err == nil {
		t.Error("Request: expected error for reserved event on main channel")
	}

	// Reserved event names are allowed on named channels.
	if err := client.Of("room").Emit(ctx, "close"); err != nil {
		t.Errorf("Emit: expected reserved name on named channel to succeed, got %v", err)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestClientChannelEmitAfterDisconnect verifies that Emit returns an error
// once the client's connection has been closed.
func TestClientChannelEmitAfterDisconnect(t *testing.T) {
	client := NewClient(nil)
	client.Bind(newMockConn())
	if err := client.Close(StatusNormalClosure, "bye"); err != nil {
		t.Fatal(err)
	}

	if err := client.Of("room").Emit(context.Background(), "hello"); !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed, got %v", err)
	}
}

// TestClientChannelRequest verifies a request/response round trip on a named
// client channel.
func TestClientChannelRequest(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	type result struct {
		data any
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := client.Of("room").Request(context.Background(), "ask", "q")
		done <- result{data, err}
	}()

	req := conn.waitWritten(t, time.Second)
	if req.Channel != "room" || req.EventName() != "ask" || req.RequestID == nil {
		t.Fatalf("expected room/ask request, got %+v", req)
	}
	conn.send(Message{RequestID: req.RequestID, ResponseData: "answer"})

	select {
	case res := <-done:
		if res.err != nil || res.data != "answer" {
			t.Fatalf("expected 'answer', got %v (err %v)", res.data, res.err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestClientChannelRequestRejected verifies that an error response from the
// remote end is returned from Request.
func TestClientChannelRequestRejected(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	done := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), "ask")
		done <- err
	}()

	req := conn.waitWritten(t, time.Second)
	conn.send(Message{RequestID: req.RequestID, ResponseError: "nope"})

	select {
	case err := <-done:
		if err == nil || err.Error() != "nope" {
			t.Fatalf("expected 'nope' error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestChannelName verifies Name for named and main channels.
func TestChannelName(t *testing.T) {
	server := NewServer()
	client := NewClient(nil)

	if got := server.Of("a").Name(); got != "a" {
		t.Errorf("ServerChannel.Name: expected 'a', got %q", got)
	}
	if got := server.Name(); got != "" {
		t.Errorf("main ServerChannel.Name: expected '', got %q", got)
	}
	if got := client.Of("b").Name(); got != "b" {
		t.Errorf("ClientChannel.Name: expected 'b', got %q", got)
	}
	if got := client.Name(); got != "" {
		t.Errorf("main ClientChannel.Name: expected '', got %q", got)
	}
}

// TestClientIgnoresUnmatchedMessages verifies that messages which do not
// match any pending request, cancellable handler, or anonymous channel are
// ignored without writing anything or disrupting the connection.
func TestClientIgnoresUnmatchedMessages(t *testing.T) {
	server := NewServer()
	conn := newMockConn()
	server.On("echo", func(s string) (string, error) { return s, nil })
	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	unknownID := 99
	conn.send(Message{}) // no event and no request ID
	conn.send(Message{RequestID: &unknownID, ResponseData: "x"})
	conn.send(Message{RequestID: &unknownID, CancelReason: "stop"})
	conn.send(Message{RequestID: &unknownID, AnonymousChannel: 1})
	conn.send(Message{AnonymousChannel: 5, CancelReason: "stop"})

	// The next written message must be the echo response; nothing above
	// should have produced output.
	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"echo"`), []byte(`"still here"`)},
	})
	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != reqID || resp.ResponseData != "still here" {
		t.Fatalf("expected echo response, got %+v", resp)
	}

	conn.Close(StatusNormalClosure, "done")
}
