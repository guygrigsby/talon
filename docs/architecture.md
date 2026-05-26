# Architecture

Read this when working on the CLI layer, the embedded gateway server, or
config. For wire-protocol details (frame types, handshake flow, error codes),
see `protocol.md`.

## CLI layer (`cmd/talon/`)

Cobra-based. `main.go` wires the root command and the helpers every subcommand
uses:

- `dial()` / `dialWith(urlOverride, tokenOverride)` — load `~/.talon/config.toml` (or `$TALON_CONFIG_PATH`), connect to the gateway, perform auth, and return a gateway client.
- `runRPC(method, params)` — one-shot RPC with a 10s timeout (dial → request → close). Most commands are thin wrappers around this.
- `emit(payload)` — pretty-prints JSON. **Note:** the `|| true` in `emit()` makes it pretty-print regardless of the `--json` global flag. This is a known gap (see `PARITY.md` "Output format gap"). Don't write new commands that depend on `--json` being honored until that's fixed.

Subcommand files:

- `gateway.go` — 12 subcommands of `gateway`, only ~6 wired. Unimplemented ones return `notYetImplemented("talon-<id>")` referencing the tracking beads issue. Many flags are defined but log `"--<flag> accepted but not yet wired"` to stderr — preserve that pattern when adding flags so gaps stay visible.
- `chat.go` — streams `chat` events from the gateway via `Client.OnEvent`. Accumulates either cumulative-or-delta text (`emitNew` checks if the new payload starts with what's already printed and emits the suffix; otherwise treats it as a replacement).
- `status.go` — `channels.status` probe.

### Adding a new gateway subcommand

1. Add a `gatewayXxxCmd()` function in `cmd/talon/gateway.go` returning a `*cobra.Command`.
2. Wire it into `gatewayCmd()` with `c.AddCommand(...)`.
3. If the underlying RPC isn't ready, return `notYetImplemented("talon-<beads-id>")` and create the beads issue if it doesn't exist.

## Embedded gateway (`internal/server/`)

- `server.go` — `http.Server` with `/healthz`, `/ws`, and an optional static file handler when `WebDir` is set. Read limit is 64 MiB. `isWebSocketUpgrade()` lets `/` route to either the static handler or the WS handler depending on `Upgrade` header.
- `session.go` — per-connection lifecycle. Sends `connect.challenge` event, waits for the `connect` request (10s handshake timeout), validates protocol version + auth, replies with `hello-ok`, then dispatches subsequent `req` frames through the registry. Handshake state (authed, role, scopes, clientID) is stored on the `Session`.
- `protocol.go` — wire types and error codes. See `protocol.md` for details.
- `auth.go` — modes `none` and `token` are wired (constant-time compare via `subtle.ConstantTimeCompare`). `password` and `trusted-proxy` return `INTERNAL` errors today.
- `registry.go` — method dispatch table. To add a server-side method, call `r.Register(name, handler)` in `registerDefaults()`. Default handlers today: `health` only.

## Config (`internal/config/` + `internal/talonconfig/` + `internal/talonpath/`)

Talon's human config is `~/.talon/config.toml`. The runtime still adapts the
native TOML model into a JSON-shaped runtime view while older handlers are
moved to typed accessors.

- `internal/talonpath/paths.go` — state/config path resolution for `~/.talon`, including `config.toml`, backups, logs, credentials, agent auth profiles, `subagents/`, and `plugins/`. Honors `TALON_STATE_DIR` and `TALON_CONFIG_PATH`.
- `internal/talonconfig/native.go` — Viper-backed native TOML structs, legacy JSON migration, TOML marshal, and runtime JSON adapter.
- `internal/config/config.go` — `MergedBytes(p)` adapts native TOML to the JSON-shaped gateway view and caches by config file stat metadata. `Load(p)` returns the small typed config used by CLI dialing.
- `internal/config/edit.go` — `Get(p, segments)`, `Set(p, segments, value, opts)`, and `Unset(p, segments)` write native TOML through the runtime adapter while path-specific callers migrate.
- `internal/config/backup.go` — config writes rotate `~/.talon/config.toml.bak{.1..4}`, append a JSONL `AuditRecord` to `~/.talon/logs/config-audit.jsonl`, and refresh the last-good sidecar on successful validation.
- `internal/config/path.go` — dot-path parser + sjson escaping.

**Don't replace the dot-path edit pipeline with full unmarshal/remarshal.**
The config schema is much larger than talon's typed `Config` struct knows
about. Round-tripping through the typed struct would silently drop or
reorder unknown keys.

## Gateway client (`internal/gateway/client.go`)

Connect-based client. Key bits:

- `Request()` sends a unary RPC through `talon.v1.RpcService/Dispatch`.
- Auth token resolution comes from native config via `config.Load`.

For the wire format and handshake sequence, see `protocol.md`.

## Plugin layer (`internal/plugin/`)

talon spawns plugins as subprocesses over a gRPC wire defined in `internal/plugin/pb/plugin.proto` (Plugin service host to plugin, Host service plugin to host, capability-gated).

- **Native (`internal/plugin/native/`)** — hashicorp/go-plugin with AutoMTLS for first-party Go plugins shipped inside `bin/talon`. `talon plugin run <name>` is the dispatch entry point; the gateway's `BuiltinPluginCmd` returns `[<talon-bin>, plugin, run, <name>]` so one binary covers everything. Plugin to host callbacks ride a `GRPCBroker` channel over the existing gRPC connection, gated by `native.NewCapabilityInterceptor` against a per-plugin `ManifestHolder` that swaps in the real manifest after `Initialize` returns.

`internal/plugin/pkgutil/` is the only place that holds the `MethodCapability` map and the cmd-resolution fallback (`ResolvePluginCmd` — try the configured path, then sibling-of-talon, then `$PATH`) so capability gating can't drift.

If you add a new bundled plugin: add it to `internal/server/plugin_deps.go:builtinPlugins`, add a constructor entry to `cmd/talon/plugin_run.go:pluginConstructors`, and implement `pb.PluginServer` under `internal/plugins/<name>/`. No standalone binary needed.
