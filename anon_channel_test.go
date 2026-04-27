package wrapper

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// ── anonChannelH type tests ───────────────────────────────────────────────────

// TestAnonChannelH_UnmarshalString verifies that a JSON string is decoded as-is.
func TestAnonChannelH_UnmarshalString(t *testing.T) {
	var h anonChannelH
	if err := json.Unmarshal([]byte(`"42"`), &h); err != nil {
		t.Fatal(err)
	}
	if h != "42" {
		t.Fatalf("expected \"42\", got %q", h)
	}
}

// TestAnonChannelH_UnmarshalNumber verifies that a truthy JSON number maps to "1".
func TestAnonChannelH_UnmarshalNumber(t *testing.T) {
	var h anonChannelH
	if err := json.Unmarshal([]byte(`1`), &h); err != nil {
		t.Fatal(err)
	}
	if h != "1" {
		t.Fatalf("expected \"1\", got %q", h)
	}
}

// TestAnonChannelH_UnmarshalZero verifies that JSON 0 maps to "".
func TestAnonChannelH_UnmarshalZero(t *testing.T) {
	var h anonChannelH
	if err := json.Unmarshal([]byte(`0`), &h); err != nil {
		t.Fatal(err)
	}
	if h != "" {
		t.Fatalf("expected empty string, got %q", h)
	}
}

// ── Channel(ctx) factory tests ────────────────────────────────────────────────

// TestChannelOutsideHandler verifies that Channel(ctx) returns nil when called
// outside a request handler (no factory in context).
func TestChannelOutsideHandler(t *testing.T) {
	if ch := Channel(context.Background()); ch != nil {
		t.Fatalf("expected nil outside handler, got %v", ch)
	}
}

