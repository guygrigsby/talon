# 0015 Logs service + web logs tab + `talon logs`

Status: Proposed

Date: 2026-05-30

## Context

The web nav ships a `logs` tab (`web/src/lib/data/sections.ts`: "streaming
gateway log, filter by handler / channel") but it routes to the unwired
`[section]/+page.svelte` placeholder. There is no way to see the gateway's
operational log from the CLI client or the web UI without tailing stderr on
the host that launched `talon gateway run`.

Two distinct log surfaces already exist on the gateway:

1. **The operational `slog` stream** (ADR 0012). Configurable level
   (`debug|info|warn|error`), correlation keys `session`/`run`/`agent`,
   lifecycle at INFO. Centralized in `internal/log` (a `consoleHandler`, a
   JSON handler, and an HCLog handler, all to stderr; `slog.SetDefault`).
   Ephemeral — nothing persists it.
2. **Structured audit JSONL files** under `~/.talon/logs` (ADR 0011 agent
   action log `agent-audit.jsonl`, plus `config-audit.jsonl` and
   `gateway-events.jsonl`). Durable, append-only, rotated
   (`internal/audit/recorder.go`); paths from `internal/talonpath`.

These answer different questions — "what is the gateway doing right now" vs
"what is the persisted record" — so the tab exposes both as sub-tabs rather
than conflating them.

The gateway already has a streaming-fan-out pattern: chat events go through a
`SinkRegistry` (`internal/server/eventsink.go`) and a Connect server-stream
(`internal/connectapi/chat_stream.go`), consumed by a resilient FE store
(reconnect + backoff, talon-15y). Logs reuse that shape rather than invent a
new one.

`talon logs` does not exist. A feature is not done until reachable from the
CLI (project convention); logs carry no secret/config, so the bar is a
command, not a `talon configure` flow.

## Decision

Add a **Logs bounded context** spanning a backend log source, a typed
`LogsService`, a web tab with two sub-tabs, and a `talon logs` command. Keep
the domain types free of `slog` and file-format types; translate at the edges
(anti-corruption), matching the DDD convention for large changes.

### Ubiquitous language / domain types

- `LogRecord{ ts, level, msg, channel, attrs map[string]string }` — one
  operational log line. Built from a `slog.Record` at the tee handler;
  `slog` types never cross the service boundary.
- `AuditRecord{ stream, ts, fields map[string]any }` — one JSONL row from a
  named audit stream. Built from a parsed line; file/format details stay in
  the reader.

"handler/channel" from the nav description maps onto `attrs`: `channel`
(source channel where a line carries one, e.g. telegram/web) and the
correlation keys `session`/`run`/`agent` from ADR 0012. There is no
first-class "handler" attribute today; filtering is over level + `channel` +
the correlation keys + a free-text substring. A future consistent
`component` attr can extend this without a contract change (filters are a
map).

### Layer A — live source: ring-buffer tee handler (`internal/log`)

A `RingTee` `slog.Handler` wraps the configured handler. On each record it
(1) passes through to the wrapped handler (stderr output unchanged) and
(2) appends a translated `LogRecord` to a bounded ring (default 1000 lines)
and broadcasts to live subscribers. Bound + fan-out mirror `SinkRegistry`;
the ring gives late subscribers (a freshly opened tab, a `talon logs` without
`--follow`) a recent snapshot.

Opt-in: `log.Init` gains a hook to install the tee so CLI-only invocations and
tests don't pay for it. Ring size is typed config read at startup → added to
the `ReloadRestart` class (`internal/config/reload.go`) per the explicit-reload
policy (no watcher).

### Layer B — `LogsService` (proto + Connect)

- `Tail(TailRequest{ level, channel, session, run, agent, text }) → stream LogRecord`
  — emits the ring snapshot (oldest→newest) then live records; server-side
  filtered.
- `AuditStreams(Empty) → []AuditStreamInfo` — the available audit streams and
  sizes.
- `AuditRead(AuditReadRequest{ stream, limit, cursor }) → AuditReadResponse`
  — newest-first page of `AuditRecord`s with a cursor.

Registered alongside the existing services; same loopback-token auth.

### Layer C — redaction (both paths)

A key-name denylist (`token`, `secret`, `authorization`, `password`, `apikey`,
and any value containing `op://` / `keychain://`) redacts matching `attrs`
values to `«redacted»` before a record leaves the gateway, on both `Tail` and
`AuditRead`. Defense in depth on top of the existing "no secrets in logs"
discipline — the gateway never streams a value a denylisted key named. Lives in
the Logs context so both paths share one implementation.

### Layer D — web `/logs` tab

Promote `logs` out of the `[section]` placeholder into a real `/logs` route
with two sub-tabs:

- **Live** — streamed `LogRecord`s; level + channel + free-text filter
  controls; auto-scroll with pause-on-scroll (reuse the transcript's
  pin-to-bottom heuristic); reconnect via the same resilient pattern as the
  chat store.
- **Audit** — pick a stream (`agent-audit` / `config-audit` /
  `gateway-events`), paged newest-first.

Technical/dense styling per the web aesthetic rules; WCAG 2.2 AA (roles,
focus, contrast, reduced-motion, hit targets) per the UI a11y rule.

### Layer E — `talon logs`

`talon logs [--follow] [--level] [--channel] [--text]`. Without `--follow`,
print the ring snapshot (filtered) and exit 0; with `--follow`, stream until
interrupted. `talon logs audit <stream> [--limit]` dumps an audit page. Shares
the `LogsService` with the web tab — one source of truth.

## Consequences

- One new `slog.Handler` and one new service; no change to existing log
  output or levels (the tee is pass-through). ADR 0012 levels/keys and ADR
  0011 audit files are consumed, not altered.
- Live logs are ephemeral (ring only); persistence of the operational stream
  is explicitly out of scope — the audit files remain the durable trail.
- Redaction is best-effort on attribute *values* by key name; it does not
  parse free-form messages. Secrets must still never be put in a log message
  body (unchanged discipline).
- New proto surface is additive (new service), so buf-breaking stays green.
- Reload: ring size is restart-only; no file watcher (reload policy).

## Out of scope (v1)

- Persisting / rotating the operational slog stream to disk.
- Full-text search or time-range queries over audit files (paged read only).
- A `component`/`handler` taxonomy beyond the attrs that exist today.
- Multi-node log aggregation across a tailnet (`InfraService.NodeList`).
