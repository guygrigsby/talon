# Wire protocol

The protocol used between `internal/gateway/client.go` (talon's CLI client) and
`internal/server/*` (talon's embedded gateway). Defined in
`internal/server/protocol.go`. Protocol version: `3`.

This document describes the legacy WebSocket frame protocol still used by
some internal tests and compatibility paths. The current UI and CLI are moving
to the Connect RPC surface.

## Frames

Every WS message is a JSON `Frame`:

```go
type Frame struct {
    Type    string          // "req" | "res" | "event"
    ID      string          // request/response correlation
    Method  string          // for req
    Params  json.RawMessage // for req
    OK      *bool           // for res
    Payload json.RawMessage // for res or event
    Error   *FrameError     // for res when !OK
    Event   string          // for event
    Seq     *int            // optional event sequence
}
```

Error codes (`internal/server/protocol.go`):
`BAD_REQUEST`, `UNAUTHORIZED`, `PROTOCOL_MISMATCH`, `METHOD_NOT_FOUND`,
`INTERNAL`, `HANDSHAKE_REQUIRED`.

## Handshake sequence

1. Client dials `/ws` (or `/`) and upgrades.
2. **Server emits** `event: connect.challenge` with `{nonce, ts}`. Client must wait for this before sending anything.
3. **Client sends** `req` with `method: "connect"` and `ConnectParams`:
   - `minProtocol`/`maxProtocol` must include 3.
   - `client.id`, `client.version`, `client.platform`, `client.mode`.
   - `role` (default `operator`), `scopes`.
   - Optional `auth.token` / `auth.password` / `auth.bootstrapToken` / `auth.deviceToken`.
4. Server validates protocol, runs `AuthConfig.Authorize()`, and replies `res` with a `HelloOK` payload: `{type: "hello-ok", protocol, server, features.{methods,events}, snapshot, auth, policy}`.
5. After hello-ok, the connection is in steady state: client sends `req` frames, server replies `res`, and either side may emit `event` frames.

If the first request after connect is anything other than `connect`, the
server replies with `HANDSHAKE_REQUIRED`.

## Auth modes

Defined in `internal/server/auth.go`:

- `none` — accepted as-is.
- `token` — client's `auth.token` constant-time-compared to server's configured token.
- `password` — declared but returns `INTERNAL` ("not yet supported").
- `trusted-proxy` — declared but returns `INTERNAL` ("not yet supported").

## Steady-state events

The CLI client routes incoming `event` frames to `Client.OnEvent`. The most
load-bearing event today is `chat`, consumed by `cmd/talon/chat.go`. Its
payload shape varies — see `extractAssistantText()` for the two known shapes
(`{phase, role, content[].text}` and `{text}`) plus a fallthrough.

Streamed text may arrive as either cumulative snapshots (each event contains
the full text so far) or deltas. `emitNew()` heuristically detects which by
checking whether the new payload starts with what's already printed.

## Adding a server-side method

1. Implement a `Handler` (signature in `internal/server/registry.go`).
2. Register it in `Registry.registerDefaults()`.
3. The method name will then appear in `HelloOK.Features.Methods`, which clients use to gate behavior.
