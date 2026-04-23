# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Current ws-wrapper compatibility: **v4.1**.

## [Unreleased]

- No unreleased library changes yet.

## [1.6.0] - 2026-04-23

### Added

- Added `NewClient` and `Client.Bind` for standalone client mode: create a `Client` independent of any `Server`, register handlers before connecting, and call `Bind` to attach a connection. `Bind` can be called again after a disconnect to reconnect while preserving all registered handlers.
- Added `Channel.Close` method to close a named channel (closes #7).
- `"close"`/`"disconnect"` handlers may now receive an additional `bool` parameter indicating whether the close was user-initiated, enabling reconnection logic for standalone clients.
- When a request's `context.Context` is cancelled, a cancellation message is now sent to the remote peer (closes #9).

### Changed

- Request tracking (request ID, mutex, response channels) moved from `Server` to `Client`, eliminating cross-client lock contention.

### Fixed

- Fixed `connReqMu` usage to correctly protect `conn` and `ctx` (was incorrectly using `dataMu`).
- The `"open"`/`"connect"` event now fires synchronously inside `Bind` before the goroutine starts to read inbound messages, ensuring event handlers added in `"open"` do not miss any messages.

## [1.5.0] - 2026-04-22

### Added

- Added `gorilla/websocket` adapter module.

### Changed

- Refactored module dependencies so the primary `ws-server-wrapper-go` module has no adapter-module dependencies.

## [1.4.0] - 2026-04-01

### Added

- Added support for remote cancellation signals (ws-wrapper v4 support).
- Added substantial test coverage.

### Changed

- [ws-wrapper](https://github.com/bminer/ws-wrapper) **v4.1** is supported.
- Request rejections are encoded as JavaScript errors for ws-wrapper v4 compatibility.
- `HandlerContextFunc` now returns `Context` only.

### Fixed

- Fixed a rare race condition while client connections read messages.
- Fixed `Message.ResponseJSError` JSON parsing.

## [1.3.0] - 2025-10-02

### Changed

- Deferred JSON decoding of message arguments until handler invocation (`[]json.RawMessage`) so values can decode directly into event-handler parameter types (instead of first decoding object values as `map[string]any`).
- Updated dependencies.

### Fixed

- Fixed a bug in `ClientError.Error()`.

## [1.2.0] - 2025-05-02

### Added

- Added `Message.Processed`, a Go channel closed after a message is processed (useful for timing/synchronizing handler execution).

### Changed

- Improved event handler invocation behavior: nil interface-typed arguments are accepted when appropriate, and pointer-typed parameters are decoded correctly.

### Fixed

- Cleaned up request-response channels when sending a request fails.

## [1.1.0] - 2025-03-25

### Added

- Added `ClientFromContext` helper to return `*Client` from event-handler contexts.

### Changed

- `Accept` now closes client connections if the server has already been closed.

## [1.0.0] - 2025-03-25

### Added

- First stable release.
- Accept inbound WebSocket connections from any Go WebSocket library.
- Named channels per ws-wrapper specification.
- Client/server event handlers, including one-time handlers.
- Client request support and server broadcast support.
- Per-client thread-safe in-memory key/value storage.
- Reflection-based handler invocation with `context.Context` support and argument type conversions.
- `coder/websocket` adapter module.
- Example echo app.

[unreleased]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.6.0...HEAD
[1.6.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/bminer/ws-server-wrapper-go/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/bminer/ws-server-wrapper-go/releases/tag/v1.0.0
