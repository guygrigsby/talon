# 0016 Deterministic chat e2e: a scripted model seam + UI-through test

Status: Proposed

Date: 2026-05-30

## Context

There is no test that drives a real chat turn through the stack. The existing
`internal/connectapi/chat_stream_e2e_test.go` only broadcasts fabricated events
into the `SinkRegistry` and checks the client receives them — it never
exercises `ChatService.Send` → chatdriver → jess → agentcore loop → model →
event stream → render. That loop is the most load-bearing and least-tested
path; the recent composer wedge (talon-15y) and the post-send dead air
(talon-i3n) were both in it.

The only non-deterministic element in that path is the LLM. Everything else is
deterministic given inputs:

- **jess** dispatches to a `model.Model` (`Stream(ctx, msgs, tools) (<-chan
  Chunk, error)` + `SupportsTools()`). `jess.LiteLLM(...)` is just the default
  cloud implementation; `jess.WithModel(m)` accepts any `model.Model`.
- **agentcore** (`github.com/voocel/agentcore`, wrapped behind jess's
  anti-corruption layer) is reached *through* jess's model adapter, so a fake
  `model.Model` short-circuits before any real LLM call — one fake covers jess
  and the agentcore loop, including tool-call rounds.
- **pinion** (`github.com/guygrigsby/pinion`, tool risk classification +
  audit) contains no LLM and is not yet wired into talon. Its `policy.Assessor`
  / `audit.Sink` / `exec.Executor` / `Primitive` are already interfaces with a
  `FakePrim` test double, so it is deterministic by construction. Out of scope
  here; relevant only when it integrates (its own ADR).

talon's chatdriver hardcodes the model: `chatdriver/build.go` always builds
`jess.LiteLLM(...)` from config. There is no way to inject a substitute, so the
loop can't be tested without a network LLM.

## Decision

Make the chat loop deterministically testable by injecting a scripted model at
the one non-deterministic seam, and add two e2e layers on top.

### Seam: a scripted `model.Model`, contributed upstream to jess

The reusable fake belongs in jess, not talon (contribute-upstream convention;
no parallel reimplementation). Add a public `jess/modeltest` package:

- `modeltest.Script` — an ordered program of emissions: reasoning text, visible
  text deltas, tool calls, usage, and a terminal stop. `Stream` replays it as
  `model.Chunk`s on the returned channel.
- Message capture — records the `[]message.Message` and `[]ToolSpec` it was
  called with, so tests can assert what the loop sent the model (system prompt,
  history, tool schemas).
- Multi-turn support — a script can emit a tool call, expect the loop to run
  the tool and call back, then emit the post-tool answer. This exercises the
  real agentcore tool-iteration loop deterministically.

Until the jess PR merges, talon consumes a thin local `modeltest` and swaps to
the upstream package on the next jess bump (tracked, not permanent).

### talon seam: `chatdriver.Builder` model override

Add an opt-in override so the gateway (and tests) can supply a `model.Model`
instead of the config-built `jess.LiteLLM`:

- `Builder.WithModel(m model.Model)` (or `WithModelFactory(func(choice) (model.Model, error))`
  if per-choice routing is needed). Default unchanged: nil override → existing
  `jess.LiteLLM` path. The override skips provider-auth resolution so a fake
  needs no API key or base URL.

### Layer 1 — backend e2e (Go, in-process, deterministic)

- Construct the gateway in-process against a temp `TALON_STATE_DIR` (replicating
  what `gateway run` wires: registry, ChatStore, chatdriver runner, Connect
  mux), with the chatdriver built using a scripted model.
- Serve it via `httptest` and drive the real typed `ChatService` client:
  `Send` then `Subscribe`.
- Assert the full event sequence (`thinking → delta(s) → final`) arrives with
  correct cumulative text and run id; assert history persists the user + final
  assistant turns; assert a tool-call script drives a real tool round.
- No Docker, no network, no secrets; runs under plain `go test`.

### Layer 2 — UI-through e2e (Playwright, headless Chromium)

- Reuse the vitest browser-mode harness (talon-lvn) / Playwright. Serve the
  built Svelte app pointed at a gateway running the scripted model (same seam),
  with the `#token=` auth handoff.
- Drive the real UI: type in the composer, press Enter, and assert the DOM
  shows the optimistic user bubble, the pending loading indicator (talon-i3n),
  the streamed assistant text, and the settled final render. Also covers the
  resilient reconnect (talon-15y) by dropping/restoring the stream.

## Consequences

- The chat loop gains real, deterministic coverage at two altitudes; the
  regressions that bit recently (wedge, dead air) become guarded.
- One small talon API addition (`Builder.WithModel`), default behavior
  unchanged.
- A reusable scripted model lands upstream in jess, usable by any jess
  consumer; talon carries a temporary local copy only until the bump.
- pinion needs nothing here; when it integrates into the tool path, its
  existing interface seams make the same in-process e2e extend to it.

## Out of scope (v1)

- A high-fidelity HTTP-boundary fake (fake OpenAI SSE + `base_url`) that also
  exercises the real LiteLLM serialization. Worth adding later as a third,
  higher-fidelity variant; the scripted `model.Model` is cheaper and covers the
  loop logic.
- Multi-node / tailnet chat.
- pinion integration and its audit assertions.
