# Logging hygiene — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Status: Ready to implement (handoff)
Date: 2026-05-27
ADR: `docs/adr/0012-logging-conventions.md`

**Goal:** Make `slog.Debug` usable via a level knob, log the swallowed errors the audit found, add lifecycle INFO, and thread correlation keys.

**Architecture:** Add a level to `internal/log.Init` (resolved from `--log-level`/`TALON_LOG_LEVEL`), wire all three handlers to honor it, then fix the specific call sites the audit flagged. No new packages.

**Tech Stack:** Go, `log/slog`, the existing `internal/log` handlers, cobra (`cmd/talon`).

---

## Context (verified)

- `internal/log/log.go:66` `Init(format Format)`; JSON handler `slog.LevelInfo` at `:70`. `internal/log/console.go:64` `level: slog.LevelInfo`. `internal/log/hclog.go` always-on.
- `cmd/talon/main.go:62` registers `--log-format`; `:68` `PersistentPreRunE`; `:73` `talonlog.Init(talonlog.ParseFormat(f))`. Plugin subprocess init: `internal/plugin/native/serve.go:25`.
- Swallow sites (audit): `internal/tools/tools.go:139` (bare return into caller's result string), `internal/server/chat.go` (~679 workspace/tool-policy, ~925 tool exec → result string only), `internal/agentcore_chat/handler.go:82-107` (BuildAgent/SetMessages/Prompt → sink only), `internal/plugin/native/host.go:84-126` (client/Dispense/Initialize wrapped, not logged).

## File structure

| File | Responsibility |
|---|---|
| `internal/log/log.go`, `console.go`, `hclog.go` | accept + honor a level; `LevelFromEnv` helper |
| `cmd/talon/main.go` | `--log-level`/`--verbose` flag + `TALON_LOG_LEVEL`; pass level to `Init`; export env for plugins |
| `internal/plugin/native/serve.go` | pass level to `Init` (inherit via env) |
| `internal/server/chat.go`, `internal/agentcore_chat/handler.go`, `internal/tools/...` callsites, `internal/plugin/native/host.go` | log swallowed errors; add lifecycle INFO + correlation |

---

## Task 1: level knob in `internal/log`

**Files:** `internal/log/log.go`, `console.go`, `hclog.go`; Test `internal/log/log_test.go`

- [ ] **Step 1: Failing test** — `Init` honors a level; Debug emitted only at debug:
```go
func TestInit_LevelFiltersDebug(t *testing.T) {
	// capture via a buffer handler isn't trivial through Init (it writes os.Stderr);
	// instead test the resolver + that newConsoleHandler(level) gates.
	if LevelFromEnv("debug") != slog.LevelDebug { t.Fatal("debug") }
	if LevelFromEnv("") != slog.LevelInfo { t.Fatal("default info") }
	h := newConsoleHandlerLevel(io.Discard, slog.LevelWarn)
	if h.Enabled(context.Background(), slog.LevelInfo) { t.Fatal("info should be filtered at warn") }
	if !h.Enabled(context.Background(), slog.LevelError) { t.Fatal("error should pass at warn") }
}
```
- [ ] **Step 2: Run, verify fail** (undefined `LevelFromEnv`/`newConsoleHandlerLevel`).
- [ ] **Step 3: Implement.** Add `func LevelFromEnv(s string) slog.Level` (parse debug/info/warn/error, case-insensitive; default info; reads the arg, caller passes flag-or-`os.Getenv("TALON_LOG_LEVEL")`). Change `Init(format Format)` → `Init(format Format, level slog.Level)`; pass `level` to `slog.NewJSONHandler(..., &slog.HandlerOptions{Level: level})`, to a new `newConsoleHandlerLevel(out, level)` (keep `newConsoleHandler` calling it with `LevelInfo`), and to the HCLog handler (gate its `Enabled`). Bridge `stdlog` at `level` too.
- [ ] **Step 4: Run, pass. Commit** (`log: configurable level (LevelFromEnv) honored by all handlers`).

## Task 2: wire the flag + env

**Files:** `cmd/talon/main.go`, `internal/plugin/native/serve.go`

- [ ] **Step 1:** In `main.go`: register `--log-level` (string) and `--verbose` (bool). In `PersistentPreRunE`, resolve: `lvl := talonlog.LevelFromEnv(firstNonEmpty(flagLogLevel, os.Getenv("TALON_LOG_LEVEL")))`; if `--verbose`, force `slog.LevelDebug`. Call `talonlog.Init(talonlog.ParseFormat(f), lvl)`. After Init, `os.Setenv("TALON_LOG_LEVEL", lvl.String-ish)` so spawned plugins inherit (mirror the existing format passthrough).
- [ ] **Step 2:** In `native/serve.go`, change the `Init` call to pass `talonlog.LevelFromEnv(os.Getenv("TALON_LOG_LEVEL"))`.
- [ ] **Step 3:** `go build ./...`; manual: `TALON_LOG_LEVEL=debug talon gateway run` emits debug; default does not. **Commit** (`log: --log-level/--verbose flag + TALON_LOG_LEVEL, inherited by plugins`).

## Task 3: log swallowed errors

**Files:** `internal/server/chat.go`, `internal/agentcore_chat/handler.go`, `internal/plugin/native/host.go`, the tool-exec callsite

- [ ] **Step 1:** At each audit site, add a log with correlation, without changing control flow:
  - tool exec failure (where the tool error becomes a result string, `chat.go` ~925 / the agentcore tool path): `slog.Error("tool execution failed", "tool", name, "session", sessionKey, "run", runID, "err", err)` (the args/output stay redacted in the audit log; the slog line carries the error, not the secret args).
  - workspace/tool-policy resolution (`chat.go` ~679): `slog.Warn("workspace/tool-policy resolution failed; continuing without", "session", sessionKey, "err", err)`.
  - agentcore `handler.go:82-107` BuildAgent/SetMessages/Prompt: add `slog.Error("agentcore <step> failed", "agent", agentID, "session", sessionKey, "run", runID, "err", err)` alongside the existing `sink.Error(...)`.
  - plugin host `host.go:84-126`: `slog.Error("plugin <step> failed", "plugin", name, "err", err)` before each wrapped return.
- [ ] **Step 2:** `go build ./... && go vet ./...`. Add/extend a test where feasible (e.g. assert the agentcore handler logs on BuildAgent error via a captured handler), else rely on build+vet. **Commit** (`log: surface swallowed errors on the chat/tool/plugin hot paths`).

## Task 4: lifecycle INFO + correlation

**Files:** `internal/server/chat_agentcore.go` (turn boundaries), tool dispatch path, `internal/agentcore_chat/events.go` (error sink keys)

- [ ] **Step 1:** Turn start/end in `runStreamAgentcore`: `slog.Info("chat turn start", "session", sessionKey, "run", runID, "agent", agentID, "model", selectedModelID)` and `slog.Info("chat turn end", "session", sessionKey, "run", runID, "dur", time.Since(start))`. Per-tool-call dispatch at DEBUG: `slog.Debug("tool dispatch", "tool", name, "session", sessionKey, "run", runID)` in the tool-start emit.
- [ ] **Step 2:** Ensure the agentcore error-sink emit carries `session`/`run` keys (thread them into the `emitError` slog line if missing).
- [ ] **Step 3:** `go build ./... && go vet ./...`. **Commit** (`log: lifecycle INFO for turns + DEBUG tool dispatch + correlation keys`).

---

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/log/... ./internal/server/... ./cmd/talon/... ./internal/agentcore_chat/...
golangci-lint run ./...   # gate must stay green
```
Manual: `talon gateway run` (default) shows INFO lifecycle, no debug; `talon gateway run --verbose` (or `TALON_LOG_LEVEL=debug`) shows tool-dispatch DEBUG; trigger a tool error and confirm an ERROR line with tool/session/run; confirm plugin subprocess logs also honor the level.

## Follow-ups
- A request-scoped logger (`slog.With(session,run)`) handed down the chat path so every line is correlated without per-call keys — larger refactor, deferred.
