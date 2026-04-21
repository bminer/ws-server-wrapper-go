package wrapper

import "fmt"

// ClientError is an error for a specific client
type ClientError struct {
	Client *Client
	error  error
}

// Error returns the error message as a string
func (ce ClientError) Error() string {
	return ce.error.Error()
}

// Unwrap returns the underlying error.
func (ce ClientError) Unwrap() error {
	return ce.error
}

// ChannelClosedError indicates the channel has been closed and cannot be used.
type ChannelClosedError struct {
	Channel string
}

// Error returns the error message as a string.
func (e ChannelClosedError) Error() string {
	if e.Channel == "" {
		return "channel is closed"
	}
	return fmt.Sprintf("channel '%s' is closed", e.Channel)
}
