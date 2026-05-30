# Plan: deterministic chat e2e (ADR 0016)

Test-first throughout (write the failing test, then the code). Each phase is
independently committable and green.

## Phase 0 — scripted model.Model

- `internal/chatdriver/modeltest/script.go`: a `Script` builder + a
  `model.Model` impl whose `Stream` replays `model.Chunk`s (reasoning, text
  deltas, tool calls, usage, stop). `SupportsTools()` returns true. Captures
  the `[]message.Message` / `[]ToolSpec` it received for assertions.
- Tests: `script_test.go` — replays a text-only script; replays a
  reasoning+text script; replays a tool-call→callback→answer script; asserts
  captured messages.
- Marked as the local mirror of the intended upstream `jess/modeltest` (see
  Upstream below).
- Acceptance: `go test ./internal/chatdriver/...` green.

## Phase 1 — chatdriver model-override seam

- `internal/chatdriver/build.go`: add `Builder.WithModel(m model.Model)` (and
  store on the builder). In `BuildAgent`, when an override is set, use it via
  `jess.WithModel` and skip provider-auth resolution; otherwise the existing
  `jess.LiteLLM` path, unchanged.
- Test-first: `build_test.go` — override path builds an agent with no
  provider/auth configured (proves auth is bypassed); nil override still
  requires/uses config (existing behavior unchanged).
- Acceptance: existing chatdriver tests still pass; new override test green.

## Phase 2 — L1 backend e2e (in-process, deterministic)

- `internal/connectapi/chat_turn_e2e_test.go` (or a small `e2e` helper):
  - Build a gateway in-process: temp `TALON_STATE_DIR`, minimal config + agent
    files, `ChatStore`, `chatdriver.NewChatRunner` wired with a scripted model,
    Connect mux via `httptest`.
  - Drive the typed `ChatService`: `Send("hi")`, then `Subscribe`.
  - Assert: event order `thinking → delta(cumulative grows) → final`; final
    text matches the script; run id consistent; `History` shows user + assistant
    turns.
  - Second case: a tool-call script drives a real tool round (register a stub
    tool) and the post-tool answer streams.
- Test-first: write the assertions against the not-yet-wired harness, watch it
  fail, then build the harness wiring until green.
- Acceptance: `go test ./internal/connectapi/...` green, no network.

## Phase 3 — L2 UI-through e2e (Playwright/vitest browser)

- `web/` browser test (vitest browser mode, talon-lvn harness) OR a Playwright
  spec: serve the built app against a gateway process running the scripted
  model (reuse Phase 1 wiring exposed as a test binary/fixture), with the
  `#token=` handoff.
- Drive the real UI: type + Enter; assert DOM shows the user bubble, the
  pending loading indicator, the streamed assistant text, and the final render.
- Add a reconnect case: drop the subscribe stream mid-run, assert recovery
  (guards talon-15y end-to-end).
- Acceptance: `pnpm test` green in CI (Chromium headless).

## Upstream (parallel, non-blocking)

- PR `jess/modeltest` (the Phase 0 scripted model, generalized) to
  `github.com/guygrigsby/jess`. On merge + version bump, delete talon's local
  mirror and import the upstream package.
- pinion: add public `policy.AssessorFunc` + a recording `audit.Sink` test
  helper in the pinion repo so it's e2e-ready when it integrates into talon
  (separate, not part of this plan).

## Sequencing

Phase 0 → 1 → 2 are the unblocked, pinion-free backbone and deliver real
end-to-end coverage. Phase 3 builds on them. Upstream work can land any time
after Phase 0.
