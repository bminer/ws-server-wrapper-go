package wrapper

import (
	"context"
	"errors"
	"reflect"
)

// handlerName is a unique name for a handler function having the specified
// channel and event name.
type handlerName struct {
	Channel string
	Event   string
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()
var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

// Calls the handler function with the given arguments. Passes the Context to
// the handler if the first argument is a Context. The handler function must
// return two values: the result and an error. This function returns the result
// and error from the handler function.
func callHandler(
	ctx context.Context, handler any, arguments []any,
) (res any, err error) {
	handlerV := reflect.ValueOf(handler)
	handlerT := handlerV.Type()

	// Ensure handler is a function with the correct number of parameters
	if handlerT.Kind() != reflect.Func {
		return nil, errors.New("handler is not a function")
	}

	// Allow for optional Context as the first argument
	hasContext := handlerT.NumIn() > 0 && handlerT.In(0) == contextType
	argOffset := 0
	if hasContext {
		argOffset = 1
	}
	if len(arguments) != handlerT.NumIn()-argOffset {
		return nil, errors.New("incorrect number of arguments")
	}

	// Ensure handler has the correct return values
	if handlerT.NumOut() != 2 {
		return nil, errors.New("handler should return two values")
	}
	if !handlerT.Out(1).Implements(errorType) {
		return nil, errors.New("handler's second return value must be an error")
	}

	// Convert arguments to a slice of reflect.Value
	ins := make([]reflect.Value, handlerT.NumIn())
	if hasContext {
		ins[0] = reflect.ValueOf(ctx)
	}
	for i := 0; i < len(arguments); i++ {
		argV := reflect.ValueOf(arguments[i]) // argument reflect.Value
		argT := argV.Type()
		handlerArgT := handlerT.In(argOffset + i) // function parameter type
		if argT.ConvertibleTo(handlerArgT) {
			// Convert arguments to the handler's argument types
			ins[argOffset+i] = argV.Convert(handlerArgT)
		} else if ka, kp := argT.Kind(), handlerArgT.Kind(); ka == reflect.Slice &&
			kp == reflect.Slice &&
			argT.Elem().ConvertibleTo(handlerArgT.Elem()) {
			// If argV amd handlerArgT are slices, we can try to convert each
			// element
			ins[argOffset+i] = reflect.MakeSlice(
				handlerArgT, argV.Len(), argV.Len(),
			)
			for j := 0; j < argV.Len(); j++ {
				ins[argOffset+i].Index(j).Set(argV.Index(j).Convert(handlerArgT.Elem()))
			}
		} else {
			return nil, errors.New("argument type mismatch")
		}
	}

	// Call the handler
	outs := handlerV.Call(ins)

	// Convert output parameters
	res = outs[0].Interface()
	errVal := outs[1].Interface()
	if errVal != nil {
		err = errVal.(error)
	}

	return res, err
}
