# talon

[![CI](https://github.com/guygrigsby/talon/actions/workflows/ci.yml/badge.svg)](https://github.com/guygrigsby/talon/actions/workflows/ci.yml)
[![Bench](https://github.com/guygrigsby/talon/actions/workflows/bench.yml/badge.svg)](https://github.com/guygrigsby/talon/actions/workflows/bench.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/guygrigsby/talon)](https://goreportcard.com/report/github.com/guygrigsby/talon)
[![Release](https://img.shields.io/github/v/release/guygrigsby/talon)](https://github.com/guygrigsby/talon/releases/latest)

A fast, single-binary, Go reimplementation of the
[openclaw](https://github.com/openclaw/openclaw) CLI and gateway.

## What is this?

[openclaw](https://github.com/openclaw/openclaw) is a local agent runtime that
exposes chat, cron, image generation, channel integrations (Telegram,
BlueBubbles, etc.) and plugin management over a JSON-RPC-style WebSocket
protocol. The reference implementation is written in TypeScript.

talon is a Go port focused on three things:

- **One static binary.** No Node, no Python, no extra runtime. `go install` or
  drop a binary on a server and it runs.
- **Faster cold starts and lower memory.** Useful when the gateway lives on a
  Raspberry Pi, a small VPS, or inside a container that wakes on demand.
- **A safer config story.** talon never writes to openclaw's state directory.
  All overrides live in a separate overlay that you can wipe without touching
  the upstream install (see [Layered config](#layered-config-the-defining-feature)).

## When to use talon

- You want to run openclaw-compatible agents on a machine where installing
  Node feels heavy (small VPS, NAS, edge device, locked-down server).
- You already run openclaw and want to experiment with a parallel gateway
  without breaking the existing state directory.
- You want a single binary in CI that can probe an openclaw gateway, edit
  config, or run a one-shot chat.

If you want the full openclaw feature surface today, run upstream openclaw.
talon is a subset (see [PARITY.md](./PARITY.md)) and is **early development**.
Some subcommands return `not yet implemented (talon-<id>)` placeholders so the
gaps stay visible.

## Two roles, one protocol

talon plays two roles over the same WebSocket protocol:

1. **CLI client.** Talks to an upstream openclaw gateway (default
   `ws://127.0.0.1:18789/`). Most subcommands are RPC pass-throughs or local
   config operations.
2. **Embedded gateway** (`talon gateway run`). A self-contained server that
   speaks the same protocol and handles chat, cron, image generation, plugin
   lifecycle, channel setup, and config RPCs natively.

You can run either half independently. Point talon's CLI at upstream openclaw,
point upstream openclaw's clients at talon's gateway, or use talon end-to-end.

## Install

```bash
go install github.com/guygrigsby/talon/cmd/talon@latest
```

Or build locally:

```bash
git clone https://github.com/guygrigsby/talon
cd talon
make build   # → bin/talon
```

Requires Go 1.26+. Pre-built binaries for linux/darwin/windows × amd64/arm64
are published on the [releases page](https://github.com/guygrigsby/talon/releases/latest).

## Quickstart

```bash
# 1. Talk to an already-running openclaw gateway on 127.0.0.1:18789
talon health
talon agents list
talon chat "hello"

# 2. Or run talon's own gateway and point your client at it
talon gateway run &
talon health
```

## Using talon as your openclaw backend

talon's embedded gateway handles chat inference, cron jobs, image generation,
plugin management, and channel setup. Enough to run agents without a separate
openclaw installation.

### Docker (recommended)

```bash
# Build the image and start the gateway on port 18789 (openclaw's default).
# Stop any running openclaw gateway first, or override the port.
make docker-run

# Tail logs
make docker-logs

# Stop
make docker-stop
```

The container bind-mounts `~/.openclaw` and `~/.talon` at their host paths
so workspace paths resolve transparently inside the container.
`--restart=unless-stopped` keeps it up across crashes and reboots.

### Native

```bash
talon gateway run                  # listens on :18789 (default)
talon gateway run --port 18790     # different port
talon gateway run --token <secret> # require auth on connections
```

### Pointing clients at talon

The talon CLI defaults to `ws://127.0.0.1:18789/`. No config change needed if
the gateway runs on the default port. For other openclaw-compatible clients,
set their gateway URL to the same address.

```bash
# Change the port talon's CLI dials (gateway on a non-default port)
talon config set gateway.port 18790

# Shared auth token (must match --token on the gateway)
talon config set gateway.auth.token <secret>
```

### Configuring an LLM provider

The gateway resolves provider API keys from per-agent auth profiles
(`~/.talon/agents/<id>/agent/auth-profiles.json` or the openclaw equivalent).

**OpenAI**

```bash
talon configure channel  # interactive wizard
# Or write directly to:
# ~/.talon/agents/main/agent/auth-profiles.json → { "openai:default": "<key>" }
```

**DeepSeek.** Set `DEEPSEEK_API_KEY` in the environment, or write the key to
the auth profiles file under `"deepseek:default"`.

**LM Studio** (local, no key needed by default):

```bash
# Default base URL: http://localhost:1234/api/v0
talon config set models.providers.lmstudio.baseUrl http://192.168.1.10:1234/api/v0
```

When the gateway runs inside Docker, loopback URLs (`localhost`, `127.0.0.1`)
in `baseUrl` are rewritten to `host.docker.internal` so LM Studio on the host
is reachable without manual config.

**1Password / keychain secret references.** Any string value starting with
`op://` or `keychain://` is resolved at runtime by the secrets subsystem:

```bash
talon config set gateway.auth.token op://Personal/talon-gateway/token
talon secrets migrate gateway.auth.token  # move a literal into 1Password
talon secrets keychain-bootstrap          # store the OP service-account token
```

### Native gateway RPCs

| Area | RPCs |
|---|---|
| Health | `health` |
| Chat | `chat.send`, `chat.history` |
| Agents & models | `agents.list`, `agents.files.*`, `models.list`, `agent.identity.get` |
| Config | `config.get`, `config.schema` |
| Cron | `cron.list/add/remove/run/status/show/enable/disable/runs` |
| Images | `images.generate/fetch/upload/list/delete`, `images.workflows.*`, `images.manager.*` |
| Channels setup | `channels.telegram.verify/captureSender/persist` |
| Plugins | `plugins.deps.status/install/uninstall/detail` |
| Nodes | `node.list` |
| Skills | `skills.status` |
| Memory | `memory.append` |

### Channel setup (Telegram, BlueBubbles)

Interactive wizards write channel config and send a confirmation DM:

```bash
talon configure channel telegram
talon configure channel bluebubbles
```

The wizard walks through token verification, sender capture, and persistence.
Plugins are spawned automatically as subprocesses; no separate binary needed.

### First-party plugins

talon ships all first-party plugins as subcommands of the main binary:

```bash
talon plugin run telegram
talon plugin run bluebubbles
talon plugin run deepseek
talon plugin run whisper
talon plugin run brave
talon plugin run mac-notify
```

The gateway spawns these automatically via `BuiltinPluginCmd`, which resolves
to `[<talon-executable>, "plugin", "run", <name>]`. No separate plugin
binaries are needed on non-Docker installs.

## Layered config (the defining feature)

talon's config is a two-layer overlay:

- `~/.openclaw/openclaw.json`. Managed by openclaw; talon treats it as
  **read-only**.
- `~/.talon/openclaw.json`. talon's own state; the **only** place talon writes.

Reads merge `~/.talon` over `~/.openclaw` (talon priority for overlapping
keys; id-keyed arrays like `agents.list` merge by id). Writes always target
the talon overlay. Override paths with `TALON_STATE_DIR`,
`OPENCLAW_STATE_DIR`, `TALON_CONFIG_PATH`, `OPENCLAW_CONFIG_PATH`.

```bash
# Read the merged view (talon overrides openclaw)
talon config get gateway.port

# Write goes to ~/.talon/openclaw.json; ~/.openclaw is never modified
talon config set gateway.port 19000

# Show both layer paths
talon config file --all

# Validate the merged config (schema-aware once cache is populated)
talon config schema --refresh   # populate ~/.talon/cache/config-schema.json
talon config validate
talon config validate --strict  # fail when no schema cache is available
```

## Config change policy: never auto-restart, never auto-load

talon never restarts the gateway after a config write and never spawns a file
watcher to auto-load changes. Reload is always explicit. After every
`config set`, talon prints a class-aware hint:

| Class | Hint |
|---|---|
| `next-rpc` (default) | applies on the gateway's next request; no restart needed |
| `hup` | send SIGHUP to the running gateway to apply (or restart) |
| `restart` | restart the gateway to apply (consumed at startup) |

Restart-class paths today: `gateway.{port,bind,auth.*}`,
`gateway.tailscale.*`, `gateway.controlUi.*`, `plugins.entries.*.{enabled,cmd,args}`,
`plugins.deny`, `plugins.load.paths`, `skills.*`. HUP class is empty pending
a SIGHUP handler in talon's embedded gateway. Override per-call with
`--reload=next-rpc|hup|restart`.

## Common commands

```bash
talon health                          # gateway health
talon config get <path>               # read merged value
talon config set <path> <value>       # write to talon overlay
talon config set <path> <value> --merge      # deep merge
talon config set <path> <value> --replace    # bypass protected-path guard
talon config set <path> <value> --dry-run    # validate without writing
talon config unset <path>             # remove from talon overlay only
talon config validate [--strict|--syntax-only]
talon config schema [--refresh] [--section <name>]
talon config file [--all]

talon gateway probe                   # reachability + auth check
talon gateway status [--deep]         # service status + RPC probe
talon gateway call <method> [--params JSON]
talon gateway run [--port N] [--token <secret>]
talon gateway health
talon gateway stability
talon gateway usage-cost --days 30

talon agents list
talon models
talon status [--deep]
talon chat [--agent <id>] <message...>
talon chat history [--agent <id>]

talon cron list [--all]
talon cron add --schedule "@hourly" --cmd "talon health"
talon cron remove <id>
talon cron run <id>
talon cron status
talon cron show <id>
talon cron enable <id>
talon cron disable <id>
talon cron runs [<id>]

talon configure channel telegram
talon configure channel bluebubbles

talon secrets audit [--check]
talon secrets migrate <path> [--vault Personal]
talon secrets keychain-bootstrap
talon secrets reload

talon plugin run <name>               # deepseek|telegram|brave|whisper|bluebubbles|mac-notify
```

## Path syntax

Config paths use openclaw's syntax: dot-separated, `[N]` for array indices,
`["key"]` for keys with reserved characters.

```bash
talon config set gateway.port 19000
talon config set 'agents.list[1].model' '"claude-opus-4-7"'
talon config set 'channels.telegram.groups["*"].requireMention' true
```

The protected-path guard refuses to silently drop entries from a protected
map/list (`agents.defaults.models`, `models.providers[.<id>]`,
`plugins.entries`, `auth.profiles`, `agents.list`,
`models.providers.<id>.models`). Pass `--merge` to layer additively, or
`--replace` to bypass the guard.

## Layered-write side effects

- **Backups** rotate on every talon-overlay write:
  `~/.talon/openclaw.json.bak`, `.bak.1` … `.bak.4`.
- **Audit log** appends one JSONL record per write to
  `~/.talon/logs/config-audit.jsonl` with sha256 hashes, gateway-mode
  changes, pid/ppid/argv.
- **Last-good** sidecar at `~/.talon/openclaw.json.last-good` is refreshed
  by `config validate` on success.
- `~/.openclaw` is never modified.

## Development

```bash
make build      # → bin/talon
make test       # go test ./... with benchmark regression gate (TALON_BENCH=1)
make test-fast  # go test -short ./... (skips benchmark gate, faster iteration)
make vet
make fmt
make tidy
make cross      # cross-compile linux/darwin/windows × amd64/arm64
make test-e2e   # full plugin lifecycle via Docker (requires Docker, ~5-30s)
```

Single test: `go test ./internal/config -run TestName -v`.

**Benchmarks** cover the chat hot path, per-turn filesystem I/O, and config
merge. Provider latency is excluded; all benches use an in-process stub.

```bash
make bench                     # stable numbers (~30s, BENCHTIME=1s)
make bench BENCHTIME=200x      # quick sanity loop (<1s)
```

Diff results with `benchstat`. The `make test` target includes a 3% average
regression gate via `TALON_BENCH=1`; it requires serialized package execution
(`-p=1`) and is skipped by plain `go test ./...` to avoid parallel-bench
contention. Use `make test-fast` when iterating and don't need the gate.

`make web*` and `make gateway-run-with-ui` depend on a sibling repo at
`../openclaw/ui`. Override with `WEB_DIR=` / `WEB_DIST=` or skip them.

## Project structure

- `cmd/talon/`. Cobra CLI (root + subcommands)
- `internal/config/`. Layered config: `config.go`, `merge.go`, `edit.go`, `backup.go`, `schema.go`, `path.go`, `reload.go`
- `internal/openclaw/`. Path resolution for the two layers
- `internal/gateway/`. WebSocket client (handshake, request/response correlation, event delivery)
- `internal/server/`. Embedded gateway: protocol framing, session lifecycle, auth, method registry
- `internal/plugin/`. Plugin host, gRPC client adapter, image provider adapter
- `internal/plugins/<name>/`. First-party plugin implementations (deepseek, telegram, brave, whisper, bluebubbles, mac-notify)
- `internal/pluginrun/`. Shared gRPC lifecycle for all first-party plugins
- `internal/provider/`. Provider interface + native implementations (openai, deepseek)
- `apps/talon-*-plugin/`. Thin `main()` wrappers (delegate to `internal/plugins/<name>`)

For deeper architecture and wire-protocol details, see
[`docs/architecture.md`](./docs/architecture.md) and
[`docs/protocol.md`](./docs/protocol.md).

## Contributing

Contributions are welcome. A few ground rules:

- **Open an issue first** for anything non-trivial (new subcommand, RPC,
  config path class). For parity gaps tracked in [PARITY.md](./PARITY.md),
  reference the existing `talon-<id>` placeholder so the gap stays linked.
- **Update PARITY.md in the same PR** when you ship a parity gap. The status
  column is the source of truth for shipped vs partial vs missing commands.
- **Run `make test vet fmt`** before pushing. CI runs the same checks plus
  the benchmark regression gate.
- **Keep parity stubs discoverable.** Unwired flags should log `"--<flag>
  accepted but not yet wired"` to stderr, and unimplemented commands should
  return `notYetImplemented("talon-<id>")` rather than a generic error.
- **Config consumers read only at startup** belong in the `ReloadRestart`
  class in `internal/config/reload.go`. Don't add file watchers or
  auto-reload triggers.

See [`docs/architecture.md`](./docs/architecture.md) for the deeper map of
where things live.

## License

MIT. See [LICENSE](./LICENSE).
