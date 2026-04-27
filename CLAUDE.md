# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Orientation (always load)

`talon` is a fast, Go-based, openclaw-compatible CLI. Drop-in alias for
`talon-01s`. Two roles over the same WebSocket protocol:
1. **CLI client** that talks to an upstream openclaw gateway (default `ws://127.0.0.1:18789/`).
2. **Embedded gateway server** (`talon gateway run`) that speaks the same protocol but only implements `health` natively today.

### Layered config model (load-bearing)

talon's state lives in **two stacked directories**:

- `~/.openclaw` — managed by the openclaw runtime; **talon treats it as read-only**.
- `~/.talon` — talon's own state; the only place talon ever writes.

Reads **merge `~/.talon` over `~/.openclaw`** (talon priority for overlapping keys; id-keyed arrays like `agents.list` merge by id). Writes **always target the talon overlay** at `~/.talon/openclaw.json`. The `internal/openclaw/paths.go` module is the single source of truth for path resolution; honor `TALON_STATE_DIR`/`OPENCLAW_STATE_DIR` and `TALON_CONFIG_PATH`/`OPENCLAW_CONFIG_PATH` if you add anything that resolves filesystem paths.

Two consequences that bite if you forget them:

- The protected-path guard in `Set` checks the **merged view**, not the overlay alone — a write that would shadow openclaw entries is refused without `--merge` / `--replace`. `--replace` only bypasses the guard; it cannot delete openclaw-layer entries from the merged view (tracked as talon-9ic, "tombstones").
- `gateway.auth.mode` pruning only removes credentials from the **talon overlay**. If openclaw still has a stale token/password, `Set` returns it in `StaleOpenclawPaths` and the CLI emits a warning. The merged view will keep the openclaw value until the user clears it on that side.

Most RPC methods invoked by talon's CLI (`config.schema`, `agents.list`,
`chat.send`, `models.list`, `usage.cost`, etc.) are served by an upstream
openclaw gateway, **not** by `talon-gateway`. `make gateway-run` does not make
those work.

`PARITY.md` is the source of truth for which commands ship, are partial, are
missing, or are out of scope. **Read it before adding/modifying CLI surface,
and update it in the same change.**

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
`gateway-run-with-ui`, and `all` targets depend on a sibling repo at
`../openclaw/ui`; override with `WEB_DIR=` / `WEB_DIST=` or skip them.

### Workflow conventions (load-bearing — keep here)

- **Issue tracking is beads (`bd`)**, local-only with no git remote. Run `bd ready` to find work, `bd show <id>` for details. Parity gaps are tracked as `talon-XXX` issue IDs referenced from `PARITY.md` and from `notYetImplemented("talon-<id>")` calls in code.
- Do **not** use TodoWrite/TaskCreate or markdown TODO files — the project enforces beads.
- When closing a parity gap, update `PARITY.md` (status column + blocker references) in the same change.
- Keep `notYetImplemented("talon-<id>")` stubs around for unwired flags/commands; that's how the parity gap stays discoverable.

### Policy: config changes are explicit (talon-5zx)

talon never auto-restarts the gateway after a config write and never spawns a
file watcher to auto-load changes. Hot-reload happens only on an explicit
signal (SIGHUP where supported) or a restart. The path-class registry lives
in `internal/config/reload.go`:

- `ClassifyReload(segments) ReloadClass` — returns `ReloadNextRPC` (default for unknown paths), `ReloadHUP` (currently empty until talon-f06), or `ReloadRestart`.
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
| The CLI layer, RPC plumbing, or `cmd/talon/*` | `docs/architecture.md` § CLI layer |
| The embedded gateway server, handshake, auth, or WS framing | `docs/architecture.md` § Embedded gateway, then `docs/protocol.md` |
| The gateway client (`internal/gateway/client.go`) or connect handshake quirks | `docs/protocol.md` |
| Config loading or dot-path edits (`internal/config/*`) | `docs/architecture.md` § Config |
| Adding a new `gateway` subcommand or wiring an unwired flag | `docs/architecture.md` § CLI layer + `PARITY.md` |
| Anything beads-related beyond the basics above | the beads slash commands or `bd help` |

If your task touches multiple areas, load only the sections you need — these
docs are split so each can be read independently.
