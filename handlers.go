package wrapper

import (
	"context"
	"errors"
	"reflect"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()
var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

// Calls the handler function with the given arguments. Passes the Context to
// the handler if the first argument is a Context. The handler function must
// return two values: the result and an error. This function returns the result
// and error from the handler function.
func CallHandlerWithArguments(
	ctx context.Context, handler any, arguments []any,
) (res any, err error) {
	handlerV := reflect.ValueOf(handler)
	handlerT := v.Type()

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
		argV := reflect.ValueOf(arguments[i])     // argument reflect.Value
		handlerArgT := handlerT.In(argOffset + i) // function parameter type
		if !argV.Type().ConvertibleTo(handlerArgT) {
			return nil, errors.New("argument type mismatch")
		}
		// Convert arguments to the handler's argument types
		ins[argOffset+i] = argV.Convert(handlerArgT)
	}

	// Call the handler
	outs := handlerV.Call(ins)

	res = outs[0].Interface()
	errVal := outs[1].Interface()
	if errVal != nil {
		err = errVal.(error)
	}

	return res, err
}
