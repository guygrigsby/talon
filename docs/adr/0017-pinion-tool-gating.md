# 0017 Tool-use safety: pinion classification + gating in talon dispatch

Status: Accepted

Date: 2026-05-30

Implemented: 2026-06-01 (talon-327). Phases 0-7 landed: `internal/toolgate`
(effect mapping, Level 1 per-call gate, Level 2 flow accumulator), wired into
`chatdriver.BuildAgent` after toolaccess filtering, with mode (off/audit/
enforce) + per-agent/global grant widening in typed native config, `tool_gate`
audit events, the `talon toolgate` command, and a `talon configure toolgate`
wizard. Deterministic e2e in `internal/connectapi` proves the gate refuses a
bash call through the real loop and records the verdict.

One deviation from the effect-mapping table below: `claude_memory` (along with
`remember`/`recall`/`finish_onboarding`) is treated as a trusted first-party
control-plane tool and exempted from gating, rather than mapped to `fs.read`.
These tools take bounded, structured inputs against fixed talon-owned stores
(not model-controlled paths), so gating them as generic `fs.read` would deny
them under the default workspace grant and break ADR 0013 with no safety gain.
See `toolgate.TrustedInternalTool`.

Deferred to a follow-up: per-agent grant config currently rides the global
`toolgate.defaults.allow` plus per-agent `agents.list[].toolgate.allow` read by
chatdriver; the per-agent override does not yet round-trip through the typed
native config (only the global `toolgate.*` does). Interactive approval for
`NeedsApproval` remains out of scope (it maps to deny).

## Context

talon runs an agentic tool loop (chatdriver → jess → agentcore): the model
proposes tool calls (read/write/edit/bash/grep/glob, claude_memory, plugin
tools) and talon executes them. Today the only guardrail is `toolaccess.Policy`
— a name-level allow/deny list (`filterToolRunner`). Nothing classifies what a
tool call actually *does* (its capabilities) or detects dangerous *flows*
(e.g. read a secret, then send it to the network).

