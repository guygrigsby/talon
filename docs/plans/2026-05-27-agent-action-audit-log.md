# Agent-action audit log — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Status: Ready to implement (handoff)
Date: 2026-05-27
ADR: `docs/adr/0011-agent-action-audit-log.md` • Relates: `talon-17z` (agentcore→jess encapsulation; this stays source-agnostic so only the adapter changes)

**Goal:** Persist a durable, redacted, correlated trail of agent actions (tool calls, results, errors, messages, turn boundaries) to `~/.talon/logs/agent-audit.jsonl`, so a session can be traced after a failure or restart.

**Architecture:** A new `internal/audit` package defines a **source-agnostic** `audit.Event` + a `Recorder` interface, with a JSONL file recorder (async, size-rotated, secret-redacted). talon feeds it from the existing `ChatHandler` emit choke point (`emitAgentToolStart`/`emitAgentToolResult`/`emitError`/message/turn). The event type is talon-owned (NOT `agentcore.Event`), so the future jess harness (`talon-17z`) swaps only the adapter.

**Tech Stack:** Go, slog, `internal/secrets` (RedactJSON), `internal/talonpath`, `internal/config` native round-trip. Mirrors the existing `config-audit.jsonl` pattern (`internal/config/backup.go`).

---

## Context (verified)

- Existing config-audit pattern to mirror: `internal/config/backup.go` `appendAudit()` (line ~160) + `talonpath.Layer.ConfigAuditLogPath()` (`internal/talonpath/paths.go:90`).
- Event choke point: `ChatHandler.emitAgentToolStart(toolSessionKey, runID, sessionKey, toolCallID, name, argumentsJSON)` and `emitAgentToolResult(toolSessionKey, runID, sessionKey, toolCallID, name, output, isError)` (`internal/server/chat.go` ~757); `emitError(...)` and text emits in `internal/server/chat_agentcore.go`. These already fire for every agent action.
- Redaction: `secrets.RedactJSON([]byte) ([]byte, error)` (`internal/secrets/redact.go:137`) — degrades to input on parse failure.
- Config round-trip pattern: `internal/talonconfig/native.go` (struct field + `decodeViper`/`*FromJSON`/`apply*Runtime`/`MarshalTOML`), as used for `memory.recall.min_score`.

## File structure

| File | New/Mod | Responsibility |
|---|---|---|
| `internal/audit/event.go` | new | source-agnostic `Event` + `EventKind` |
| `internal/audit/recorder.go` | new | `Recorder` iface + `JSONLRecorder` (async append, rotation, redaction) |
| `internal/audit/recorder_test.go` | new | unit tests incl. redaction + rotation |
| `internal/talonpath/paths.go` | mod | `AgentAuditLogPath()` |
| `internal/talonconfig/native.go` | mod | `audit.{enabled,max_size_mb,keep}` round-trip |
| `internal/server/chat.go` | mod | `ChatHandler.audit` field + record at emit choke points |
| `internal/server/server.go` | mod | build recorder from config, inject into handler |
| `cmd/talon/audit.go` | new | `talon audit show` (read/filter the trail) |
| `cmd/talon/audit_test.go` | new | CLI filter test |

---

## Task 1: source-agnostic event type

**Files:** Create `internal/audit/event.go`, `internal/audit/recorder_test.go` (start)

- [ ] **Step 1: Write the type** in `internal/audit/event.go`:
```go
// Package audit records agent actions to a durable, redacted, correlated
// trail so a session can be traced after a failure. The Event type is
// talon-owned and source-agnostic: today it's populated from the agentcore
// event stream, but nothing here depends on agentcore (see ADR 0011 / talon-17z).
package audit

import "time"

type EventKind string

const (
	KindTurnStart  EventKind = "turn_start"
	KindToolCall   EventKind = "tool_call"
	KindToolResult EventKind = "tool_result"
	KindMessage    EventKind = "message"
	KindError      EventKind = "error"
	KindTurnEnd    EventKind = "turn_end"
)

// Event is one recorded agent action. Correlation: Session+Run+Seq order a
// run's actions. Secret-bearing fields (Args, Output, Text) are redacted and
// bounded by the recorder before they hit disk.
type Event struct {
	Ts         time.Time `json:"ts"`
	Kind       EventKind `json:"kind"`
	Session    string    `json:"session"`
	Run        string    `json:"run"`
	Agent      string    `json:"agent,omitempty"`
	Seq        int64     `json:"seq"`
	Model      string    `json:"model,omitempty"`      // turn_start
	Tool       string    `json:"tool,omitempty"`       // tool_call / tool_result
	ToolCallID string    `json:"toolCallId,omitempty"` // tool_call / tool_result
	Args       string    `json:"args,omitempty"`       // tool_call (redacted)
	Output     string    `json:"output,omitempty"`     // tool_result (redacted)
	IsError    bool      `json:"isError,omitempty"`    // tool_result
	Text       string    `json:"text,omitempty"`       // message summary
	ErrKind    string    `json:"errKind,omitempty"`    // error
	ErrMsg     string    `json:"errMsg,omitempty"`     // error
}
```
- [ ] **Step 2: Build** `go build ./internal/audit/` — clean. **Commit** (`audit: source-agnostic Event type`).

