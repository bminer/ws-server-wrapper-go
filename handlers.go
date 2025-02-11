package wrapper

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// List of reserved event names. It is an error to send or receive events on the
// main channel with these event names.
const (
	EventOpen       = "open"
	EventConnect    = "connect"
	EventError      = "error"
	EventMessage    = "message"
	EventClose      = "close"
	EventDisconnect = "disconnect"
)

// IsReservedEvent checks if the event name is a reserved event name
func IsReservedEvent(eventName string) bool {
	switch eventName {
	case EventOpen:
		return true
	case EventConnect:
		return true
	case EventError:
		return true
	case EventMessage:
		return true
	case EventClose:
		return true
	case EventDisconnect:
		return true
	default:
		return false
	}
}

// handlerName is a unique name for a handler function having the specified
// channel and event name.
type handlerName struct {
	Channel string
	Event   string
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()
var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

// HandlerContextFunc is a function that can modify the context passed to every
// registered event handler before it is called.
type HandlerContextFunc func(
	ctx context.Context,
	channel string,
	eventName string,
) (context.Context, context.CancelFunc)

// Type aliases for "reserved" event handlers
type OpenHandler = func(*Client)
type ErrorHandler = func(*Client, error)
type MessageHandler = func(*Client, Message)
type CloseHandler = func(*Client, StatusCode, string)

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
	numIn := handlerT.NumIn()
	hasContext := numIn > 0 && handlerT.In(0) == contextType
	argOffset := 0
	if hasContext {
		argOffset = 1
	}
	if len(arguments) != numIn-argOffset {
		return nil, errors.New("incorrect number of arguments")
	}

	// Ensure handler has the correct return values
	errOffset := 0
	switch handlerT.NumOut() {
	case 1:
	case 2:
		errOffset = 1
	default:
		return nil, errors.New("handler should return one or two values")
	}
	if !handlerT.Out(errOffset).Implements(errorType) {
		return nil, errors.New(
			"handler's last output type must implement error",
		)
	}

	// Convert arguments to a slice of reflect.Value
	ins := make([]reflect.Value, numIn)
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
	if errOffset > 0 {
		res = outs[0].Interface()
	}
	errVal := outs[errOffset].Interface()
	if errVal != nil {
		err = errVal.(error)
	}

	return res, err
}

// emitReserved calls all handlers on the main channel with the specified event
// names. caller is called for each handler function; it is caller's
// responsibility to cast f to the appropriate function type and call it.
func emitReserved(
	caller func(f any) bool,
	lock *sync.Mutex,
	handlers map[handlerName]any,
	handlersOnce map[handlerName]any,
	eventNames ...string,
) bool {
	// Make a slice of handler functions to call
	handlerFuncs := make([]any, 0)
	// Populate handlerFuncs by reading from handlers and handlersOnce
	lock.Lock()
	for _, eventName := range eventNames {
		key := handlerName{Channel: "", Event: eventName}
		if h := handlers[key]; h != nil {
			handlerFuncs = append(handlerFuncs, h)
		}
		if h, ok := handlersOnce[key]; ok {
			delete(handlersOnce, key)
			if h != nil {
				handlerFuncs = append(handlerFuncs, h)
			}
		}
	}
	lock.Unlock()
	// Call each handler function
	called := false
	for _, f := range handlerFuncs {
		if caller(f) {
			called = true
		} // else f does not have a valid function signature
	}
	return called
}

// checkHandler ensures that reserved event handlers have the proper function
// signature
func checkHandler(channel, eventName string, handler any) error {
	if handler == nil {
		// No validation needed if handler is nil
		return nil
	}
	// Perform validation for reserved events
	if channel == "" {
		switch eventName {
		case EventOpen:
			fallthrough
		case EventConnect:
			_, ok := handler.(OpenHandler)
			if !ok {
				return fmt.Errorf(
					"handler '%s' must be func(*Client)", eventName,
				)
			}
			return nil
		case EventError:
			_, ok := handler.(ErrorHandler)
			if !ok {
				return fmt.Errorf(
					"handler '%s' must be func(*Client, error)", eventName,
				)
			}
			return nil
		case EventMessage:
			_, ok := handler.(MessageHandler)
			if !ok {
				return fmt.Errorf(
					"handler '%s' must be func(*Client, Message)", eventName,
				)
			}
			return nil
		case EventClose:
			fallthrough
		case EventDisconnect:
			_, ok := handler.(CloseHandler)
			if !ok {
				return fmt.Errorf(
					"handler '%s' must be func(*Client, StatusCode, string)",
					eventName,
				)
			}
			return nil
		}
	}

	// Perform validation for normal events
	t := reflect.TypeOf(handler)
	if t.Kind() != reflect.Func {
		return errors.New("handler must be a function")
	}

	// Ensure handler has the correct return values
	errOffset := 0
	switch t.NumOut() {
	case 1:
	case 2:
		errOffset = 1
	default:
		return errors.New("handler should return one or two values")
	}
	if !t.Out(errOffset).Implements(errorType) {
		return errors.New(
			"handler's last output type must implement error",
		)
	}

	// Passed validation
	return nil
}
