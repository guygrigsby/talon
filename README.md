# talon

A fast, Go-rewrite-in-progress, openclaw-compatible CLI and gateway. Drop-in
alias for `talon-01s`. Tracks command parity against
[openclaw](https://github.com/openclaw/openclaw) — see
[PARITY.md](./PARITY.md) for the per-command status.

talon plays two roles over the same WebSocket protocol:

1. **CLI client** — talks to an upstream openclaw gateway (default
   `ws://127.0.0.1:18789/`). Most subcommands here are RPC pass-throughs.
2. **Embedded gateway server** (`talon gateway run`) — speaks the same
   protocol but only implements `health` natively today. Useful for
   protocol-level testing; not yet a full openclaw replacement.

Status: **early development.** Many openclaw subcommands return
`not yet implemented (tracked as talon-<id>)` so parity gaps stay visible.

## Install

```bash
go install github.com/guygrigsby/talon/cmd/talon@latest
```

Or build locally:

```bash
git clone <this repo>
cd talon
make build   # → bin/talon
```

Requires Go 1.26+.

## Layered config (the defining feature)

talon's config is a two-layer overlay:

- `~/.openclaw/openclaw.json` — managed by openclaw; talon treats it as
  **read-only**.
- `~/.talon/openclaw.json` — talon's own state; the **only** place talon
  writes.

Reads merge `~/.talon` over `~/.openclaw` (talon priority for overlapping
keys; id-keyed arrays like `agents.list` merge by id). Writes always target
the talon overlay. Override paths with `TALON_STATE_DIR`,
`OPENCLAW_STATE_DIR`, `TALON_CONFIG_PATH`, `OPENCLAW_CONFIG_PATH`.

```bash
# read the merged view (talon overrides openclaw)
talon config get gateway.port

# write goes to ~/.talon/openclaw.json; ~/.openclaw is never modified
talon config set gateway.port 19000

# show both layer paths
talon config file --all

# validate the merged config (schema-aware once cache is populated)
talon config schema --refresh   # populate ~/.talon/cache/config-schema.json
talon config validate
talon config validate --strict  # fail when no schema cache is available
```

## Config-change policy: never auto-restart, never auto-load

talon never restarts the gateway after a config write and never spawns a file
watcher to auto-load changes. Reload is always explicit. After every
`config set`, talon prints a class-aware hint:

| Class | Hint |
|---|---|
| `next-rpc` (default) | applies on the gateway's next request — no restart needed |
| `hup` | send SIGHUP to the running gateway to apply (or restart) |
| `restart` | restart the gateway to apply (consumed at startup) |

Restart-class paths today: `gateway.{port,bind,auth.*}`,
`gateway.tailscale.*`, `gateway.controlUi.*`, `plugins.entries.*.enabled`,
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
talon gateway health
talon gateway stability
talon gateway usage-cost --days 30

talon agents list
talon models
talon status [--deep]
talon chat [--agent <id>] <message...>
talon chat history [--agent <id>]
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
- ~/.openclaw is never modified.

## Development

```bash
make build      # → bin/talon
make test       # go test ./...
make vet
make fmt
make tidy
make cross      # cross-compile linux/darwin/windows × amd64/arm64
```

Single test: `go test ./internal/config -run TestName -v`.

`make web*` and `make gateway-run-with-ui` depend on a sibling repo at
`../openclaw/ui`. Override with `WEB_DIR=` / `WEB_DIST=` or skip them.

## Project structure

- `cmd/talon/` — Cobra CLI (root + subcommands)
- `internal/config/` — layered config: `config.go`, `merge.go`, `edit.go`, `backup.go`, `schema.go`, `path.go`, `reload.go`
- `internal/openclaw/` — path resolution for the two layers
- `internal/gateway/` — WebSocket client (handshake, request/response correlation, event delivery)
- `internal/server/` — embedded gateway: protocol framing, session lifecycle, auth, method registry

For deeper architecture and the wire-protocol details, see
[`docs/architecture.md`](./docs/architecture.md) and
[`docs/protocol.md`](./docs/protocol.md). Future Claude Code instances should
start with [`CLAUDE.md`](./CLAUDE.md).

## Issue tracking

talon uses **beads** (`bd`) for local-only issue tracking — every parity
gap and follow-up is referenced as `talon-<id>` in `PARITY.md` and in
`notYetImplemented("talon-<id>")` calls in the source. Run `bd ready` to
find work.