`github.com/guygrigsby/pinion` (the author's own library) classifies exactly
this: primitives declare `effect.Effect`s (`fs.read`, `fs.write`, `net.out`,
`exec`, `secrets.read`, …); `analyze.Of(composition)` produces a
`Footprint{Effects, Flows}` where a `Flow{Source, Sink}` is a source effect
reaching a sink effect through the call graph; and `policy.Assessor.Assess`
returns `Allow | NeedsApproval | Deny` with `Finding`s. `policy.Default()`
flags `secrets.read → net.out` (exfiltration), `fs.read → net.out`,
`net.in → exec`, `net.in → fs.write`. pinion's example
(`pinion/examples/classifier`) gates *compositions it owns*; this ADR is the
example's "Next step (B)": move the gate into talon so the running gateway
classifies **every** agent tool call.

Decisions taken with the author (who owns both repos):

- **No anti-corruption layer.** talon imports pinion and uses its types
  (`effect`, `analyze`, `policy`, `compose`) directly. The usual DDD-ACL
  convention is for isolating a *vendor* dependency; pinion is first-party and
  co-evolves with talon, so a translation layer is pure overhead here.
- **Both levels.** Per-call effect gating (Level 1) **and** cross-call flow
  detection (Level 2).
- **Enforce by default**, shipping with an authored default grant.
- **needs-approval → deny** (fail-safe; no interactive approval channel yet).
- **In-process via `replace`.** pinion is unpublished; talon's go.mod adds it
  with `replace github.com/guygrigsby/pinion => ../pinion`. pinion's core
  go.mod pulls neither jess nor agentcore, so the import is clean. Drop the
  replace when pinion is tagged.

## Decision

Add `internal/toolgate`: a package that uses pinion to classify and gate every
tool call on the live chatdriver path, enforcing by default and auditing every
verdict. **pinion is the classifier; talon remains the executor** — talon does
not adopt `pinion/exec`. talon runs the tool via jess as today; pinion only
decides whether it may.

### Effect mapping (the core artifact)

Each tool call is turned into a pinion view — an effect set with scope derived
from the call's args:

| tool | effects (scope from args) |
| --- | --- |
| read, grep, glob, claude_memory | `fs.read` (scope = path / workspace glob) |
| write, edit | `fs.write` (scope = path) |
| bash | `exec` (a sink; a command string can't be scoped, so bash is treated as high-danger) |
| plugin tools | the effects the plugin declares; **unlabeled ⇒ `effect.MaxDanger`** (fail-closed) |

The mapping lives in `internal/toolgate` as a registry keyed by tool name, with
per-tool arg→scope extractors (the path for fs tools; none for bash). Scope
extraction is best-effort and conservative: when a path can't be resolved, the
effect is unscoped (widest), which can only tighten the verdict.

### Level 1 — per-call gating

For the call's effects, compute `effect.Subset(callEffects, grant)`. A
non-empty delta (effects the session grant doesn't cover) and/or a
`policy.Assess` of the single-node footprint drives the verdict. Catches
"write outside workspace", "exec/net when not granted", etc.

### Level 2 — cross-call flow gating

A per-turn accumulator records each tool call as a node in a `compose`
composition with forward edges (call N → call N+1), conservatively modeling
"data from earlier calls may reach later calls". On each new call, build the
composition of calls-so-far, run `analyze.Of` → `policy.Assess`, and **deny the
call that completes a dangerous flow** (e.g. a prior `secrets.read` reaching
this `net.out`). Conservative by design: it over-approximates data flow
(flagging read-then-send even when unrelated), which is the safe direction.
The accumulator is keyed by `runID`; it resets per turn (the unit a single
prompt's tool loop runs in).

### Verdict handling

- `Allow` → talon executes the tool as today.
- `Deny` (or `NeedsApproval`, which maps to deny in v1) → talon does **not**
  execute; the gate returns a model-visible refusal JSON
  (`{refused, verdict, reasons}`) so the agent sees and reacts (matching the
  pinion example), not a transport error.

### Gate point

Wrap each jess `tool.Tool` in `chatdriver.BuildAgent`, after `toolaccess`
filtering — the effect-level sibling of the existing name-level
`filterToolRunner`. The wrapper holds the per-run accumulator, grant, and
assessor. (The legacy `server.ToolRunner` path, if still reachable, gets the
same decorator.)

### Grant + policy (authored defaults)

A per-agent `effect.Grant`, resolved from config, default authored here:
`fs.read` + `fs.write` scoped to the agent workspace; **no** `exec`, `net.out`,
`net.fetch`, or `secrets.read`. Assessor is `policy.Default()`. So out of the
box: reads/writes inside the workspace pass; bash (exec) and any network or
secret access are denied unless the grant is widened; any read→net or
secret→net flow is denied regardless of grant.

### Audit

Every classification emits an audit event (ADR 0011 agent-action stream, new
`tool_gate` kind: tool, verdict, findings, run, session, agent) **and** is
offered to a pinion `audit.Sink`. Verdicts are logged in all modes, so even a
future audit-only mode produces the same record.

### Config + CLI (feature-done bar)

- `toolgate.mode = "enforce" | "audit" | "off"` (default `enforce`).
- `toolgate.grants` — per-agent effect grants (the authored default applies
  when unset).
- Wired into `talon configure`; a `talon toolgate` command shows the effective
  grant for an agent and recent verdicts. Unwired flags keep
  `notYetImplemented("talon-<id>")` stubs.
- Reload class: `toolgate.*` is read per-turn in `BuildAgent` (like the model
  and tool set), so changes apply on the next turn — no `ReloadRestart` entry.

## Consequences

- Every agent tool call on the live path is classified and gated; the default
  posture denies exec/network/secret-exfil unless explicitly granted.
- talon takes a first-party dependency on pinion (local `replace` until
  tagged); pinion's flow rules become talon's safety policy.
- Refusals are model-visible, so the agent can adapt (try a permitted path)
  rather than hard-erroring the turn.
- Conservative flow modeling will produce false positives (unrelated
  read-then-send flagged). Acceptable for a safety gate; the grant + a future
  provenance-aware flow model can tighten it.
- bash is effectively gated off by default (exec not in the default grant) —
  a deliberate, possibly disruptive choice; widening the grant per agent is the
  escape hatch.

## Out of scope (v1)

- Interactive approval (prompt the human via a channel/UI for `NeedsApproval`).
- Provenance-accurate flow (tracking the actual secret value through tool
  args/outputs) instead of conservative topological reachability.
- Gating non-chatdriver paths beyond reusing the same decorator.
- Adopting `pinion/exec` to run tools (talon keeps its own executor).
