# 0012 Logging conventions (levels, error-at-boundary, correlation)

Status: Accepted

Date: 2026-05-27

## Context

The 2026-05-27 logging audit found three systemic gaps (separate from the
agent-action audit log in ADR 0011, which persists actions; this ADR is about
the operational `slog` stream):

1. **Debug is dead.** All handlers hardcode `slog.LevelInfo`
   (`internal/log/log.go:70`, `internal/log/console.go:64`) and there's no
   level knob. Every `slog.Debug` in the tree is unconditionally dropped.
2. **Errors swallowed on hot paths.** Tool execution failures
   (`internal/tools/tools.go:139` returned bare into a result string;
   `internal/server/chat.go` ~925), agentcore handler errors
   (`internal/agentcore_chat/handler.go:82-107`, event-sink only), workspace /
   tool-policy resolution (`chat.go` ~679), and plugin spawn/handshake
   (`internal/plugin/native/host.go:84-126`) are invisible in logs.
3. **Inconsistent correlation + levels.** session/run/agent keys appear on some
   logs but not others; no INFO for turn/tool lifecycle; the agentcore error
   sink carries no correlation keys.

## Decision

Adopt these logging conventions and bring the code into line.

**Level is configurable.** `log.Init` takes a level; the level is resolved from
`--log-level` (flag) then `TALON_LOG_LEVEL` env (`debug|info|warn|error`),
default `info`. All three handlers (console, JSON, HCLog) honor it. This makes
`slog.Debug` usable for verbose tracing without recompiling. `--verbose` is a
convenience alias for `--log-level=debug`.

**Errors are logged where they would otherwise vanish.** At any boundary where
an error is swallowed, converted to a non-error result, or dropped, log it
(ERROR for real failures, WARN for recoverable/degraded fallback) with context.
Returning an error up a chain that *does* log it is fine; the rule targets the
terminal swallow points the audit found.

**Level discipline.**
- INFO: lifecycle — gateway/plugin/channel start, chat turn start/end, config
  reload. One line, not per-token.
- DEBUG: per-tool-call dispatch, per-delta/timing detail, recoverable retries.
- WARN: degraded-but-continuing (dropped sink, fallback to empty workspace).
- ERROR: failures that abort or lose work.

**Correlation keys.** Chat/tool/agent logs carry `session`, `run`, and (where
known) `agent`. The agentcore event-sink error path gets these keys too, so a
failure can be traced to its turn.

## Consequences

- `slog.Debug` becomes a real, opt-in tool; default output is unchanged
  (INFO).
- More code logs errors; the previously-invisible hot-path failures surface.
- `log.Init`'s signature changes (level param); callers (gateway, plugin
  `serve`) pass the resolved level. Plugin subprocesses inherit the level via
  an env passthrough so one `--log-level` covers the whole tree.
- This is hygiene, not architecture; the agent-action audit log (ADR 0011) is
  the durable forensic trail and is unaffected.
