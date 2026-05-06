package wrapper

import (
	"context"
	"encoding/json"
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
