# talon: port the chat driver to the jess facade — Design

- Status: Approved (brainstormed 2026-05-29)
- Repo: `github.com/guygrigsby/talon`
- Branch: `feat/port-to-jess-facade`
- Depends on: jess facade additions (jess PR #5 / branch `feat/facade-additions-for-talon`), consumed via a local `replace github.com/guygrigsby/jess => ../jess` until merged.

## Context

talon drives `agentcore` directly for chat: `internal/agentcore_chat` builds an
`agentcore.Agent` and `cmd/talon/gateway_agentcore.go` subscribes/prompts/waits
on it. jess is now a facade (`jess.New` -> `Agent`/`Session`/`Run`) that owns the
agent run; this port moves talon onto it so talon no longer drives agentcore.

The only thing that *forces* a change is `memory.NewContextManager`, which moved
into jess's `internal/acl` (build fails at `internal/agentcore_chat/build.go:260`).
Everything else compiles against new jess today; the rest of this rewrite is the
chosen full-facade adoption.

**Guiding principle:** jess provides unopinionated primitives; talon owns its
architecture. Session lifecycle is talon's decision, not jess's.

## Decision: implementation swap behind talon's stable server seam, with a rename

The exploration showed talon's server talks to the agent driver through ONE
seam — `AgentcoreRunFn` — and the server layer (RPC handlers, `ChatStore`, the
`EventSink` fan-out registry) does not otherwise touch agentcore. So this is a
surgical swap of the driver behind that seam, plus a rename so "agentcore" leaves
talon's vocabulary (agentcore is now an implementation detail hidden inside jess).

### Session lifecycle: per-turn rebuild + seed (talon's choice)

talon's gateway is per-turn RPC, `ChatStore` is the durable source of truth, and
only one run per session is in flight at a time. So each turn:

1. build a `*jess.Agent` from the resolved config,
2. `agent.NewSessionWithHistory(history)` seeded from `ChatStore.Snapshot(sessionKey)`,
3. `sess.Prompt(memory.WithSource(ctx, src), userText)`,
4. stream `run.Events()` to the `EventSink`, then `run.Wait()` for the result,
5. persist the new assistant/tool messages back to `ChatStore`,
6. discard the Session.

No session registry, eviction, or model-invalidation logic — `ChatStore` stays
the single source of truth, matching talon today. This is a talon implementation
detail; switching to long-lived sessions later needs zero jess changes.

### Rename (agentcore leaves talon's vocabulary)

- `internal/agentcore_chat` -> `internal/chatdriver` (package `chatdriver`).
- `server.AgentcoreRunFn` -> `server.ChatRunFn`; `AgentcoreRunResult` ->
  `ChatRunResult`; `AgentcoreUsage` -> `ChatUsage`; `ChatHandler().WithAgentcoreRunner`
  -> `WithChatRunner`; the handler field/`runStreamAgentcore` method renamed
  (`chat_agentcore.go` -> `chat_run.go`, `runStream`).
- `cmd/talon/gateway_agentcore.go` -> `cmd/talon/gateway_chat.go`
  (`buildAgentcoreRunner` -> `buildChatRunner`).
- The `ChatRunFn` SIGNATURE is unchanged (same inputs: agentID, sessionKey,
  runID, userText, selectedModelID, `[]ChatMessage`, the four emit callbacks;
  same outputs: FinalText, ModelID, `ChatUsage`, error). Only names change, so the
  RPC/server logic is untouched beyond the renames.

### Drop the test-only handler

`internal/agentcore_chat/handler.go` (the `Handler`/`RunRequest` self-contained
driver) and `handler_test.go` are deleted. It is not on the gateway path; tests
must exercise the real runner path, not a parallel driver. (Implementation
confirms no non-test caller before deleting.)

## Components

### 1. Agent construction (`chatdriver.Builder.BuildAgent`)

Reshape `BuildAgent(agentID)` to return `(*jess.Agent, ModelChoice, error)`:

- **Model:** reuse the existing `ResolveModel` / `ResolveProviderAuth` /
  `resolveModelMaxTokens`, then `jess.LiteLLM(choice.Provider, choice.Model,
  jess.WithLLMAPIKey(key), jess.WithLLMBaseURL(baseURL), jess.WithLLMMaxTokens(cap))`.
  `model_cap.go` is deleted (the cap now lives in `WithLLMMaxTokens`).
- **Tools** via `jess.WithTools(...)`:
  - filesystem tools from `agentcore/tools` (`NewRead/Write/Edit/Bash/Glob/Grep/Ls`)
    — they satisfy `jess/tool.Tool` structurally, so they pass straight through.
    talon keeps importing `agentcore/tools` (tool implementations only; it no
    longer drives `agentcore.Agent`).
  - memory tools: `memory.NewRememberTool` / `memory.NewRecallTool` (already
    `jess/tool.Tool`).
  - the onboarding tool, reimplemented as a `jess/tool.Tool` with a hand-built
    JSON-schema `map[string]any` (drops the `agentcore/schema` dependency).
  - existing per-agent tool filtering (`toolaccess.Resolve` / `filterTools`) is
    preserved, now over `[]tool.Tool`.
- **Memory:** `jess.WithMemory(store, recaller)` (replaces the
  `memory.NewContextManager` + `agentcore.WithContextManager` wiring). talon
  accepts jess defaults for `MaxItems`/`Header`/`Kinds` (the previous
  `ContextManagerOptions` fields). The important recall tuning lives on the
  `Recaller` talon constructs (min-score, stopwords, require-match) and is
  preserved. No new jess primitive is needed; if a real need for non-default
  injection config emerges later, add `jess.WithMemoryOptions(...)` then.
- **System prompt:** `jess.WithSystemPrompt(systemPrompt)`. `jess.WithAgentID(agentID)`
  scopes memory.

### 2. Event adapter (`chatdriver` jess `event.Event` -> `EventSink`)

Replaces the agentcore `EventAdapter`. Consumes `run.Events()` and calls the
existing 6-method `EventSink` (`Delta`/`Thinking`/`Final`/`ToolStart`/`ToolResult`/`Error`).
Mapping (jess `event.Event`):

- `KindMessageDelta` + `DeltaText` -> accumulate, `Delta(full, delta)`.
- `KindMessageDelta` + `DeltaThinking` -> accumulate, `Thinking(full, delta)`.
- `KindToolStart` -> `ToolStart(ToolCallID, Tool, Args)`.
- `KindToolEnd` -> `ToolResult(ToolCallID, Tool, Result, IsError)`.
- `KindError` -> `Error("agent", Err.Error())`.
- run completion: after the Events channel closes, `run.Wait()` gives the final
  text (assistant message) and `Result.Summary.Usage`; emit `Final(fullText)` and
  return `ChatUsage{Input, Output}` from `Summary.Usage`.

The adapter accumulates `fullText` itself (jess streams deltas; the `EventSink`
wants the running total), mirroring what the agentcore adapter did.

### 3. History conversion + persistence (`chatdriver`)

- **Seed:** `chatMessagesToJess([]server.ChatMessage) []message.Message` —
  user/assistant(+tool calls)/tool-result/system mapped to `message.Message`
  with `ContentBlock`s (`BlockText`, `BlockToolCall{ToolID,ToolName,Args}`,
  `BlockToolResult{ToolID,Result,IsError}`). Used by `NewSessionWithHistory`.
  Mirrors the deleted `agentcoreHistoryFromChatStore`, retargeted to jess types.
- **Persist:** after `run.Wait()`, append the run's new assistant message (and any
  tool-result messages) to `ChatStore` via the existing
  `AppendAssistantWithCalls` / `AppendToolResult`, converting `message.Message`
  back to `ChatMessage`. `ChatStore` remains the source of truth.

