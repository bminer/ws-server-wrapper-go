package wrapper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestCallHandler(t *testing.T) {
	ctx := context.Background()
	type Test struct {
		Name      string
		Handler   any
		Arguments []any
		Response  any
		Error     error
	}
	type TestStruct struct {
		StringSlice []string
		FloatSlice  []float64
	}
	tests := []Test{
		{
			Name: "Handler returning string",
			Handler: func(s string, i int, f float64) (string, error) {
				output := fmt.Sprintf("s: %s, i: %v, f: %v", s, i, f)
				return output, nil
			},
			Arguments: []any{"string", 42, 700.3},
			Response:  "s: string, i: 42, f: 700.3",
		},
		{
			Name: "Handler returning error",
			Handler: func(s string, i int, f float64) (string, error) {
				return "", fmt.Errorf("uh oh!")
			},
			Arguments: []any{"string", 42, 700.3},
			Error:     fmt.Errorf("uh oh!"),
		},
		{
			Name: "Handler returning struct",
			Handler: func(s string, i int, f float64) (TestStruct, error) {
				return TestStruct{
					StringSlice: []string{s},
					FloatSlice:  []float64{float64(i), f},
				}, nil
			},
			Arguments: []any{"string", 42, 700.3},
			Response: TestStruct{
				StringSlice: []string{"string"},
				FloatSlice:  []float64{42, 700.3},
			},
		},
		{
			Name: "Handler with context and slice arg",
			Handler: func(ctx context.Context, ints []int) (TestStruct, error) {
				// Convert ints to floats
				slice := make([]float64, len(ints))
				for i := range ints {
					slice[i] = float64(ints[i])
				}
				return TestStruct{
					StringSlice: []string{"hmm", "ok"},
					FloatSlice:  slice,
				}, nil
			},
			Arguments: []any{[]int{1, 2, 3, 4}},
			Response: TestStruct{
				StringSlice: []string{"hmm", "ok"},
				FloatSlice:  []float64{1, 2, 3, 4},
			},
		},
		{
			Name: "Handler with context and slice conversion",
			Handler: func(ctx context.Context, ints []int) (TestStruct, error) {
				// Convert ints to floats
				slice := make([]float64, len(ints))
				for i := range ints {
					slice[i] = float64(ints[i])
				}
				return TestStruct{
					StringSlice: []string{"hmm", "ok"},
					FloatSlice:  slice,
				}, nil
			},
			Arguments: []any{[]float64{1, 2, 3, 4}},
			Response: TestStruct{
				StringSlice: []string{"hmm", "ok"},
				FloatSlice:  []float64{1, 2, 3, 4},
			},
		},
		{
			Name:      "Non-function handler",
			Handler:   "not a function",
			Arguments: []any{},
			Error:     fmt.Errorf("handler is not a function"),
		},
		{
			Name: "Wrong number of arguments",
			Handler: func(s string, i int) (string, error) {
				return s, nil
			},
			Arguments: []any{"string"},
			Error:     fmt.Errorf("incorrect number of arguments"),
		},
		{
			Name: "Too many return values",
			Handler: func(s string) (string, string, error) {
				return s, s, nil
			},
			Arguments: []any{"string"},
			Error:     fmt.Errorf("handler should return one or two values"),
		},
		{
			Name: "Last return type does not implement error",
			Handler: func(s string) (string, string) {
				return s, s
			},
			Arguments: []any{"string"},
			Error:     fmt.Errorf("handler's last output type must implement error"),
		},
		{
			Name: "Handler returning only error (nil)",
			Handler: func(s string) error {
				return nil
			},
			Arguments: []any{"string"},
			Response:  nil,
		},
		{
			Name: "Handler returning only error (non-nil)",
			Handler: func(s string) error {
				return fmt.Errorf("just an error")
			},
			Arguments: []any{"string"},
			Error:     fmt.Errorf("just an error"),
		},
	}
	for _, ht := range tests {
		name := ht.Name
		jsonArgs := make([]json.RawMessage, len(ht.Arguments))
		for i, arg := range ht.Arguments {
			buf, err := json.Marshal(arg)
			if err != nil {
				t.Errorf(name+": JSON encoding error: %v", err)
				continue
			}
			jsonArgs[i] = buf
		}

		res, err := callHandler(ctx, ht.Handler, jsonArgs)
		if ht.Error != nil {
			if err == nil {
				t.Errorf(name+": expected error %v", ht.Error)
			} else if got, want := err.Error(), ht.Error.Error(); got != want {
				t.Errorf(name+": errors don't match; got %v, want %v",
					got, want)
			}
		} else if err != nil {
			t.Errorf(name+": unexpected error %v", err)
		} else if got, want := res, ht.Response; !reflect.DeepEqual(got, want) {
			t.Fatalf(name+":\n\tgot: %v\n\twant: %v", got, want)
		}
	}
	// handler :=
}

// TestIsReservedEvent verifies that reserved event names are correctly
// identified and non-reserved names are not.
func TestIsReservedEvent(t *testing.T) {
	reserved := []string{
		EventOpen, EventConnect, EventError, EventMessage, EventClose, EventDisconnect,
	}
	for _, name := range reserved {
		if !IsReservedEvent(name) {
			t.Errorf("expected %q to be a reserved event", name)
		}
	}
	for _, name := range []string{"custom", "echo", "ping", ""} {
		if IsReservedEvent(name) {
			t.Errorf("expected %q to not be a reserved event", name)
		}
	}
}

