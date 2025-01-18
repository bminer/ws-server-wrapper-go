package wrapper

// ClientError is an error for a specific client
type ClientError struct {
	Client *Client
	error  error
}

func (ce ClientError) Error() string {
	return ce.Error()
}
