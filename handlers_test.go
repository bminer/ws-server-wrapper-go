package wrapper

import (
	"context"
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
			Arguments: []any{[]float64{1.2, 2.7, 3, 4}},
			Response: TestStruct{
				StringSlice: []string{"hmm", "ok"},
				FloatSlice:  []float64{1, 2, 3, 4},
			},
		},
	}
	for _, ht := range tests {
		name := ht.Name
		res, err := callHandler(ctx, ht.Handler, ht.Arguments)
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
