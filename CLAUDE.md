# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Orientation (always load)

`talon` is a standalone, Go-based agent runtime: a single static binary that
runs a local agent gateway, plus a CLI for talking to it. Two roles over the
same WebSocket protocol:

1. **CLI client** — connects to a local or remote talon gateway.
2. **Embedded gateway server** (`talon gateway run`) — hosts the agent loop,
   tools, memory, channels (Telegram/etc.), and serves the web UI.

talon is now standalone. The old compatibility runtime, extension tree, and
multi-workspace subagent model have been removed; new work should follow the
Talon-owned config and plugin surfaces described in ADRs.

All architecture decisions are recorded with ADRs. Any new large change
requires a new ADR in docs/adr

### Architecture stack (load-bearing — do not reinvent)

The chat path is **agentcore + jess + chromem-go**, not in-tree provider
plumbing. The current source tree still contains legacy
`internal/provider/*`, plugin shims (`internal/plugins/openaicompat`,
`internal/plugins/anthropic`), and `internal/server/chat.go`'s inline loop —
all of which mirror upstream primitives and are being deleted per
`docs/migration-agentcore.md`. **Do not extend the direct provider stack.**

Default direction for any chat / provider / tool / memory work:

- Provider dispatch → `agentcore/llm` (wraps LiteLLM upstream)
- Agent loop + event stream + tool dispatch → `agentcore.Agent`
- Context assembly + memory recall → `jess/memory.ContextManager`
- Vector store for memory → `chromem-go` (via `jess/memory.NewChromemStore`)
- Built-in tools (read/write/edit/bash) → `agentcore/tools`
- Subagent invocation → `agentcore/subagent`

If a piece doesn't exist upstream, **contribute it upstream** rather than
fork into talon. See [Contribute upstream](feedback_contribute_upstream.md)
in memory and the talon convention: no vendor, no parallel reimplementation.

Full per-dep details, scope boundaries, and how they compose:
`docs/dependencies.md`. Migration progress + remaining cleanup:
`docs/migration-agentcore.md`.

### Config and State Model

Talon's state root is `~/.talon` by default. Override with
`TALON_STATE_DIR`; override the config file with `TALON_CONFIG_PATH`.

- `~/.talon/config.toml` is the human-owned config file.
- The main agent's Markdown context files (`IDENTITY.md`, `SOUL.md`,
  `AGENTS.md`, `USER.md`) live directly in `~/.talon`.
- File-backed subagents live in `~/.talon/subagents/*.md`.
- Third-party gRPC plugin binaries live in `~/.talon/plugins`.
- Logs, cache, backups, credentials, and generated state have dedicated
  subdirectories under `~/.talon`.

The gateway still adapts native TOML into a JSON-shaped runtime view
internally while callers are migrated to typed accessors. Do not add new
long-lived config as dotted JSON paths unless an existing surface requires it;
prefer typed native config fields and document larger changes with an ADR.

### Common commands

```bash
make build      # → bin/talon
make test       # go test ./...
make vet
make fmt
make tidy
make cross      # cross-compile linux/darwin/windows × amd64/arm64
```

Single test: `go test ./internal/server -run TestName -v`. The `web*`,
`gateway-run-with-ui`, and `all` targets build the embedded SvelteKit UI from
`web/`. Override paths with `WEB_DIR=` / `WEB_DIST=` or skip them.

### Workflow conventions (load-bearing — keep here)

- **Issue tracking is beads (`bd`)**, local-only with no git remote. Run
  `bd ready` to find work, `bd show <id>` for details. Open work items are
  tracked as `talon-XXX` issue IDs.
- Do **not** use TodoWrite/TaskCreate or markdown TODO files — the project
  enforces beads.
- Keep `notYetImplemented("talon-<id>")` stubs around for unwired flags/
  commands; that's how the gap stays discoverable.

### Policy: config changes are explicit (talon-5zx)

talon never auto-restarts the gateway after a config write and never spawns
a file watcher to auto-load changes. Hot-reload happens only on an explicit
signal (SIGHUP where supported) or a restart. The path-class registry lives
in `internal/config/reload.go`:

- `ClassifyReload(segments) ReloadClass` — returns `ReloadNextRPC` (default
  for unknown paths), `ReloadHUP` (currently empty until talon-f06), or
  `ReloadRestart`.
- `ParseReloadClass(s)` — for the `--reload` CLI override.
- `(ReloadClass).Hint(path)` — the user-facing message.

When you add a config consumer that's only read at gateway startup, add its
path to the `ReloadRestart` list. When you add one that genuinely benefits
from SIGHUP and the embedded gateway can reload it, move it to `ReloadHUP`.
Don't add file watchers or auto-reload triggers anywhere.

## Progressive disclosure (read on demand)

Don't load these unless your task touches the area. Each file is self-contained.

| When working on… | Read |
|---|---|
| Provider dispatch, agent loop, tools, or memory | `docs/dependencies.md` (then the agentcore/jess source under `$(go env GOMODCACHE)`) |
| The legacy chat path that's being removed | `docs/migration-agentcore.md` |
| The CLI layer, RPC plumbing, or `cmd/talon/*` | `docs/architecture.md` § CLI layer |
| The embedded gateway server, handshake, auth, or WS framing | `docs/architecture.md` § Embedded gateway, then `docs/protocol.md` |
| The gateway client (`internal/gateway/client.go`) or connect handshake quirks | `docs/protocol.md` |
| Config loading or dot-path edits (`internal/config/*`) | `docs/architecture.md` § Config |
| Plugin host (spawn/handshake/lifecycle, native or legacy) or a new first-party plugin | `docs/architecture.md` § Plugin layer |
| Anything beads-related beyond the basics above | the beads slash commands or `bd help` |

If your task touches multiple areas, load only the sections you need — these
docs are split so each can be read independently.