## Task 2: JSONLRecorder (async, rotation, redaction)

**Files:** Create `internal/audit/recorder.go`; Test `internal/audit/recorder_test.go`

- [ ] **Step 1: Failing test** — records are written as JSONL, secrets redacted, large fields bounded:
```go
func TestJSONLRecorder_WritesRedactedJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-audit.jsonl")
	r, err := NewJSONLRecorder(Options{Path: path, MaxSizeMB: 10, Keep: 3})
	if err != nil { t.Fatal(err) }
	r.Record(Event{Kind: KindToolCall, Session: "s", Run: "r", Seq: 1, Tool: "bash",
		Args: `{"cmd":"x","token":"sk-secret-123"}`})
	if err := r.Close(); err != nil { t.Fatal(err) } // Close flushes
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("sk-secret-123")) {
		t.Fatal("secret leaked into audit log")
	}
	if !bytes.Contains(b, []byte(`"tool":"bash"`)) {
		t.Fatalf("record not written: %s", b)
	}
}
```
- [ ] **Step 2: Run, verify fail** (`go test ./internal/audit/ -run WritesRedacted`) — undefined.
- [ ] **Step 3: Implement `recorder.go`**:
```go
type Recorder interface {
	Record(Event) // non-blocking, best-effort
	Close() error
}

type Options struct {
	Path      string
	MaxSizeMB int64 // rotate when the file exceeds this (0 = 10)
	Keep      int   // rotated files to keep (0 = 3)
	MaxField  int   // cap on Args/Output/Text bytes (0 = 8192)
}

type JSONLRecorder struct {
	opts Options
	ch   chan Event
	done chan struct{}
}

func NewJSONLRecorder(o Options) (*JSONLRecorder, error) {
	if o.MaxSizeMB == 0 { o.MaxSizeMB = 10 }
	if o.Keep == 0 { o.Keep = 3 }
	if o.MaxField == 0 { o.MaxField = 8192 }
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil { return nil, err }
	r := &JSONLRecorder{opts: o, ch: make(chan Event, 256), done: make(chan struct{})}
	go r.run()
	return r, nil
}

func (r *JSONLRecorder) Record(e Event) {
	if e.Ts.IsZero() { e.Ts = time.Now() }
	select {
	case r.ch <- e:
	default:
		slog.Warn("audit: drop event (buffer full)", "kind", e.Kind, "session", e.Session, "run", e.Run)
	}
}

func (r *JSONLRecorder) Close() error { close(r.ch); <-r.done; return nil }

func (r *JSONLRecorder) run() {
	defer close(r.done)
	for e := range r.ch {
		r.write(r.redact(e))
	}
}
```
Implement `redact(Event) Event`: for `Args`/`Output`, if it parses as JSON run `secrets.RedactJSON`; always truncate `Args`/`Output`/`Text` to `MaxField` (append `…[truncated]`). Implement `write(Event)`: `json.Marshal` + newline, open `O_APPEND|O_CREATE|O_WRONLY` 0o600, write; before writing, if `Stat().Size() > MaxSizeMB<<20` rotate (`agent-audit.jsonl` → `.1` → `.2`…, drop beyond `Keep`). Write errors are logged via slog (`slog.Error("audit: write failed", "err", err)`) and dropped — never block.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Add rotation test** `TestJSONLRecorder_RotatesAtMaxSize` (MaxSizeMB tiny via a byte override or many records; assert `.1` appears). **Run, pass.**
- [ ] **Step 6: Commit** (`audit: JSONL recorder with rotation + redaction`).

## Task 3: talonpath path

**Files:** Modify `internal/talonpath/paths.go`

- [ ] **Step 1:** Add next to `ConfigAuditLogPath` (~line 90):
```go
// AgentAuditLogPath returns the JSONL agent-action audit-log path.
func (l Layer) AgentAuditLogPath() string {
	return filepath.Join(l.LogsDir(), "agent-audit.jsonl")
}
```
- [ ] **Step 2:** `go build ./internal/talonpath/`; **commit**.

## Task 4: config knobs

**Files:** Modify `internal/talonconfig/native.go`; test mirrors existing config round-trip tests

- [ ] **Step 1: Failing round-trip test** asserting `audit.enabled`, `audit.max_size_mb`, `audit.keep` survive `config.Set`→read (mirror the `memory.recall.min_score` test).
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Add an `AuditConfig`** (Enabled bool — default true; MaxSizeMB int64; Keep int) wired through the struct + `decodeViper`/`*FromJSON`/`applyRuntime`/`MarshalTOML` exactly like `MemoryRecallConfig` (real `mapstructure` tags, not `-`). Runtime JSON path `audit.*`. Default Enabled=true (the trail is the point); zero size/keep fall back to recorder defaults.
- [ ] **Step 4: Run, pass. Commit.**

