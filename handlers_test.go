package wrapper

import (
	"context"
	"encoding/json"
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
				for i := 0; i < len(ints); i++ {
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
				for i := 0; i < len(ints); i++ {
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
