# ws-server-wrapper-go

A lightweight Go WebSocket library that implements the
[ws-wrapper](https://github.com/bminer/ws-wrapper) protocol. It wraps any
WebSocket transport and adds named events, request/response (RPC), and channels
on top — enabling seamless communication between Go clients/servers, and
[ws-wrapper](https://github.com/bminer/ws-wrapper) JavaScript clients/servers (browser
or Node.js).

[![Go Reference](https://pkg.go.dev/badge/github.com/bminer/ws-server-wrapper-go.svg)](https://pkg.go.dev/github.com/bminer/ws-server-wrapper-go)

Features:

- **Named events** — emit an event on one end, handle it on the other
- **Request / response** — send a request; the handler's return value is sent back as the response
- **Channels** — logically namespace events over a single WebSocket connection
- **Bi-directionality** — the server can also request data from clients
- **Go client** — dial out to any ws-wrapper server with `NewClient` / `Bind`; supports reconnection
- **Pluggable transports** — use any WebSocket library you want by implementing the `Conn` interface (see [Custom Adapters](#custom-adapters) for supported libraries). This library by itself has no dependencies.

## Install

```
go get github.com/bminer/ws-server-wrapper-go
```

Then, install the WebSocket library of your choice along with its corresponding adapter:

```
go get github.com/coder/websocket
go get github.com/bminer/ws-server-wrapper-go/adapters/coder
```

## Server Quick Start

The example below uses the included
[coder/websocket](https://github.com/coder/websocket) adapter:

```go
import (
    "net/http"
    wrapper "github.com/bminer/ws-server-wrapper-go"
    "github.com/bminer/ws-server-wrapper-go/adapters/coder"
    "github.com/coder/websocket"
)

wsServer := wrapper.NewServer()
wsServer.On("echo", func(s string) (string, error) {
    return s, nil
})

http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    conn, err := websocket.Accept(w, r, nil)
    if err != nil {
        return // websocket.Accept writes the HTTP response
    }
    wsServer.Accept(coder.Wrap(conn))
})
http.ListenAndServe(":8080", nil)
```

## Event Handling

```go
// Handle a named event (fire-and-forget from the client side)
wsServer.On("chat", func(msg string) error {
    fmt.Println("chat:", msg)
    return nil
})

// Handle a request and return a value
wsServer.On("add", func(a, b int) (int, error) {
    return a + b, nil
})

// Access the Client inside the handler via Context
wsServer.On("whoami", func(ctx context.Context) (string, error) {
    client := wrapper.ClientFromContext(ctx)
    return client.Get("username").(string), nil
})
```

## Request / Response (Server → Client)

The server can also send requests to a connected client and await a response:

```go
wsServer.On("open", func(c *wrapper.Client) {
    name, err := c.Request(context.Background(), "getName")
    if err == nil {
        c.Set("name", name)
    }
})
```

## Channels

Namespace events to avoid name collisions:

```go
wsServer.Of("io").On("readFile", func(filename string) ([]byte, error) {
    // TODO: Sanitize `filename` for security reasons
    return os.ReadFile(filename)
})
```

## Per-Client Handlers

Register handlers on an individual `*Client`.
They take precedence over server-level handlers for that client:

```go
wsServer.On("open", func(c *wrapper.Client) {
    c.On("secret", func(token string) (string, error) {
        return "ok", nil
    })
})
```

## Custom Adapters

Adapters wrap a WebSocket connection from any library into the `Conn` interface.

| Adapter module | Library |
|---|---|
| `github.com/bminer/ws-server-wrapper-go/adapters/coder` | [coder/websocket](https://github.com/coder/websocket) |
| `github.com/bminer/ws-server-wrapper-go/adapters/gorilla` | [gorilla/websocket](https://github.com/gorilla/websocket) |

Each adapter is a separate Go module, so you only download the one you need.

## Client Mode

Use `NewClient` and `Bind` to act as a WebSocket client that speaks the
ws-wrapper protocol. Register all handlers *before* calling `Bind` so that no
inbound message can arrive before a handler is in place:

```go
import (
    wrapper "github.com/bminer/ws-server-wrapper-go"
    "github.com/bminer/ws-server-wrapper-go/adapters/coder"
    "github.com/coder/websocket"
)

client := wrapper.NewClient(nil)
client.On("open", func(c *wrapper.Client) {
    fmt.Println("connected!")
})
client.On("news", func(headline string) error {
    // Server is sending us some news
    fmt.Println("breaking news:", headline)
    return nil
})

conn, _ := websocket.Dial(ctx, "ws://example.com/ws", nil)
client.Bind(coder.Wrap(conn))
```

If you don't need an `"open"` handler and just want a quick one-liner, pass
the connection directly:

```go
client := wrapper.NewClient(coder.Wrap(conn))
```

### Reconnection

All handlers are stored on the `Client` and survive across calls to `Bind`, so
reconnection is straightforward. Use the `userClosed` boolean in the `"close"`
handler to distinguish a user-initiated close from a connection drop — only
reconnect when `userClosed` is `false`:

```go
client := wrapper.NewClient(nil)

client.On("close", func(c *wrapper.Client, status wrapper.StatusCode, reason string, userClosed bool) {
    if userClosed {
        return // explicit close, don't reconnect
    }
    // Connection dropped; reconnect after a short delay.
    time.AfterFunc(10 * time.Second, func() {
        conn, err := websocket.Dial(ctx, "ws://example.com/ws", nil)
        if err != nil {
            log.Println("reconnect failed:", err)
            return
        }
        c.Bind(coder.Wrap(conn))
    })
})

conn, _ := websocket.Dial(ctx, "ws://example.com/ws", nil)
client.Bind(coder.Wrap(conn))
```

The `userClosed` parameter is `true` only when `client.Close()` is called explicitly by your own code.

## See Also

- [ws-wrapper](https://github.com/bminer/ws-wrapper) — the original JavaScript client/server library
- [ws-server-wrapper](https://github.com/bminer/ws-server-wrapper) – Node.js ws-wrapper server with more features