## Task 5: wire the recorder into the chat handler

**Files:** Modify `internal/server/chat.go` (handler struct + emit methods), `internal/server/server.go` (construct + inject)

- [ ] **Step 1: Failing test** in `internal/server/` using a fake recorder (`type fakeRec struct{ evs []audit.Event }; func (f *fakeRec) Record(e audit.Event){ f.evs=append(f.evs,e) }; func (f *fakeRec) Close() error {return nil}`). Drive `emitAgentToolStart`/`emitAgentToolResult`/`emitError` and assert audit events recorded with correct Kind + Session/Run/Tool/ToolCallID + IsError, and that a secret in args is redacted (recorder does redaction, so assert at recorder level in Task 2; here assert the event is forwarded with correlation fields).
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Add `audit audit.Recorder` to `ChatHandler` (nil = disabled → all calls guarded `if h.audit != nil`). In `emitAgentToolStart` add `h.recordAudit(audit.Event{Kind: KindToolCall, Session: sessionKey, Run: runID, ToolCallID: toolCallID, Tool: name, Args: argumentsJSON})`; in `emitAgentToolResult` `KindToolResult` with `Output: output, IsError: isError`; in `emitError` `KindError` with `ErrKind: kind, ErrMsg: msg`. Add a per-run `seq` (reuse the existing `seq atomic.Int64` in `runStreamAgentcore`, or a handler-side counter keyed by run) so `Seq` increments per run. Record `KindTurnStart` (with Model, Agent) at run start and `KindTurnEnd` at run end in `runStreamAgentcore` (`chat_agentcore.go`). `recordAudit` is a tiny helper that fills `Agent` if known and calls `h.audit.Record`.
- [ ] **Step 4: Run, pass.**
- [ ] **Step 5: Construct in `server.go`**: read `AuditConfig`; if `Enabled`, `audit.NewJSONLRecorder(audit.Options{Path: paths.Talon.AgentAuditLogPath(), MaxSizeMB: cfg.Audit.MaxSizeMB, Keep: cfg.Audit.Keep})` and set `handler.audit`; close it on gateway shutdown. Log `slog.Info("audit log enabled", "path", ...)` (or a debug when disabled).
- [ ] **Step 6: Run `go test ./internal/server/...`, commit** (`audit: record agent actions from the chat handler`).

## Task 6: `talon audit show` CLI (reachability)

**Files:** Create `cmd/talon/audit.go`, `cmd/talon/audit_test.go`; register in the root command

- [ ] **Step 1: Failing test** — given a temp JSONL with mixed sessions/runs, `audit show --session s --run r` prints only matching records in `seq` order. Drive the filter func directly.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** `talon audit show [--session K] [--run R] [--since DUR] [--json]`: read `paths.Talon.AgentAuditLogPath()` (+ rotated `.1`…), parse JSONL, filter by session/run/since, sort by Ts/Seq, pretty-print one line per event (`ts  kind  tool  [isError]  session/run#seq`) or raw `--json`. Register `auditCmd()` under the root command in `cmd/talon` (mirror an existing subcommand).
- [ ] **Step 4: Run, pass. Verify reachable:** `talon audit show --help` works; against a temp `TALON_STATE_DIR` with a seeded file, filtering works.
- [ ] **Step 5: Commit** (`audit: talon audit show to trace a session`).

## Task 7: redaction hardening + docs

**Files:** `internal/audit/recorder_test.go`, `docs/dependencies.md` (or a short `docs/` note)

- [ ] **Step 1: Dedicated leak test** `TestJSONLRecorder_NeverLeaksSecrets`: feed Args/Output/Text containing `op://`, `keychain://`, `sk-...`, an OpenAI-style key, and a bearer token; assert none appear in the file. (Extend `redact` if any slip — RedactJSON handles JSON; add a literal-secret scrub for non-JSON Output/Text via the existing `secrets` redactors.)
- [ ] **Step 2: Run, pass.**
- [ ] **Step 3: Doc note** in `docs/dependencies.md` (or `docs/architecture.md`): the agent-action audit log (path, what it captures, source-agnostic per ADR 0011, redaction, rotation, `talon audit show`).
- [ ] **Step 4: Commit.**

---

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/audit/... ./internal/talonconfig/... ./internal/server/... ./cmd/talon/...
golangci-lint run ./...   # the new lint gate must stay green
```
Manual: enable audit (default on), run a chat turn that calls a tool, then `talon audit show --session main` shows turn_start → tool_call → tool_result (→ error if any) → turn_end in order; `grep` the file confirms no secrets; restart the gateway and confirm prior records persist.

## Follow-ups (file as beads issues)
- `talon audit` replay/tail (`--follow`) and a web inspector view.
- When `talon-17z` lands, swap the audit adapter to consume the jess event stream; add the deeper signals (implicit memory injection, subagent inner actions) the agentcore stream doesn't expose.
- Audit-log retention by age (not just size) if needed.
