package wrapper

import (
	"errors"
	"log/slog"
)

// Message is a ws-wrapper JSON-encoded message.
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
	if len(m.Arguments) < 1 {
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

func (m Message) LogValue() slog.Value {
	if m.IgnoreIfFalse != nil && !*m.IgnoreIfFalse {
		return slog.GroupValue(slog.Bool("ignored", true))
	}

	var attrs []slog.Attr
	eventName := m.EventName()
	if eventName != "" {
		attrs = []slog.Attr{
			slog.String("ch", m.Channel),
			slog.String("event", eventName),
			slog.Any("args", m.HandlerArguments()),
		}
	} else if m.RequestID != nil {
		if m.ResponseError == nil {
			attrs = []slog.Attr{
				slog.Int("reqID", *m.RequestID),
				slog.Any("error", m.ResponseError),
				slog.Bool("js", m.ResponseJSError),
			}
		} else {
			attrs = []slog.Attr{
				slog.Int("reqID", *m.RequestID),
				slog.Any("data", m.ResponseData),
			}
		}
	} else {
		attrs = []slog.Attr{slog.Bool("invalid", true)}
	}
	return slog.GroupValue(attrs...)
}

// messageResponse is a response to a message
type messageResponse struct {
	Data  any
	Error error
}
