# 0011 Agent-action audit log

Status: Accepted

Date: 2026-05-27

## Context

When an agent does something wrong, there is currently no way to reconstruct
what it did. An audit of talon's logging (2026-05-27) found:

- The only "audit log" is `~/.talon/logs/config-audit.jsonl`
  (`internal/config/backup.go`, `AuditRecord`). It records **config writes**
  only — not agent actions.
- Agent actions (tool calls, results, errors, assistant messages) flow only
  through the in-memory `SinkRegistry` (broadcast to *live* subscribers) and
  the in-memory `ChatStore`. Both are wiped on restart and lost for any
  disconnected session. After a failure, "what did the agent do in session X"
  is unanswerable from disk.
- Separately, `slog.Debug` is dead (all handlers hardcode `LevelInfo`) and
  several agent-loop errors are swallowed without a log. Those logging-hygiene
  gaps are tracked in a companion plan; this ADR is the persistent
  agent-action audit trail.

We want a durable, append-only, correlated record of what each agent did, so a
session can be traced forensically after the fact.

## Decision

Add a persistent **agent-action audit log**, owned by talon, fed from the
agentcore event stream talon already consumes. No changes to jess (see the
eventing analysis below).

### Event source — source-agnostic records, fed from agentcore today

talon defines its **own** audit event type, not `agentcore.Event`. The audit
record schema and on-disk format are decoupled from whatever produces the
events. An adapter populates audit records from the current source; the
records and the file format do not depend on it.

Today that source is the agentcore event stream talon already subscribes to
(`internal/agentcore_chat/handler.go:99` `agent.Subscribe`; the
`internal/agentcore_chat/events.go` adapter handles `EventToolExecStart`,
`EventToolExecEnd`, `EventMessageEnd`, `EventError`, …), surfaced through the
`ChatHandler` emit callbacks (`emitAgentToolStart` / `emitAgentToolResult` /
`emitError` / message emits in `internal/server/chat_agentcore.go`). That
stream carries the agent's actions: tool calls with arguments, tool results
with an error flag, assistant messages, and errors; model/session/run/agent
identity are known at the handler level. The audit sink is fed alongside the
existing `SinkRegistry` broadcast, so every event that reaches subscribers is
also persisted. No jess change is needed now.

**Why source-agnostic:** talon currently depends on agentcore directly, but a
tracked effort (`talon-17z`) may move the agent loop behind jess as an owned
harness with its own event stream. Because the audit records are defined by
talon (not aliased to `agentcore.Event`), that migration changes only the thin
**adapter** that maps source events → audit records. The schema, the on-disk
format, and every downstream consumer are unaffected. This keeps the
architectural decision (`talon-17z`) reversible instead of a prerequisite, and
the audit log ships now.

### Persistence — rotating JSONL under `~/.talon/logs`

Decision (the open item from scoping): a single append-only JSONL file,
`~/.talon/logs/agent-audit.jsonl`, size-rotated (e.g. 10 MB × N kept), mirroring
the existing `config-audit.jsonl` pattern.

Considered and rejected:
- **slog at an "audit" level** — couples the audit trail to log-shipping
  config; a misconfigured handler or level filter could silently drop audit
  records. Auditing must be complete and self-contained, independent of the
  operator's log setup.
- **Embedded store (chromem/sqlite)** — query power we don't need yet; more
  moving parts. JSONL is greppable, trivially shippable, and append-only by
  construction. Revisit if/when we need server-side audit queries.

### Record schema

One record per agent-action event, newline-delimited JSON:

- `ts` (RFC3339), `event` (`turn_start` | `tool_call` | `tool_result` |
  `message` | `error` | `turn_end`)
- correlation: `session`, `run`, `agent`, `seq` (monotonic within a run)
- per-event payload: tool `name` + `args` (tool_call); `output` + `isError`
  (tool_result); assistant text summary (message); `kind` + `message` (error);
  `model` (turn_start)
- `args`/`output`/text are **redacted and bounded** (see below).

### Secret redaction (load-bearing)

Tool arguments and results can contain secrets. The audit writer runs every
payload through `secrets.RedactJSON` / the existing redaction path before
writing, and truncates large fields to a cap. The audit log must never become
a secret-leak vector (ADR 0006; the no-cleartext-in-output convention). This
is a first-class requirement, tested.

### Scope (v1)

Captured: tool calls + arguments, tool results + error flag, assistant message
summaries, agent-loop errors, turn boundaries, and the model in use —
persisted, redacted, correlated by `session`+`run`+`seq`, surviving restart,
size-rotated.

Out of scope (v1): a query/replay UI, and the two items the agentcore stream
doesn't expose (see limitations).

## Consequences

- A real forensic trail: after a failure or restart, an operator can grep
  `agent-audit.jsonl` for a session/run and see the ordered actions, their
  results, and errors.
- New persistent write on the chat hot path. It must be cheap (async append,
  bounded payloads) so it never blocks a turn; a slow/failed audit write is
  logged and dropped, never propagated into the agent loop.
- Disk growth + retention: rotation caps total size; retention is a config
  knob with a sane default.
- **jess eventing — analyzed, not needed now.** The agentcore stream already
  carries the actions, so v1 needs no jess change. Two things it does *not*
  surface, deferred:
  - jess's *implicit* memory injection (`ContextManager.Project` recall isn't a
    tool, isn't evented). Explicit `remember`/`recall` *are* tools and so are
    audited.
  - a subagent's *inner* actions (talon subscribes to the top-level agent only;
    the `subagent` tool call + its result are audited, but the child's own tool
    calls are not).
  Auditing either would require an eventing hook in jess/agentcore. The natural
  home for that is the jess agent-loop harness tracked in `talon-17z`; when that
  lands with its own event stream, those deeper signals feed the same audit sink
  through the (swapped) adapter. Explicitly out of scope here, and no jess
  changes are made by this ADR.
- Companion logging-hygiene work (debug level knob, logging swallowed errors,
  lifecycle INFO, correlation keys) lands separately; it complements but does
  not block this audit log.
- Future: server-side audit query/replay, and a `talon audit` CLI to tail/grep
  a session's trail.
