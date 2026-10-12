package wrapper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageEventName(t *testing.T) {
	tests := []struct {
		name     string
		msg      Message
		expected string
	}{
		{
			name:     "no arguments",
			msg:      Message{},
			expected: "",
		},
		{
			name:     "valid event name",
			msg:      Message{Arguments: []json.RawMessage{[]byte(`"click"`)}},
			expected: "click",
		},
		{
			name: "event name with additional arguments",
			msg: Message{
				Arguments: []json.RawMessage{[]byte(`"click"`), []byte(`42`)},
			},
			expected: "click",
		},
		{
			name:     "invalid JSON in first argument",
			msg:      Message{Arguments: []json.RawMessage{[]byte(`not-json`)}},
			expected: "",
		},
		{
			name:     "non-string first argument",
			msg:      Message{Arguments: []json.RawMessage{[]byte(`42`)}},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.msg.EventName()
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestMessageHandlerArguments(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		// if wantNil is true, HandlerArguments() should return nil
		wantNil bool
		// if wantNil is false, len(HandlerArguments()) should return wantLen
		wantLen int
	}{
		{
			name:    "no arguments",
			msg:     Message{},
			wantNil: true,
		},
		{
			name:    "only event name",
			msg:     Message{Arguments: []json.RawMessage{[]byte(`"click"`)}},
			wantNil: true,
		},
		{
			name: "event name and one argument",
			msg: Message{
				Arguments: []json.RawMessage{[]byte(`"click"`), []byte(`42`)},
			},
			wantLen: 1,
		},
		{
			name: "event name and two arguments",
			msg: Message{
				Arguments: []json.RawMessage{
					[]byte(`"click"`), []byte(`42`), []byte(`"hello"`),
				},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.msg.HandlerArguments()
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
			} else if len(got) != tt.wantLen {
				t.Errorf("expected %d args, got %d", tt.wantLen, len(got))
			}
		})
	}
}

func TestMessageResponse(t *testing.T) {
	reqID := 1

	t.Run("successful response", func(t *testing.T) {
		msg := Message{RequestID: &reqID, ResponseData: "result"}
		data, err := msg.Response()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if data != "result" {
			t.Errorf("expected 'result', got %v", data)
		}
	})

	t.Run("string error response", func(t *testing.T) {
		msg := Message{RequestID: &reqID, ResponseError: "something went wrong"}
		_, err := msg.Response()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "something went wrong" {
			t.Errorf("expected 'something went wrong', got %q", err.Error())
		}
	})

	t.Run("JavaScript error response", func(t *testing.T) {
		msg := Message{
			RequestID:       &reqID,
			ResponseError:   map[string]any{"message": "js error"},
			ResponseJSError: true,
		}
		_, err := msg.Response()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "js error" {
			t.Errorf("expected 'js error', got %q", err.Error())
		}
	})

	t.Run("missing request ID", func(t *testing.T) {
		msg := Message{}
		_, err := msg.Response()
		if err == nil {
			t.Error("expected error for message without request ID")
		}
	})

	t.Run("malformed JavaScript error (not an object)", func(t *testing.T) {
		msg := Message{
			RequestID:       &reqID,
			ResponseError:   "oops",
			ResponseJSError: true,
		}
		_, err := msg.Response()
		if err == nil {
			t.Error("expected error for malformed JS error")
		}
	})
}

func TestMessageCancelCause(t *testing.T) {
	reqID := 1

	t.Run("missing request ID", func(t *testing.T) {
		// CancelCause works for anonymous-channel aborts too (no RequestID).
		msg := Message{CancelReason: "user cancelled"}
		err := msg.CancelCause()
		if err == nil || err.Error() != "user cancelled" {
			t.Errorf("expected 'user cancelled', got %v", err)
		}
	})

	t.Run("missing cancel reason", func(t *testing.T) {
		msg := Message{RequestID: &reqID}
		err := msg.CancelCause()
		if err == nil || err.Error() != "message is not a cancellation" {
			t.Errorf("expected 'message is not a cancellation', got %v", err)
		}
	})

	t.Run("string cancel reason", func(t *testing.T) {
		msg := Message{RequestID: &reqID, CancelReason: "user cancelled"}
		err := msg.CancelCause()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "user cancelled" {
			t.Errorf("expected 'user cancelled', got %q", err.Error())
		}
	})

	t.Run("cancel reason of unusual type returns context.Canceled", func(t *testing.T) {
		msg := Message{RequestID: &reqID, CancelReason: 23}
		err := msg.CancelCause()
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}

		msg = Message{RequestID: &reqID, CancelReason: false}
		err = msg.CancelCause()
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("JavaScript error cancel reason", func(t *testing.T) {
		msg := Message{
			RequestID:       &reqID,
			CancelReason:    map[string]any{"message": "js cancel"},
			ResponseJSError: true,
		}
		err := msg.CancelCause()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "js cancel" {
			t.Errorf("expected 'js cancel', got %q", err.Error())
		}
	})

	t.Run("malformed JavaScript error", func(t *testing.T) {
		msg := Message{
			RequestID:       &reqID,
			CancelReason:    "not an object",
			ResponseJSError: true,
		}
		err := msg.CancelCause()
		if err == nil {
			t.Error("expected error for malformed JS cancel reason")
		}
	})
}

func TestResponseJSErrorUnmarshal(t *testing.T) {
	// When "_" is 1 it should parse as true
	var m Message
	enc := []byte(`{"i":1,"e":{"message":"js err"},"_":1}`)
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !m.ResponseJSError {
		t.Error("expected ResponseJSError true for _=1")
	}

	// When "_" is 0 it should parse as false
	m = Message{}
	enc = []byte(`{"i":1,"d":"ok","_":0}`)
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m.ResponseJSError {
		t.Error("expected ResponseJSError false for _=0")
	}

	// When "_" is boolean true/false it also works
	m = Message{}
	enc = []byte(`{"i":1,"e":{"message":"js err"},"_":true}`)
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !m.ResponseJSError {
		t.Error("expected ResponseJSError true for _=true")
	}

	m = Message{}
	enc = []byte(`{"i":1,"e":"some error string","_":false}`)
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m.ResponseJSError {
		t.Error("expected ResponseJSError false for _=false")
	}
	res, err := m.Response()
	if res != nil {
		t.Error("expected no response data")
	}
	if exp := "some error string"; err == nil || err.Error() != exp {
		t.Errorf("expected error '%v', got '%v'", exp, err)
	}

	// When "_" is falsy but `e` is an object we cannot parse
	m = Message{}
	enc = []byte(`{"i":1,"e":{"message":"js err"},"d":"poop"}`)
	if err := json.Unmarshal(enc, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m.ResponseJSError {
		t.Error("expected ResponseJSError to be false")
	}
	res, err = m.Response()
	// We ignore `d`; still interpret as an error
	if res != nil {
		t.Error("expected no response data")
	}
	if exp := "response error is not a string"; err == nil || err.Error() != exp {
		t.Errorf("expected error '%v', got '%v'", exp, err)
	}
}

func TestMessageLogValue(t *testing.T) {
	reqID := 3
	ignore := weakBool(false)
	event := func(args ...string) []json.RawMessage {
		raw := make([]json.RawMessage, len(args))
		for i, a := range args {
			raw[i] = json.RawMessage(a)
		}
		return raw
	}

	tests := []struct {
		name string
		msg  Message
		want map[string]string
	}{
		{
			name: "ignored",
			msg:  Message{IgnoreIfFalse: &ignore, Arguments: event(`"ping"`)},
			want: map[string]string{"ignored": "true"},
		},
		{
			name: "event on named channel",
			msg:  Message{Channel: "room", Arguments: event(`"ping"`, `1`)},
			want: map[string]string{"ch": "room", "event": "ping", "args[0]": "1"},
		},
		{
			name: "request on anonymous channel",
			msg:  Message{AnonymousChannel: 2, RequestID: &reqID, Arguments: event(`"ask"`)},
			want: map[string]string{"anonCh": "2", "event": "ask", "reqID": "3"},
		},
		{
			name: "anonymous channel abort",
			msg:  Message{AnonymousChannel: 2, CancelReason: "stop"},
			want: map[string]string{"anonCh": "2", "cancelReason": "stop", "jsError": "false"},
		},
		{
			name: "anonymous channel created",
			msg:  Message{AnonymousChannel: 1, RequestID: &reqID},
			want: map[string]string{"reqID": "3", "anonChCreated": "true"},
		},
		{
			name: "request cancellation",
			msg:  Message{RequestID: &reqID, CancelReason: "stop", ResponseJSError: true},
			want: map[string]string{"reqID": "3", "cancelReason": "stop", "jsError": "true"},
		},
		{
			name: "rejection",
			msg:  Message{RequestID: &reqID, ResponseError: "nope"},
			want: map[string]string{"reqID": "3", "error": "nope", "jsError": "false"},
		},
		{
			name: "resolution",
			msg:  Message{RequestID: &reqID, ResponseData: "ok"},
			want: map[string]string{"reqID": "3", "data": "ok"},
		},
		{
			name: "invalid",
			msg:  Message{},
			want: map[string]string{"invalid": "true"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			for _, attr := range tc.msg.LogValue().Group() {
				got[attr.Key] = attr.Value.String()
			}
			if len(got) != len(tc.want) {
				t.Fatalf("expected attrs %v, got %v", tc.want, got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("attr %q: expected %q, got %q", k, v, got[k])
				}
			}
		})
	}

	t.Run("long argument is truncated", func(t *testing.T) {
		long := `"` + strings.Repeat("x", 2000) + `"`
		msg := Message{Arguments: event(`"ping"`, long)}
		for _, attr := range msg.LogValue().Group() {
			if attr.Key != "args[0]" {
				continue
			}
			s := attr.Value.String()
			if len(s) != 1024 || !strings.HasSuffix(s, "...(truncated)") {
				t.Fatalf("expected 1024-byte truncated value, got %d bytes ending %q",
					len(s), s[len(s)-20:])
			}
			return
		}
		t.Fatal("args[0] attribute not found")
	})
}
