package wrapper

import (
	"errors"
)

// Message is ws-wrapper JSON-encoded message
// See https://github.com/bminer/ws-wrapper/blob/master/README.md#protocol
type Message struct {
	Channel         string `json:"c,omitempty"`
	Arguments       []any  `json:"a,omitempty"` // Arguments[0] is the event name
	RequestID       *int   `json:"i,omitempty"`
	ResponseData    any    `json:"d,omitempty"`
	ResponseError   any    `json:"e,omitempty"`
	ResponseJSError bool   `json:"_,omitempty"`
	IgnoreIfFalse   *bool  `json:"ws-wrapper,omitempty"`
}

// EventName returns the name of the event or empty string if the message is
// invalid
func (m Message) EventName() string {
	if len(m.Arguments) == 0 {
		return ""
	}
	name, ok := m.Arguments[0].(string)
	if !ok {
		return ""
	}
	return name
}

// HandlerArguments returns the arguments for the event handler
func (m Message) HandlerArguments() []any {
	if len(m.Arguments) < 2 {
		return nil
	}
	return m.Arguments[1:] // exclude event name
}

// Response returns the response data and error for this message.
func (m Message) Response() (any, error) {
	if m.RequestID == nil {
		return nil, errors.New("message is not a response")
	}
	if m.ResponseError == nil {
		// No error
		return m.ResponseData, nil
	}
	// Handle JavaScript error
	if m.ResponseJSError {
		jsErr, ok := m.ResponseError.(map[string]any)
		if !ok {
			return nil, errors.New(
				"response is a malformed JavaScript error: not an object",
			)
		}
		errMsg, ok := jsErr["message"].(string)
		if !ok {
			return nil, errors.New(
				"response is a malformed JavaScript error: message key is not a string",
			)
		}
		return nil, errors.New(errMsg)
	}
	// Handle string error
	errMsg, ok := m.ResponseError.(string)
	if !ok {
		return nil, errors.New("response error is not a string")
	}
	return nil, errors.New(errMsg)
}

// MessageResponse is a response to a message
type MessageResponse struct {
	Data  any
	Error error
}
