# talon

[![CI](https://github.com/guygrigsby/talon/actions/workflows/ci.yml/badge.svg)](https://github.com/guygrigsby/talon/actions/workflows/ci.yml)
[![Bench](https://github.com/guygrigsby/talon/actions/workflows/bench.yml/badge.svg)](https://github.com/guygrigsby/talon/actions/workflows/bench.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/guygrigsby/talon)](https://goreportcard.com/report/github.com/guygrigsby/talon)
[![Release](https://img.shields.io/github/v/release/guygrigsby/talon)](https://github.com/guygrigsby/talon/releases/latest)

A fast, single-binary, Go agent runtime: a local gateway that runs LLM-driven
agents with tools, memory, and channel integrations, plus a CLI for talking to
it.

## What is this?

talon is a self-contained agent platform written in Go. It exposes chat,
cron, channel integrations (Telegram, BlueBubbles, etc.), and plugin
management over a JSON-RPC-style WebSocket protocol, behind a single static
binary. Built around three upstream libraries:

- [agentcore](https://github.com/voocel/agentcore) — agent loop, provider
  dispatch (via LiteLLM), tool calling, subagent orchestration
- [jess](https://github.com/guygrigsby/jess) — durable agent memory + skills
  on top of agentcore
- [chromem-go](https://github.com/philippgille/chromem-go) — embeddable
  vector database backing jess's memory store

talon is the runtime that composes these into a daemon: it adds session
state, per-agent identity, channels, cost caps, secrets resolution, and a
web UI on top.

Design priorities:

- **One static binary.** No Node, no Python, no extra runtime. `go install`
  or drop a binary on a server and it runs.
- **Faster cold starts and lower memory.** Useful when the gateway lives on
  a Raspberry Pi, a small VPS, or inside a container that wakes on demand.
- **Pure-Go stack end-to-end.** chromem-go, agentcore, jess all build
  without CGO. Cross-compiles to every `go build` target.

> **Early development.** Some subcommands return
> `not yet implemented (talon-<id>)` placeholders so gaps stay visible.

## Two roles, one protocol

talon plays two roles over the same WebSocket protocol:

1. **CLI client.** Connects to a talon gateway (default
   `ws://127.0.0.1:18789/`). Most subcommands are RPC pass-throughs or local
   config operations.
2. **Embedded gateway** (`talon gateway run`). A self-contained server that
   handles chat, cron, plugin lifecycle, channel setup, and config RPCs.

You can run either half independently or together.

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
# 1. Start the gateway
talon gateway run &

# 2. Health check
talon health

# 3. Chat (uses agents.defaults.model.primary from config)
talon chat "hello"
```

## Configuring an LLM provider

API keys live in 1Password (`op://...`) or the macOS keychain
(`keychain://...`). Direct plaintext in config is discouraged.

```bash
# Bootstrap the 1Password service-account token into your keychain
talon secrets keychain-bootstrap

# Point a provider at its key in 1Password
talon config set plugins.entries.openai-compat.config.providers.openai.apiKey \
  op://talon/openai-api-key/credential
```

The `openai-compat` plugin is multi-tenant — one plugin process serves
openai, deepseek, mistral, mlx, lmstudio, ollama, and any other
OpenAI-compatible endpoint. Each tenant gets its own `baseUrl` + `apiKey`
under `plugins.entries.openai-compat.config.providers.<name>`. Anthropic is
its own plugin entry (`plugins.entries.anthropic.config.apiKey`).

**Local servers** (mlx, lmstudio, ollama, vllm, sglang, llama.cpp) work
without a key when `baseUrl` is loopback. Default ports:

- mlx: `http://localhost:8080/v1`
- lmstudio: `http://localhost:1234/v1`
- ollama: `http://localhost:11434/v1`

The model picker is config-driven only — list the models you want under
`models.providers.<name>.models[]`. Live discovery via provider `/v1/models`
endpoints is deliberately not auto-merged because it floods the picker.

## Native gateway RPCs

| Area | RPCs |
|---|---|
| Health | `health` |
| Chat | `chat.send`, `chat.history` |
| Agents & models | `agents.list`, `agents.files.*`, `models.list`, `agent.identity.get` |
| Config | `config.get`, `config.schema` |
| Cron | `cron.list/add/remove/run/status/show/enable/disable/runs` |
| Channels setup | `channels.telegram.verify/captureSender/persist` |
| Plugins | `plugins.deps.status/install/uninstall/detail` |
| Nodes | `node.list` |
| Skills | `skills.status` |
| Memory | `memory.append` |

## Channel setup (Telegram, BlueBubbles)

```bash
talon configure channel telegram
talon configure channel bluebubbles
```

The wizards walk through token verification, sender capture, and persistence.
Plugins are spawned automatically as subprocesses; no separate binary needed.

## First-party plugins

talon ships all first-party plugins as subcommands of the main binary:

```bash
talon plugin run anthropic
talon plugin run openai-compat
talon plugin run telegram
talon plugin run bluebubbles
talon plugin run whisper
talon plugin run brave
talon plugin run mac-notify
talon plugin run mac-open
```

The gateway spawns these automatically via `BuiltinPluginCmd`, which
resolves to `[<talon-executable>, "plugin", "run", <name>]`. No separate
plugin binaries are needed on non-Docker installs.

> **Migration in progress.** The `anthropic` and `openai-compat` plugins are
> being replaced by direct `agentcore/llm` dispatch in-process. See
> [`docs/migration-agentcore.md`](./docs/migration-agentcore.md).

## Layered config

talon's config lives in `~/.talon/talon.json`. talon never writes anywhere
else.

For machines that previously ran openclaw, talon reads from `~/.openclaw`
as a read-only fallback layer during migration. Pure-talon installs only
need `~/.talon`.

Reads merge `~/.talon` over `~/.openclaw` (talon priority for overlapping
keys; id-keyed arrays like `agents.list` merge by id). Writes always target
`~/.talon`. Override paths with `TALON_STATE_DIR` and `TALON_CONFIG_PATH`.

```bash
# Read the merged view
talon config get gateway.port

# Write to ~/.talon/talon.json
talon config set gateway.port 19000

# Validate the merged config
talon config validate

# Schema cache
talon config schema --refresh
```

## Config change policy: never auto-restart, never auto-load

talon never restarts the gateway after a config write and never spawns a
file watcher to auto-load changes. Reload is always explicit. After every
`config set`, talon prints a class-aware hint:

| Class | Hint |
|---|---|
| `next-rpc` (default) | applies on the gateway's next request; no restart needed |
| `hup` | send SIGHUP to the running gateway to apply (or restart) |
| `restart` | restart the gateway to apply (consumed at startup) |

Restart-class paths today: `gateway.{port,bind,auth.*}`,
`gateway.tailscale.*`, `gateway.controlUi.*`,
`plugins.entries.*.{enabled,cmd,args}`, `plugins.deny`, `plugins.load.paths`,
`skills.*`. Override per-call with `--reload=next-rpc|hup|restart`.

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

talon agents list
talon models                          # list configured models
talon models test                     # per-model smoke probe (latency + errors)
talon chat [--agent <id>] <message...>
talon chat history [--agent <id>]

talon cron list [--all]
talon cron add --schedule "@hourly" --cmd "talon health"
talon cron remove <id>
talon cron run <id>
talon cron status

talon configure channel telegram
talon configure channel bluebubbles

talon secrets audit [--check]
talon secrets migrate <path> [--vault Personal]
talon secrets keychain-bootstrap

talon plugin run <name>
```

## Path syntax

Config paths use dot-separated form, `[N]` for array indices, `["key"]` for
keys with reserved characters.

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

- **Backups** rotate on every overlay write: `~/.talon/talon.json.bak`,
  `.bak.1` … `.bak.4`.
- **Audit log** appends one JSONL record per write to
  `~/.talon/logs/config-audit.jsonl` with sha256 hashes, gateway-mode
  changes, pid/ppid/argv.
- **Last-good** sidecar at `~/.talon/talon.json.last-good` is refreshed by
  `config validate` on success.
- `~/.openclaw` (when present) is never modified.

## Development

```bash
make build      # → bin/talon
make test       # go test ./... with benchmark regression gate (TALON_BENCH=1)
make test-fast  # go test -short ./... (skips benchmark gate, faster iteration)
make vet
make fmt
make tidy
make cross      # cross-compile linux/darwin/windows × amd64/arm64
make test-e2e   # full plugin lifecycle via Docker (requires Docker)
```

Single test: `go test ./internal/config -run TestName -v`.

**Benchmarks** cover the chat hot path, per-turn filesystem I/O, and config
merge. Provider latency is excluded; all benches use an in-process stub.

```bash
make bench                     # stable numbers (~30s, BENCHTIME=1s)
make bench BENCHTIME=200x      # quick sanity loop (<1s)
```

Diff results with `benchstat`. The `make test` target includes a 3% average
regression gate via `TALON_BENCH=1`.

## Project structure

- `cmd/talon/`. Cobra CLI (root + subcommands)
- `internal/config/`. Layered config: `config.go`, `merge.go`, `edit.go`,
  `backup.go`, `schema.go`, `path.go`, `reload.go`
- `internal/openclaw/`. Path resolution for the two state layers (migration-era)
- `internal/gateway/`. WebSocket client
- `internal/server/`. Embedded gateway: protocol framing, session lifecycle,
  auth, method registry
- `internal/plugin/`. Plugin host (native go-plugin + legacy transports)
- `internal/plugins/<name>/`. First-party plugin implementations
- `internal/secrets/`. `op://` + `keychain://` resolution
- `apps/talon-op-plugin/`. 1Password CLI secrets-resolver subprocess
- `web/`. Embedded SvelteKit UI

For deeper architecture, wire-protocol, and dependency details, see
[`docs/dependencies.md`](./docs/dependencies.md),
[`docs/architecture.md`](./docs/architecture.md),
[`docs/protocol.md`](./docs/protocol.md), and
[`docs/migration-agentcore.md`](./docs/migration-agentcore.md).

## Contributing

Contributions welcome. Ground rules:

- **Open an issue first** for anything non-trivial. Use beads (`bd ready`,
  `bd show <id>`) for in-tree issue tracking.
- **Don't extend the legacy provider stack.** Per
  [`docs/migration-agentcore.md`](./docs/migration-agentcore.md), the chat
  path is moving to `agentcore` + `jess`. New work targets those directly;
  contribute upstream rather than fork into talon.
- **Run `make test vet fmt`** before pushing. CI runs the same checks plus
  the benchmark regression gate.
- **Config consumers read only at startup** belong in the `ReloadRestart`
  class in `internal/config/reload.go`. Don't add file watchers.
- **No plaintext secrets** in config or shell files. Use `op://` or
  `keychain://` references.

## License

MIT. See [LICENSE](./LICENSE).