### 4. Provenance

`sess.Prompt(memory.WithSource(ctx, memory.Source{SessionID: sessionKey,
MessageID: runID, Tool: "remember", Reason: "model decided"}), userText)`. The
memory tools stamp writes from `SourceFromContext` (jess re-applies it onto the
tool ctx). Replaces talon's `memorySourceMiddleware`, which is deleted.

### 5. Gateway wiring (`cmd/talon/gateway_chat.go`)

`buildChatRunner(paths, mem) server.ChatRunFn` returns the per-turn closure
above. `cmd/talon/gateway.go` keeps `srv.ChatHandler().WithChatRunner(buildChatRunner(paths, mem))`
and the `WithMemory(mem)` sidecar wiring unchanged (memory construction in
`gateway_memory.go` is untouched — those memory APIs did not change).

## Out of scope / unchanged

- Memory construction (`gateway_memory.go`: chromem store, gomlx embedder,
  recallers) — memory APIs unchanged.
- The legacy non-agentcore provider path (`internal/server/chat_memory.go` and
  the `provider`-based runner) — separate from the agentcore/jess path; not
  touched beyond any rename fallout.
- The server RPC surface, `ChatStore`, the `EventSink` fan-out registry, auth,
  channels, plugins.
- jess itself (sub-project 1 already shipped the needed primitives).

## Testing

- **Unit (no network):** `chatMessagesToJess` round-trip over all roles incl.
  tool calls/results; the event adapter mapping (drive a fake `event.Event`
  sequence, assert `EventSink` calls); the runner against a fake `model.Model`
  (`model.Once` / a scripted streamer) asserting `ChatRunResult` (final text +
  usage) and that ChatStore is updated. `build_test.go` reshaped to assert
  `BuildAgent` returns a `*jess.Agent` wired with the expected tools.
- **Integration (tagged):** the existing `integration_test.go` reworked to drive
  the REAL runner (`ChatRunFn`) against a real provider, replacing its use of the
  deleted `Handler`. No test-only driver.
- `recall_floor_e2e_test.go` (memory recall) is unaffected (memory APIs unchanged).
- Gates: `make test` / `make build`; `go vet`.

## Consequences

- talon stops driving agentcore; agentcore survives only as filesystem-tool
  implementations passed through `jess.WithTools`. The agent loop, model dispatch,
  context injection, and event stream are all jess's.
- "agentcore" leaves talon's type/identifier vocabulary (rename), reducing
  coupling-by-naming to a hidden dependency.
- Token usage and per-run provenance now flow through jess's primitives rather
  than talon-side agentcore middleware.

## Risks

- The event-delta accumulation and final-message extraction must match the old
  adapter's behavior so the web UI transcript is unchanged; covered by the
  adapter unit test and a manual gateway smoke test.
- `agentcore/tools` filesystem tools must actually satisfy `jess/tool.Tool` at
  compile time; verified early in implementation (a one-line `var _ tool.Tool`
  assertion) before building the rest.
