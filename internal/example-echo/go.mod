module github.com/bminer/ws-server-wrapper-go/internal/example-echo

go 1.23.4

require (
	github.com/bminer/ws-server-wrapper-go v0.0.0
	github.com/bminer/ws-server-wrapper-go/adapters/coder v0.0.0
	github.com/coder/websocket v1.8.14
)

replace (
	github.com/bminer/ws-server-wrapper-go => ../..
	github.com/bminer/ws-server-wrapper-go/adapters/coder => ../../adapters/coder
)
