package wrapper

// ClientError is an error for a specific client
type ClientError struct {
	Client *Client
	error  error
}

// Error returns the error message as a string
func (ce ClientError) Error() string {
	return ce.error.Error()
}
