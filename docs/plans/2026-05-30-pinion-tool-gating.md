# Plan: pinion tool-use gating in talon (ADR 0017)

Test-first throughout. pinion is the classifier; talon stays the executor. No
ACL — `internal/toolgate` imports pinion types directly. Each phase is
independently committable and green.

## Phase 0 — add the pinion dependency

- `go.mod`: `require github.com/guygrigsby/pinion`, `replace => ../pinion`;
  `go mod tidy`.
- Verify pinion core pulls neither jess nor agentcore into talon's graph
  (`go mod graph | grep pinion`), and `make build` stays green.
- Acceptance: builds; no surprise transitive deps. Commit.

## Phase 1 — effect mapping registry

- `internal/toolgate/effects.go`: `EffectsFor(name string, args json.RawMessage) []effect.Effect`
  — the table from the ADR. Per-tool arg→scope extractors (path for fs tools).
  Unknown/plugin-undeclared tool ⇒ `effect.MaxDanger` (fail-closed).
- Test-first `effects_test.go`: read→fs.read scoped to the path; write/edit→
  fs.write; bash→exec; unknown→MaxDanger; bad/empty args→unscoped (widest).
- Acceptance: `go test ./internal/toolgate/` green.

## Phase 2 — Level 1 per-call gate

- `internal/toolgate/gate.go`: `Gate` holding `grant effect.Grant`,
  `assessor policy.Assessor`. `Classify1(effects) Decision` = `effect.Subset`
  vs grant + single-node `policy.Assess`. `Decision{Verdict, Findings, Delta}`.
- Test-first: fs.write inside grant→allow; fs.write outside scope→deny/needs;
  exec with no exec grant→deny; empty grant + read→deny.
- Acceptance: green.

## Phase 3 — Level 2 cross-call flow gate

- `internal/toolgate/flow.go`: a per-run `Accumulator` that records each call
  as a `compose` node with forward edges; `Add(name, effects) Decision` builds
  the composition-so-far, runs `analyze.Of` → `policy.Assess`, returns the
  verdict (denies the call completing a dangerous flow).
- Test-first `flow_test.go`: the exfil scenario — call 1 `secrets.read`
  (allow), call 2 `net.out` → **deny** with the `exfiltration` finding; a
  benign read→write sequence stays allow; accumulator isolates per runID.
- Acceptance: green. This is the headline guarantee.

## Phase 4 — wire the gate into chatdriver

- `internal/toolgate/jesstool.go`: wrap a jess `tool.Tool` so `Execute` runs
  Level 1 + Level 2 (via the run-scoped accumulator) before delegating; on
  deny, return the refusal JSON (model-visible), do **not** call inner.
- `chatdriver/build.go`: after `toolaccess` filtering, wrap each tool with the
  gate; resolve the per-agent `effect.Grant` from config (authored default when
  unset). The accumulator is created per run (thread runID through, or key a
  map by runID).
- `chatdriver` default grant authored: fs.read+fs.write scoped to workspace; no
  exec/net/secrets.
- Test-first: a built agent whose toolset is gated; a granted read executes, a
  bash/exec call refuses, with the default grant.
- Acceptance: existing chatdriver tests green + new gate-wiring test.

## Phase 5 — audit

- Emit a `tool_gate` audit event (ADR 0011 stream) per classification: tool,
  verdict, findings, run/session/agent. Offer the same to a pinion `audit.Sink`.
- Test-first: a denied call records a `tool_gate` deny event.
- Acceptance: green.

## Phase 6 — config + CLI (feature-done bar)

- `toolgate.mode` (off|audit|enforce, default enforce) + `toolgate.grants`
  (per-agent). `audit` mode logs but doesn't block; `enforce` blocks.
- `talon toolgate` command: show effective grant for an agent + recent
  verdicts. Wire into `talon configure`. `notYetImplemented` stubs for any
  unwired flag.
- Test-first: mode=audit lets a denied call through but still audits;
  mode=off bypasses; mode=enforce blocks.
- Acceptance: green; feature reachable from the CLI.

## Phase 7 — deterministic e2e (reuse ADR 0016 harness)

- In `internal/connectapi`, drive a real turn with the scripted model
  (`chatdriver.WithModelOverride`) that emits a tool-call sequence attempting
  exfil (a secrets-read tool then a net-out tool). Assert the gate **denies**
  the sink call (refusal event in the stream) and an audit `tool_gate` deny is
  recorded — through the real chatdriver→jess→agentcore loop, no network.
- Acceptance: green under `-race`. Ties the gate to the end-to-end path.

## Sequencing

0 → 1 → 2 → 3 build and prove the classifier in isolation (pinion-only, no
talon wiring). 4 wires it into the live loop. 5–6 add audit + the operator
surface. 7 proves it end-to-end with the deterministic harness. Phases 1–3 are
pure logic and land fast; 4 is the load-bearing integration.

## Open items to confirm at implementation

- `compose` forward-edge construction for opaque calls: sequential
  (N→N+1) vs fully-connected-forward. Start sequential (reachability gives the
  flow); revisit if it misses a real pattern.
- Per-run accumulator lifetime + cleanup (drop on turn_end, like `auditSeq`).
- bash default: gated off (exec not granted). Confirm the authored default
  grant doesn't break the user's normal workflow, or add bash to the default
  agent grant explicitly.
