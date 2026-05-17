# Architecture

Read this when working on the CLI layer, the embedded gateway server, or
config. For wire-protocol details (frame types, handshake flow, error codes),
see `protocol.md`.

## CLI layer (`cmd/talon/`)

Cobra-based. `main.go` wires the root command and the helpers every subcommand
uses:

- `dial()` / `dialWith(urlOverride, tokenOverride)` — load `~/.openclaw/openclaw.json` (or `$OPENCLAW_CONFIG_PATH`), open a websocket to the gateway, perform the handshake, return a `*gateway.Client`.
- `runRPC(method, params)` — one-shot RPC with a 10s timeout (dial → request → close). Most commands are thin wrappers around this.
- `emit(payload)` — pretty-prints JSON. **Note:** the `|| true` in `emit()` makes it pretty-print regardless of the `--json` global flag. This is a known gap (see `PARITY.md` "Output format gap"). Don't write new commands that depend on `--json` being honored until that's fixed.

Subcommand files:

- `gateway.go` — 12 subcommands of `gateway`, only ~6 wired. Unimplemented ones return `notYetImplemented("talon-<id>")` referencing the tracking beads issue. Many flags are defined for compatibility but log `"--<flag> accepted but not yet wired"` to stderr — preserve that pattern when adding flags so parity gaps stay visible.
- `chat.go` — streams `chat` events from the gateway via `Client.OnEvent`. Accumulates either cumulative-or-delta text (`emitNew` checks if the new payload starts with what's already printed and emits the suffix; otherwise treats it as a replacement).
- `status.go` — `channels.status` probe.

### Adding a new gateway subcommand

1. Add a `gatewayXxxCmd()` function in `cmd/talon/gateway.go` returning a `*cobra.Command`.
2. Wire it into `gatewayCmd()` with `c.AddCommand(...)`.
3. If the underlying RPC isn't ready, return `notYetImplemented("talon-<beads-id>")` and create the beads issue if it doesn't exist.
4. Update the gateway subcommands table in `PARITY.md` (status column + blocker references).

## Embedded gateway (`internal/server/`)

- `server.go` — `http.Server` with `/healthz`, `/ws`, and an optional static file handler when `WebDir` is set. Read limit is 64 MiB. `isWebSocketUpgrade()` lets `/` route to either the static handler or the WS handler depending on `Upgrade` header.
- `session.go` — per-connection lifecycle. Sends `connect.challenge` event, waits for the `connect` request (10s handshake timeout), validates protocol version + auth, replies with `hello-ok`, then dispatches subsequent `req` frames through the registry. Handshake state (authed, role, scopes, clientID) is stored on the `Session`.
- `protocol.go` — wire types and error codes. See `protocol.md` for details.
- `auth.go` — modes `none` and `token` are wired (constant-time compare via `subtle.ConstantTimeCompare`). `password` and `trusted-proxy` return `INTERNAL` errors today.
- `registry.go` — method dispatch table. To add a server-side method, call `r.Register(name, handler)` in `registerDefaults()`. Default handlers today: `health` only.

## Config (`internal/config/` + `internal/openclaw/`)

talon's config layer is a two-layer overlay. See CLAUDE.md "Layered config
model" for the high-level rule; this section is the implementation map.

- `internal/openclaw/paths.go` — `Paths{Talon, Openclaw}` plus per-layer accessors (`ConfigBackupPath(n)`, `LastGoodPath()`, `LogsDir()`, `ConfigAuditLogPath()`, `CredentialsDir()`, `IdentityDir()`, `LocksDir()`, `AgentDir(id)`). Honors `TALON_STATE_DIR`, `OPENCLAW_STATE_DIR`, `TALON_CONFIG_PATH`, `OPENCLAW_CONFIG_PATH`.
- `internal/config/config.go` — typed `Config` plus `MergedBytes(p)` (deep-merge talon over openclaw, id-keyed array merge for `agents.list`-style arrays) and `Load(p)` (typed Config from the merged bytes). Default gateway port `18789`.
- `internal/config/merge.go` — `mergeJSON(base, overlay)` and `mergeValues` for the read-side merge. Treats both layers as `map[string]any` for the merge then re-marshals; the merge result is *not* what gets written back to disk.
- `internal/config/edit.go` — `Get(p, segments)` reads merged; `Set(p, segments, value, opts)` and `Unset(p, segments)` only write to `p.Talon.Config`. The protected-path guard checks the merged view. `Unset` returns `ErrNotInOverlay` when the requested path exists only in the openclaw layer.
- `internal/config/backup.go` — `writeOverlay()` does (1) rotate `~/.talon/openclaw.json.bak{.1..4}` (5-deep ring), (2) atomic temp-file rename, (3) refresh the unnumbered `.bak`, (4) append a JSONL `AuditRecord` to `~/.talon/logs/config-audit.jsonl` with sha256 hashes before/after, gateway-mode-change, pid/ppid/argv. `Validate()` refreshes `~/.talon/openclaw.json.last-good` on success.
- `internal/config/path.go` — openclaw-compatible path parser + sjson escaping (see CLAUDE.md routing).

**Don't replace the dot-path edit pipeline with full unmarshal/remarshal.**
talon shares the openclaw config schema, which is much larger than talon's
typed `Config` struct knows about. Round-tripping through the typed struct
would silently drop or reorder unknown keys.

**Don't write to `~/.openclaw/`.** The openclaw layer is read-only. Anything
that needs persistent state goes under `~/.talon/`.

## Gateway client (`internal/gateway/client.go`)

Thin WebSocket client. Pairs with the embedded server's framing. Key bits:

- `Connect()` waits for the server's `connect.challenge` event, then sends a `connect` request. The `client.id` is hardcoded to `"openclaw-tui"` so upstream openclaw gateways accept the handshake — this is intentional. Don't "fix" it.
- `Request()` correlates by frame ID via a `pending map[string]chan *Frame`. The read loop dispatches `res` frames into pending channels and `event` frames to `OnEvent`.
- The client requests broad scopes by default (`operator.admin`, `read`, `write`, `approvals`, `pairing`). Per-command scope reduction isn't wired today.

For the wire format and handshake sequence, see `protocol.md`.

## Plugin layer (`internal/plugin/`)

talon spawns plugins as subprocesses over a gRPC wire defined in `internal/plugin/pb/plugin.proto` (Plugin service host→plugin, Host service plugin→host, capability-gated). Two transports coexist:

- **Native (`internal/plugin/native/`)** — hashicorp/go-plugin with AutoMTLS for first-party Go plugins shipped inside `bin/talon`. `talon plugin run <name>` is the dispatch entry point; the gateway's `BuiltinPluginCmd` returns `[<talon-bin>, plugin, run, <name>]` so one binary covers everything. Plugin→host callbacks ride a `GRPCBroker` channel over the existing gRPC connection (no separate listener), gated by `native.NewCapabilityInterceptor` against a per-plugin `ManifestHolder` that swaps in the real manifest after `Initialize` returns.
- **Legacy (`internal/plugin/legacy/`)** — the bespoke spawn/handshake/host code used today only by the openclaw Node shim path (bundled JS extensions). Plugins authenticate via an env-supplied cookie and dial a separate loopback gRPC listener for Host callbacks. Identity flows through `legacy.Host.UnaryInterceptor` via the cookie in gRPC metadata.

`internal/plugin/pkgutil/` is the only place that holds the `MethodCapability` map and the cmd-resolution fallback (`ResolvePluginCmd` — try the configured path, then sibling-of-talon, then `$PATH`). Both transports read from it so capability gating can't drift.

`gateway_chat.go:parsePluginSpecs` tags each spec with `kind`: explicit `cmd:` or `bundled:` → `kindLegacy`; first-party builtin fallback → `kindNative`. `loadConfiguredPlugins` dispatches accordingly; both paths register their `*legacy.Instance` into the shared `legacy.Host` registry so all downstream consumers (`agentProviderFactory`, `channel` dispatch, tool runner) see a single namespace.

If you add a new bundled plugin: add it to `internal/server/plugin_deps.go:builtinPlugins`, add a constructor entry to `cmd/talon/plugin_run.go:pluginConstructors`, and implement `pb.PluginServer` under `internal/plugins/<name>/`. No standalone binary needed.
