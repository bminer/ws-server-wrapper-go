package wrapper

import (
	"errors"
	"testing"
)

func TestChannelClosedErrorMessage(t *testing.T) {
	if got := (ChannelClosedError{}).Error(); got != "channel is closed" {
		t.Errorf("main channel: expected 'channel is closed', got %q", got)
	}
	if got := (ChannelClosedError{Channel: "room"}).Error(); got != "channel 'room' is closed" {
		t.Errorf("named channel: expected \"channel 'room' is closed\", got %q", got)
	}
}

func TestClientErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := ClientError{error: inner}
	if err.Error() != "boom" {
		t.Errorf("expected 'boom', got %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("expected ClientError to unwrap to the inner error")
	}
}