// TestChannelLazySingleton verifies that Channel(ctx) returns the same
// *AnonymousChannel on repeated calls within the same handler context.
func TestChannelLazySingleton(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var ch1, ch2 *AnonymousChannel
	created := make(chan struct{})
	server.On("getChannel", func(ctx context.Context) (*AnonymousChannel, error) {
		ch1 = Channel(ctx)
		ch2 = Channel(ctx) // second call — must return the same pointer
		close(created)
		return ch1, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"getChannel"`)},
	})

	select {
	case <-created:
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}

	// Consume the creation response before checking
	conn.waitWritten(t, time.Second)

	if ch1 == nil {
		t.Fatal("Channel(ctx) returned nil inside handler")
	}
	if ch1 != ch2 {
		t.Fatal("Channel(ctx) returned different pointers on repeated calls")
	}

	conn.Close(StatusNormalClosure, "done")
}

// ── Go-as-handler: returning *AnonymousChannel ────────────────────────────────

// TestHandlerReturnsAnonChannel verifies that when a handler returns
// *AnonymousChannel, the server sends a creation response {i, h}.
func TestHandlerReturnsAnonChannel(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		return Channel(ctx), nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != reqID {
		t.Fatalf("expected creation response for request %d, got %+v", reqID, resp)
	}
	if resp.AnonymousChannel == "" {
		t.Fatalf("expected non-empty h field, got %+v", resp)
	}
	if resp.ResponseData != nil || resp.ResponseError != nil {
		t.Fatalf("creation response must not have d/e fields, got %+v", resp)
	}
	// Channel ID must equal strconv.Itoa(reqID)
	expectedChanID := "1"
	if string(resp.AnonymousChannel) != expectedChanID {
		t.Fatalf("expected h=%q, got h=%q", expectedChanID, resp.AnonymousChannel)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestHandlerReturnsNilDoesNotSendCreation verifies that returning nil (or a
// non-matching *AnonymousChannel) sends a normal response, not a creation response.
func TestHandlerReturnsNilDoesNotSendCreation(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("noChannel", func(ctx context.Context) (*AnonymousChannel, error) {
		// Deliberately ignore Channel(ctx) — return nil
		return nil, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"noChannel"`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != reqID {
		t.Fatalf("expected response for request %d, got %+v", reqID, resp)
	}
	// Must be a regular resolve (d: null), not a creation response (h field)
	if resp.AnonymousChannel != "" {
		t.Fatalf("expected no h field, got %+v", resp)
	}

	conn.Close(StatusNormalClosure, "done")
}

// ── Routing events/requests to anonymous channel handlers ────────────────────

// TestAnonChannelEventRouted verifies that inbound {h, a} messages are routed
// to handlers registered on the anonymous channel.
func TestAnonChannelEventRouted(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	called := make(chan string, 1)
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		ch := Channel(ctx)
		ch.On("data", func(v string) (string, error) {
			called <- v
			return "", nil
		})
		return ch, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	// Request that triggers creation
	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response

	// Send an event on the anonymous channel
	conn.send(Message{
		AnonymousChannel: "1",
		Arguments:        []json.RawMessage{[]byte(`"data"`), []byte(`"hello"`)},
	})

	select {
	case v := <-called:
		if v != "hello" {
			t.Fatalf("expected 'hello', got %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("anon channel event handler was not called")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelRequestRouted verifies that inbound {i, h, a} sub-requests
// are routed to handlers and a response is sent back.
func TestAnonChannelRequestRouted(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		ch := Channel(ctx)
		ch.On("compute", func(x int) (int, error) {
			return x * 2, nil
		})
		return ch, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response

	// Send a sub-request on the anonymous channel
	subReqID := 2
	conn.send(Message{
		RequestID:        &subReqID,
		AnonymousChannel: "1",
		Arguments:        []json.RawMessage{[]byte(`"compute"`), []byte(`21`)},
	})

	resp := conn.waitWritten(t, time.Second)
	if resp.RequestID == nil || *resp.RequestID != subReqID {
		t.Fatalf("expected response for sub-request %d, got %+v", subReqID, resp)
	}
	if resp.ResponseData != 42 {
		t.Fatalf("expected 42, got %v", resp.ResponseData)
	}
	// Response must NOT have h field
	if resp.AnonymousChannel != "" {
		t.Fatalf("sub-request response must not have h field, got %+v", resp)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelInboundAbort verifies that an inbound {h, x} abort message
// closes the local anonymous channel and cancels its context.
func TestAnonChannelInboundAbort(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	ctxDone := make(chan error, 1)
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		ch := Channel(ctx)
		go func() {
			<-ch.Context().Done()
			ctxDone <- context.Cause(ch.Context())
		}()
		return ch, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response

	// Remote sends abort
	conn.send(Message{
		AnonymousChannel: "1",
		ResponseJSError:  true,
		CancelReason: map[string]any{
			"message": "remote aborted",
		},
	})

	select {
	case cause := <-ctxDone:
		if cause == nil || cause.Error() != "remote aborted" {
			t.Fatalf("expected 'remote aborted', got %v", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("channel context was not cancelled after inbound abort")
	}

	conn.Close(StatusNormalClosure, "done")
}

// ── AnonymousChannel.Abort() and Close() ─────────────────────────────────────

// TestAnonChannelAbort verifies that Abort sends {h, x} and cancels the context.
func TestAnonChannelAbort(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response

	<-ready

	abortErr := errors.New("server aborted")
	if err := capturedCh.Abort(abortErr); err != nil {
		t.Fatalf("Abort returned error: %v", err)
	}

	// Expect abort message
	abort := conn.waitWritten(t, time.Second)
	if abort.AnonymousChannel != "1" {
		t.Fatalf("expected h='1', got %+v", abort)
	}
	if !abort.ResponseJSError {
		t.Fatal("expected abort encoded as JS error")
	}
	expected := map[string]any{"message": "server aborted"}
	if !reflect.DeepEqual(expected, abort.CancelReason) {
		t.Fatalf("expected cancel reason %v, got %v", expected, abort.CancelReason)
	}
	if abort.RequestID != nil {
		t.Fatalf("abort must not have i field, got %+v", abort)
	}

	// Context must be cancelled
	select {
	case <-capturedCh.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("channel context was not cancelled after Abort")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelClose verifies that Close cancels the context without sending
// any abort message to the remote end.
func TestAnonChannelClose(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response

	<-ready

	if err := capturedCh.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// No abort message should be sent
	select {
	case msg := <-conn.writeCh:
		t.Fatalf("unexpected message after Close: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}

	// Context must be cancelled
	select {
	case <-capturedCh.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("channel context was not cancelled after Close")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelCloseIdempotent verifies that calling Close multiple times is
// safe and returns nil on subsequent calls.
func TestAnonChannelCloseIdempotent(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second)
	<-ready

	if err := capturedCh.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := capturedCh.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelEmitAfterClose verifies that Emit on a closed channel returns
// ChannelClosedError.
func TestAnonChannelEmitAfterClose(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second)
	<-ready

	capturedCh.Close()

	err := capturedCh.Emit(context.Background(), "event")
	if err == nil {
		t.Fatal("expected ChannelClosedError, got nil")
	}
	var closedErr ChannelClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ChannelClosedError, got %T: %v", err, err)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestAnonChannelEmitSendsCorrectMessage verifies that Emit on an open
// anonymous channel sends {h, a} (no c field, no i field).
func TestAnonChannelEmitSendsCorrectMessage(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second) // consume creation response
	<-ready

	if err := capturedCh.Emit(context.Background(), "tick", 42); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	msg := conn.waitWritten(t, time.Second)
	if msg.Channel != "" {
		t.Fatalf("expected no c field, got %+v", msg)
	}
	if msg.AnonymousChannel != "1" {
		t.Fatalf("expected h='1', got %+v", msg)
	}
	if msg.RequestID != nil {
		t.Fatalf("event must not have i field, got %+v", msg)
	}
	if len(msg.Arguments) < 2 {
		t.Fatalf("expected at least 2 arguments (event+payload), got %+v", msg)
	}

	conn.Close(StatusNormalClosure, "done")
}

// ── Connection close cleans up anonymous channels ────────────────────────────

// TestConnectionCloseAbortsAnonChannels verifies that closing the connection
// cancels all open anonymous channel contexts.
func TestConnectionCloseAbortsAnonChannels(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	ctxDone := make(chan struct{}, 1)
	var capturedCh *AnonymousChannel
	ready := make(chan struct{})
	server.On("stream", func(ctx context.Context) (*AnonymousChannel, error) {
		capturedCh = Channel(ctx)
		close(ready)
		return capturedCh, nil
	})

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	reqID := 1
	conn.send(Message{
		RequestID: &reqID,
		Arguments: []json.RawMessage{[]byte(`"stream"`)},
	})
	conn.waitWritten(t, time.Second)
	<-ready

	go func() {
		<-capturedCh.Context().Done()
		ctxDone <- struct{}{}
	}()

	conn.Close(StatusNormalClosure, "done")

	select {
	case <-ctxDone:
	case <-time.After(time.Second):
		t.Fatal("anon channel context not cancelled after connection close")
	}
}

// ── Unknown anonymous channel traffic ────────────────────────────────────────

// TestUnknownAnonChannelEventSendsCancel verifies that traffic on an unknown
// anonymous channel triggers a sendAnonCancel message.
func TestUnknownAnonChannelEventSendsCancel(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	// Send event to a channel ID that was never created
	conn.send(Message{
		AnonymousChannel: "99",
		Arguments:        []json.RawMessage{[]byte(`"data"`), []byte(`"hello"`)},
	})

	msg := conn.waitWritten(t, time.Second)
	if msg.AnonymousChannel != "99" {
		t.Fatalf("expected h='99', got %+v", msg)
	}
	if msg.CancelReason == nil {
		t.Fatalf("expected cancel reason, got %+v", msg)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestUnknownAnonChannelRequestSendsCancelAndReject verifies that a sub-request
// on an unknown anonymous channel triggers both a cancel AND a reject response.
func TestUnknownAnonChannelRequestSendsCancelAndReject(t *testing.T) {
	server := NewServer()
	conn := newMockConn()

	if err := server.Accept(conn); err != nil {
		t.Fatal(err)
	}

	subReqID := 5
	conn.send(Message{
		RequestID:        &subReqID,
		AnonymousChannel: "99",
		Arguments:        []json.RawMessage{[]byte(`"compute"`)},
	})

	// Expect two outbound messages: cancel + reject (order may vary)
	msg1 := conn.waitWritten(t, time.Second)
	msg2 := conn.waitWritten(t, time.Second)

	// Identify cancel vs reject
	var cancelMsg, rejectMsg Message
	if msg1.AnonymousChannel != "" {
		cancelMsg, rejectMsg = msg1, msg2
	} else {
		cancelMsg, rejectMsg = msg2, msg1
	}

	if cancelMsg.AnonymousChannel != "99" {
		t.Fatalf("expected cancel with h='99', got %+v", cancelMsg)
	}
	if rejectMsg.RequestID == nil || *rejectMsg.RequestID != subReqID {
		t.Fatalf("expected reject for sub-request %d, got %+v", subReqID, rejectMsg)
	}
	if rejectMsg.ResponseError == nil {
		t.Fatalf("expected reject with error, got %+v", rejectMsg)
	}

	conn.Close(StatusNormalClosure, "done")
}

// ── Go-as-requestor: receiving a creation response ────────────────────────────

// TestGoAsRequestorReceivesAnonChannel verifies that when Go sends a request
// and the remote responds with {i, h: 1}, sendRequest returns *AnonymousChannel.
func TestGoAsRequestorReceivesAnonChannel(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	result := make(chan any, 1)
	go func() {
		resp, err := client.Request(context.Background(), "streamData")
		if err != nil {
			result <- err
			return
		}
		result <- resp
	}()

	// Wait for the outbound request
	req := conn.waitWritten(t, time.Second)
	if req.RequestID == nil {
		t.Fatalf("expected request with ID, got %+v", req)
	}

	// JS responds with {i: reqID, h: 1}
	conn.send(Message{
		RequestID:        req.RequestID,
		AnonymousChannel: "1", // custom unmarshaller maps JS's h:1 to "1"
	})

	select {
	case v := <-result:
		if err, ok := v.(error); ok {
			t.Fatalf("expected *AnonymousChannel, got error: %v", err)
		}
		anon, ok := v.(*AnonymousChannel)
		if !ok {
			t.Fatalf("expected *AnonymousChannel, got %T", v)
		}
		if anon == nil {
			t.Fatal("expected non-nil *AnonymousChannel")
		}
		// Channel ID should be the string form of the request ID
		if anon.Name() != "1" {
			t.Fatalf("expected Name()='1', got %q", anon.Name())
		}
		_ = anon
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for anonymous channel result")
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestGoAsRequestorEmitsOnAnonChannel verifies that after receiving a creation
// response, Go can send events on the anonymous channel.
func TestGoAsRequestorEmitsOnAnonChannel(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	result := make(chan *AnonymousChannel, 1)
	go func() {
		resp, err := client.Request(context.Background(), "stream")
		if err != nil {
			return
		}
		anon, _ := resp.(*AnonymousChannel)
		result <- anon
	}()

	// Receive outbound request and respond with creation response
	req := conn.waitWritten(t, time.Second)
	conn.send(Message{
		RequestID:        req.RequestID,
		AnonymousChannel: "1",
	})

	var anon *AnonymousChannel
	select {
	case anon = <-result:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for anonymous channel")
	}

	// Now emit an event on the channel
	if err := anon.Emit(context.Background(), "data", "payload"); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	emitted := conn.waitWritten(t, time.Second)
	if emitted.AnonymousChannel != anonChannelH(anon.Name()) {
		t.Fatalf("expected h=%q, got %+v", anon.Name(), emitted)
	}
	if emitted.Channel != "" {
		t.Fatalf("event must not have c field, got %+v", emitted)
	}

	conn.Close(StatusNormalClosure, "done")
}

// TestGoAsRequestorReceivesAnonChannelEvent verifies that when the remote sends
// an event on the anonymous channel, the registered handler is called.
func TestGoAsRequestorReceivesAnonChannelEvent(t *testing.T) {
	client := NewClient(nil)
	conn := newMockConn()
	client.Bind(conn)

	called := make(chan string, 1)
	result := make(chan *AnonymousChannel, 1)
	go func() {
		resp, err := client.Request(context.Background(), "stream")
		if err != nil {
			return
		}
		anon, _ := resp.(*AnonymousChannel)
		if anon == nil {
			return
		}
		anon.On("data", func(v string) (string, error) {
			called <- v
			return "", nil
		})
		result <- anon
	}()

	req := conn.waitWritten(t, time.Second)
	conn.send(Message{
		RequestID:        req.RequestID,
		AnonymousChannel: "1",
	})

	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	// Remote sends event on the channel
	conn.send(Message{
		AnonymousChannel: anonChannelH(anon_name(req.RequestID)),
		Arguments:        []json.RawMessage{[]byte(`"data"`), []byte(`"world"`)},
	})

	select {
	case v := <-called:
		if v != "world" {
			t.Fatalf("expected 'world', got %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("event handler not called")
	}

	conn.Close(StatusNormalClosure, "done")
}

// anon_name returns the expected channel ID for a given request ID pointer.
func anon_name(id *int) string {
	if id == nil {
		return ""
	}
	return string(rune('0' + *id)) // works for single-digit IDs in tests
}
