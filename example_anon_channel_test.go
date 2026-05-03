package wrapper_test

import (
	"context"
	"fmt"

	wrapper "github.com/bminer/ws-server-wrapper-go"
)

// ExampleChannel shows how to stream events from a server handler back to the
// requesting client using an anonymous channel.
//
// The handler calls [Channel] to obtain an *[AnonymousChannel] tied to the
// current request, launches a goroutine that emits a series of "data" events
// followed by an "end" event, and returns the channel so the library can send
// the creation response to the client.
//
// On the client side the [Client.Request] return value is a *[AnonymousChannel]
// when the server responds with a channel. Register handlers on it before any
// events can arrive:
//
//	resp, err := client.Request(ctx, "subscribe", "news")
//	if err == nil {
//	    if ch, ok := resp.(*wrapper.AnonymousChannel); ok {
//	        ch.On("data", func(update string) error {
//	            fmt.Println("update:", update)
//	            return nil
//	        })
//	    }
//	}
func ExampleChannel() {
	wsServer := wrapper.NewServer()

	updates := []string{"first", "second", "third"}

	wsServer.On("subscribe", func(ctx context.Context, topic string) (*wrapper.AnonymousChannel, error) {
		ch := wrapper.Channel(ctx) // obtain the anonymous channel for this request
		go func() {
			defer ch.Close()
			for _, u := range updates {
				msg := fmt.Sprintf("[%s] %s", topic, u)
				if err := ch.Emit(ch.Context(), "data", msg); err != nil {
					return // client disconnected or aborted
				}
			}
			ch.Emit(ch.Context(), "end") //nolint:errcheck
		}()
		return ch, nil
	})

	_ = wsServer
}
