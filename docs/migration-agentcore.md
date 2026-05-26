# Migration: parallel chat stack → agentcore-based

## Why

talon currently carries a parallel stack that reimplements primitives
already shipped by [`agentcore`](https://github.com/voocel/agentcore) and
[`jess`](https://github.com/guygrigsby/jess):

| Talon legacy                                        | Upstream equivalent                       |
|-----------------------------------------------------|-------------------------------------------|
| `internal/provider/{provider.go,openai,anthropic,deepseek}` | `agentcore/llm` (via LiteLLM)             |
| `internal/server/chat.go` per-iteration loop        | `agentcore.Agent` + `agentcore.AgentLoop` |
| `internal/plugins/openaicompat`, `internal/plugins/anthropic` | `agentcore/llm.NewModel(provider, model, ...)` — one upstream covers them all |
| `internal/server/chat_memory.go` inline recall      | `jess/memory.ContextManager`              |
| `internal/tools/{read,write,edit,bash,glob,grep}`   | `agentcore/tools` (read/write/edit/bash exist upstream; glob/grep are talon-specific and stay) |
| `internal/tools/subagent.go`                        | `agentcore/subagent`                      |
| The `ListProviderModels` plugin RPC (added 2026-05-25, then disconnected from the picker on 2026-05-26) | not needed — agentcore doesn't enumerate; the picker is config-driven |

This fork increased over time without an explicit decision. The decision now
is to converge. New work targets agentcore directly.

## Phased approach

Big-bang replacement is the wrong shape — too many tests, FE flows, and
side-channels (channels, sessions, cost cap) touch the chat path. The plan
is staged so each commit leaves the tree green and reversible.

### Phase 0 — docs (done)

This file + `docs/dependencies.md` + the CLAUDE.md rewrite. No code change.

### Status snapshot (updated 2026-05-25)

| Phase | State |
|---|---|
| 0 — docs | ✅ done |
| 1 — agentcore-based chat handler scaffold | ✅ done. `internal/agentcore_chat/` with 30+ unit tests. |
| 1.5 — jess memory + agentcore builtin tools | ✅ done. `Builder.WithMemory` wires jess RememberTool/RecallTool when the gateway has the memory sidecar. agentcore/tools Read/Write/Edit/Bash/Glob/Grep/Ls attached automatically. |
| 2 — integration tests | ⚠️ 2/3 passing. openai/gpt-4o-mini ✅ (~1s TTFB). deepseek/deepseek-chat ✅ (~1.3s TTFB, streams thinking deltas). anthropic ❌ — blocked on upstream LiteLLM issue (auto-fills top_p=1.0, Anthropic rejects when temperature is also set). |
| 3 — gateway dispatch behind flag | ✅ done. `chat.handler: "agentcore"` in merged config routes through the new handler. `WithPaths` + `WithAgentcoreRunner` on `ChatHandler`. |
| 4 — delete legacy stack | 🚫 **blocked** by Phase 2 anthropic failure. Until LiteLLM ships a top_p fix (or talon adds a deeper wrapper), the anthropic plugin remains required. |
| 5 — followups | open: per-session model override + cost-cap Record() + sinks fan-out integration in the agentcore path; FE wire-shape Playwright verification; `client.id` rename. |

**Anthropic blocker detail:** `agentcore/llm/litellm.go`'s `Generate`/`GenerateStream` doesn't set `req.TopP`, so it's nil when reaching `litellm.Client.applyChatDefaults` at `client_runtime.go:81-84`, which fills it with `c.defaults.TopP = 1.0`. The Anthropic provider then includes both `temperature` and `top_p` in the outgoing request, and Anthropic 400s with "cannot both be specified for this model." Two-line upstream fix in `litellm/providers/anthropic.go` (drop top_p when temperature is present) or in `agentcore/llm/litellm.go` (expose a no-top_p option). Talon convention says contribute upstream.

### Phase 1 — agentcore-based chat handler scaffold

Add `internal/agentcore_chat/` (or similar) that:
- Builds an `agentcore.Agent` from talon's merged config: model picked
  from `agents.<id>.model` (or `agents.defaults.model.primary`), API key
  resolved from `plugins.entries.openai-compat.config.providers.<name>.
  apiKey` (and similar) via the existing secrets resolver.
- Subscribes to `agentcore.Event` and translates events into talon's
  existing `chat.event` wire frames (`MessageDelta` → text event, etc.).
- Tools registered: jess `RememberTool` + `RecallTool` (when memory is
  enabled), `agentcore/tools.{Read,Write,Edit,Bash}`, talon's custom
  `Glob`/`Grep`/`Agents` wrapped as `agentcore.Tool`.
- ContextManager: jess `memory.NewContextManager(...)` when memory is on,
  otherwise `agentcore/context`'s default.

Feature-flagged via `chat.handler` config key: `"legacy"` (default) keeps
the current path; `"agentcore"` uses the new handler. Lets tests run both
in parallel during cutover.

### Phase 2 — integration tests

`internal/agentcore_chat/integration_test.go` exercises:
- One probe chat per configured model in `models.providers.<name>.models[]`.
- Tool dispatch (read/write/edit/bash on a tmp workspace).
- Memory recall (jess store, `RememberTool` then re-prompt verifies recall).
- Subagent invocation (single + parallel modes).
- Streaming throughput (measure tokens/sec).

Per the test-before-done feedback rule, these are mandatory before any
later phase claims success.

### Phase 3 — wire to gateway, flip the flag

`agentProviderFactory` in `cmd/talon/gateway_chat.go` becomes a thin shim
that picks the handler based on `chat.handler`. Default flips to
`agentcore` once Phase 2 is green for every configured model.

### Phase 4 — delete

In one commit (or a small commit cluster), remove:

- `internal/provider/{provider.go,openai/,anthropic/,deepseek/,stub.go}`
- `internal/plugins/{openaicompat/,anthropic/}`
- `internal/server/chat_memory.go` (the recall logic moves to jess CM)
- The chat loop body in `internal/server/chat.go` — keep the handler
  shell as the registry adapter that calls into the agentcore-based
  layer.
- `pluginConstructors` entries for `openai-compat` and `anthropic`
- `builtinPlugins` entries for the same
- Tests for the deleted paths
- `proto/plugin.proto` `ListProviderModels` RPC + `ModelDescriptor` (the
  whole discovery RPC family becomes dead code once provider dispatch
  moves to agentcore)

### Phase 5 — followups (separate commits)

- Drop the `~/.openclaw` overlay layer entirely (it's a migration artifact).
  Pure-talon installs already work with just `~/.talon`. Add a one-shot
  `talon migrate` command that copies legacy state, then remove the
  read-side merge.
- Audit code comments + log messages for stale "openclaw" mentions.
- The `client.id = "openclaw-tui"` hardcode in `internal/gateway/client.go`
  was for upstream openclaw acceptance; can become `"talon"` once we're
  fully standalone.

## Inverse case: when NOT to remove a piece

A talon-only behavior that doesn't fit upstream's primitives stays. The
list as of this writing:

- **Per-agent daily USD cap** (`internal/server/cost_tracker.go`) — agentcore
  doesn't enforce cost caps. talon's hook stays around the agentcore call.
- **Per-session model override + alias** (`SessionStore.SetModel`) — talon
  picks the model before constructing the `agentcore.Agent` instance; not
  an agentcore concern.
- **Channels** (Telegram, BlueBubbles, IRC, Discord, etc.) — these are
  talon's plugin domain, separate from the chat loop.
- **Workspace confinement** (`resolveInWorkspace`) — talon's tool registry
  enforces this; agentcore's tools take a root path and don't escape.
- **Subagent depth limit, exec approval, identity** — talon-side policy
  hooks. Wired as `agentcore.ToolGate` adapters.

## Risk register

- **Test coverage shifts.** Phase 4 deletes a lot of tests. Phase 2's
  integration suite has to cover the surface area meaningfully before any
  delete lands. Don't delete and trust unit tests alone.
- **FE wire shape.** `chat.event` payloads are consumed by the SvelteKit
  composer. agentcore's event types differ from talon's; the adapter in
  Phase 1 must match the FE's expectations exactly. Test against the real
  FE (browser session or Playwright) before flipping default.
- **Cost cap interaction.** The cap runs around the chat call. Phase 1's
  adapter needs to invoke it at the right point — before each agent step,
  not just at request entry.
- **Plugin process loss.** Removing the openai-compat and anthropic plugin
  subprocesses means the corresponding process boundary disappears.
  Anything that relied on isolation (it shouldn't — providers don't run
  user code) is now in-process.