// TestCheckHandler verifies that checkHandler accepts valid handler signatures
// and rejects invalid ones.
func TestCheckHandler(t *testing.T) {
	// Valid: nil handler (removes the handler)
	var h any = nil
	if err := checkHandler("", "custom", h); err != nil {
		t.Errorf("unexpected error for nil handler: %v", err)
	}

	// Valid: normal event handler returning (result, error)
	h = func(s string) (string, error) { return s, nil }
	if err := checkHandler("", "custom", h); err != nil {
		t.Errorf("unexpected error for valid handler: %v", err)
	}

	// Valid: reserved "open" handler
	h = func(*Client) {}
	if err := checkHandler("", EventOpen, h); err != nil {
		t.Errorf("unexpected error for valid open handler: %v", err)
	}

	// Valid: reserved "error" handler
	h = func(*Client, error) {}
	if err := checkHandler("", EventError, h); err != nil {
		t.Errorf("unexpected error for valid error handler: %v", err)
	}

	// Valid: reserved "close" handler
	h = func(*Client, StatusCode, string, bool) {}
	if err := checkHandler("", EventClose, h); err != nil {
		t.Errorf("unexpected error for valid close handler: %v", err)
	}
	h = func(*Client, StatusCode, string) {}
	if err := checkHandler("", EventClose, h); err != nil {
		t.Errorf("unexpected error for valid close handler: %v", err)
	}

	// Invalid: reserved "open" handler with wrong signature
	h = func(s string) {}
	if err := checkHandler("", EventOpen, h); err == nil {
		t.Error("expected error for open handler with wrong signature")
	}

	// Invalid: non-function handler
	h = "not a function"
	if err := checkHandler("", "custom", h); err == nil {
		t.Error("expected error for non-function handler")
	}

	// Invalid: handler with no return values
	h = func() {}
	if err := checkHandler("", "custom", h); err == nil {
		t.Error("expected error for handler with no return values")
	}

	// Invalid: handler whose last return type does not implement error
	h = func() (string, string) { return "", "" }
	if err := checkHandler("", "custom", h); err == nil {
		t.Error("expected error for handler with wrong return type")
	}
}

func TestClientChannelCloseRemovesChannelHandlers(t *testing.T) {
	client := NewClient(nil)

	client.Of("room").On("a", func() error { return nil })
	client.Of("room").Once("b", func() error { return nil })
	client.Of("other").On("a", func() error { return nil })

	room := client.Of("room")
	if err := room.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if _, ok := client.handlers[handlerName{Channel: "room", Anonymous: false, Event: "a"}]; ok {
		t.Fatal("expected persistent room handler to be removed")
	}
	if _, ok := client.handlersOnce[handlerName{Channel: "room", Anonymous: false, Event: "b"}]; ok {
		t.Fatal("expected once room handler to be removed")
	}
	if _, ok := client.handlers[handlerName{Channel: "other", Anonymous: false, Event: "a"}]; !ok {
		t.Fatal("expected handlers for other channels to be preserved")
	}
}

func TestServerChannelCloseRemovesChannelHandlers(t *testing.T) {
	server := NewServer()

	server.Of("room").On("a", func() error { return nil })
	server.Of("room").Once("b", func() error { return nil })
	server.Of("other").On("a", func() error { return nil })

	room := server.Of("room")
	if err := room.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if _, ok := server.handlers[handlerName{Channel: "room", Anonymous: false, Event: "a"}]; ok {
		t.Fatal("expected persistent room handler to be removed")
	}
	if _, ok := server.handlersOnce[handlerName{Channel: "room", Anonymous: false, Event: "b"}]; ok {
		t.Fatal("expected once room handler to be removed")
	}
	if _, ok := server.handlers[handlerName{Channel: "other", Anonymous: false, Event: "a"}]; !ok {
		t.Fatal("expected handlers for other channels to be preserved")
	}
}

func TestClientChannelCloseMarksChannelClosed(t *testing.T) {
	client := NewClient(nil)
	ch := client.Of("room")
	if ch.client == nil {
		t.Fatal("expected open channel to reference a client")
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("unexpected close error on second close: %v", err)
	}
	ch.On("a", func() error { return nil })
	ch.Once("a", func() error { return nil })
	if err := ch.Emit(context.Background(), "a"); err == nil {
		t.Fatal("expected closed-channel error from Emit")
	} else {
		var cerr ChannelClosedError
		if !errors.As(err, &cerr) {
			t.Fatalf("expected ChannelClosedError, got %T", err)
		}
	}
	if _, err := ch.Request(context.Background(), "a"); err == nil {
		t.Fatal("expected closed-channel error from Request")
	} else {
		var cerr ChannelClosedError
		if !errors.As(err, &cerr) {
			t.Fatalf("expected ChannelClosedError, got %T", err)
		}
	}
}

func TestServerChannelCloseMarksChannelClosed(t *testing.T) {
	server := NewServer()
	ch := server.Of("room")
	if ch.server == nil {
		t.Fatal("expected open channel to reference a server")
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("unexpected close error on second close: %v", err)
	}
	ch.On("a", func() error { return nil })
	ch.Once("a", func() error { return nil })
	if errs := ch.Emit(context.Background(), "a"); len(errs) == 0 {
		t.Fatal("expected closed-channel error from Emit")
	} else {
		var cerr ChannelClosedError
		if !errors.As(errs[0], &cerr) {
			t.Fatalf("expected ChannelClosedError, got %T", errs[0])
		}
	}
}
