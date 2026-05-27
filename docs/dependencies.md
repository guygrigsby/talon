# Major dependencies

talon's runtime is built on three upstream Go libraries plus one
transitive provider abstraction. Each has a distinct role. Loading
them in your head saves you from reinventing their primitives inside
talon — which is exactly what the codebase has been doing in places
and is being walked back from.

## agentcore — the agent loop

`github.com/voocel/agentcore`

### What it is

A minimal, composable Go agent library. Stable surface:

- `agentcore.Agent` — stateful agent. Holds the system prompt, tool
  set, model binding, and an event listener registry. Methods:
  `Prompt(text)`, `Subscribe(fn)`, `WaitForIdle()`, etc.
- `agentcore.AgentLoop` — the free-function loop kernel. Inner loop
  processes tool calls + steering; outer loop handles follow-up
  turns. `agent.go` is the sole consumer; you almost never call the
  loop directly.
- `agentcore.Event` — single `<-chan Event` output drives every UI
  surface (TUI, web, Slack, logging). Event kinds: `MessageStart`,
  `MessageDelta`, `MessageEnd`, `ToolCallStart`, `ToolCallEnd`,
  `Thinking`, etc.
- `agentcore.Tool` — interface every callable tool implements.
  `Name()`, `Description()`, `Schema()`, `Run(ctx, input)`.
- `agentcore.Message` — conversation turn. Roles: user, assistant,
  system, tool. Content blocks support text + tool_use + tool_result.

Subpackages we lean on:

- `agentcore/llm` — model adapters. `llm.NewModel("openai",
  "gpt-5.4-mini", llm.WithAPIKey(...))` returns a `ChatModel`.
  Provider plumbing routes through LiteLLM (see below); this is one
  upstream covering openai/anthropic/deepseek/mistral/google/groq/etc.
- `agentcore/tools` — built-in tools: `Read`, `Write`, `Edit`, `Bash`.
  All workspace-scoped with read-before-write enforcement.
- `agentcore/context` — default `ContextManager`. Projects history
  to wire-shape messages, handles overflow recovery, estimates token
  usage. Pluggable; jess swaps in its memory-aware variant.
- `agentcore/subagent` — `SubAgent` tool. Four modes: single,
  parallel, chain, background.
- `agentcore/task` — background task registry. Shared by bash long-
  running output streams and subagent background mode.
- `agentcore/permission` — optional gate engine. We use the `ToolGate`
  hook to enforce exec-approval policies before any tool runs.

### What we use it for

Everything in the chat path. The agent loop, provider dispatch, tool
calling, streaming events, context management.

Old talon code that mirrors agentcore primitives is in the process of
being deleted (`internal/provider/*`, `internal/server/chat.go`'s
loop, plugin shims). See `docs/migration-agentcore.md`.

### What's NOT in scope here

- Channel-side I/O (Telegram, BlueBubbles, Discord). Those stay as
  talon plugins talking to platform APIs; only the *chat* path
  through `chat.send` routes through agentcore.
- Per-session prefs (model override, alias), agent identity, daily
  USD cap, the WebSocket framing. Those are talon-level concerns.
- Memory storage backends — that's jess (and below).

## jess — memory + skills on top of agentcore

`github.com/guygrigsby/jess`

### What it is

> "Memory and skills for agentcore-based Go agents. The leather strap
> on the falcon's leg."

Two extension packages:

- **`jess/memory`** — durable agent memory. Typed `Kind` (user,
  feedback, project, reference) with per-kind retrieval policy.
  Pluggable `Store` (in-memory, JSONL, chromem-go vector — we use
  chromem). Pure-Go embedder via GoMLX + sentence-transformers (no
  CGO). Recallers: `SimpleRecaller` (token overlap), `VectorRecaller`
  (cosine sim), fused via `HybridRecaller` (reciprocal rank fusion).
  Tools the model can call: `RememberTool` (save) + `RecallTool`
  (query). `ContextManager` adapter injects layered memory (always-on
  core + relevance-recalled) into every LLM call.
  `VectorRecaller` carries an absolute cosine **relevance floor**
  (`memory.WithMinScore`): vector hits below it are dropped before
  `HybridRecaller`'s RRF fusion, so off-topic memories don't ride
  along on a low-similarity match (ADR 0010). talon sets the floor
  from `memory.recall.min_score` (default `0.30` for MiniLM-L6-v2
  cosine when unset); `talon config set memory.recall.min_score <f>`
  tunes it, and the gateway must restart to apply (startup-read).

- **`jess/skills`** — registerable capability bundles. A `Skill` is
  a name, description, system-prompt contribution, and zero-or-more
  `agentcore.Tool` implementations. Loads from disk (Claude-Code
  skill layout) or by direct registration.

### What we use it for

- `jess/memory` is the durable memory layer for every agent.
  `memory.NewChromemStore` backs to `~/.talon/memory/<agent-id>/`
  via chromem-go. The `RememberTool` and `RecallTool` register as
  agentcore tools.
- jess's `ContextManager` is the prompt assembler. Old talon had
  inline memory recall logic in `internal/server/chat_memory.go`;
  the migration removed that recall assembly and uses jess's CM
  directly. The remaining server file is only a temporary adapter
  for the direct provider loop.
- `jess/skills` is unused today. Candidate for adopting once we want
  to expose user-authored skills like Claude Code does.

### What's NOT in scope here

- Memory tools for sessions where `memory.enabled` is unset — jess
  is wired only when memory is on.
- Per-session memory scoping that doesn't fit jess's `Kind` model —
  talon's session-prefs store stays separate.

## chromem-go — embeddable vector DB

`github.com/philippgille/chromem-go`

### What it is

> "Embeddable vector database for Go with Chroma-like interface and
> zero third-party dependencies. In-memory with optional persistence."

A SQLite-style local vector DB for RAG. Not a Chroma client; not a
service. Pure Go, no CGO. Benchmarks: 0.3 ms / 1k docs, 40 ms / 100k
docs on a mid-range laptop CPU. Persistence is just a directory of
JSON gob files.

### What we use it for

The persistence backing for jess `memory.NewChromemStore`. Each agent
gets a chromem collection at `~/.talon/memory/<agent-id>/`. Vector
search is local; no network call for memory lookup.

The embedder is jess's GoMLX MiniLM-L6-v2, not chromem's built-in
options. chromem just stores vectors+payloads and runs cosine search.

### What's NOT in scope here

- Cross-agent memory sharing. Each chromem collection is per-agent.
- Anything other than vector search. If you need keyword search,
  that's the `SimpleRecaller` (token overlap), composed via
  `HybridRecaller`.

## LiteLLM (Go) — provider abstraction, via agentcore

`github.com/voocel/litellm`

### What it is

A Go port (by the agentcore author) of the LiteLLM model unification
layer. One client interface, dozens of providers (OpenAI, Anthropic,
DeepSeek, Mistral, Google, Groq, Cohere, Together, Fireworks,
OpenRouter, Ollama, LM Studio, vLLM, …). Handles per-provider
wire-shape quirks: Anthropic's content-block messages, OpenAI's
streaming SSE, DeepSeek's `reasoning_content`, etc.

### What we use it for

We don't reach for LiteLLM directly. It's a transitive dep of
agentcore — `agentcore/llm` builds on it. When we say
`llm.NewModel("anthropic", "claude-opus-4-7", llm.WithAPIKey(k))`,
the underlying provider implementation is LiteLLM's.

If you encounter a per-provider issue (e.g. deepseek reasoning_content
roundtrip), the fix probably lives in LiteLLM upstream. Contribute
there per the talon convention; don't fork into internal/provider/.

### What's NOT in scope here

- Auth resolution. We resolve API keys from talon's `op://` /
  `keychain://` refs and pass the literal string to
  `llm.WithAPIKey`. LiteLLM doesn't know about secret references.
- Discovery (`/v1/models`). LiteLLM doesn't enumerate models for us;
  the picker is config-driven (`models.providers.<name>.models[]`).

## tailscale.com (tsnet) — embedded tailnet node

`tailscale.com` (tsnet)

### What it is

The official Tailscale Go module. We use the `tsnet` subpackage: an
embeddable, userspace tailnet node. A Go program registers as its own
machine on the tailnet and advertises a Tailscale Service (VIPService)
in-process via `Server.ListenService`, with no system `tailscaled`, no
CLI, no config files. BSD-3-Clause, so license-clean under the
no-GPL/AGPL rule. Heavyweight (pulls gvisor + a netstack), accepted as
the cost of a self-contained node.

### What we use it for

Selected by `gateway.bind=tailnet` (ADR 0008). Two in-tree pieces:

- `internal/tailnet` — runtime. Brings up the tsnet node (state under
  `~/.talon/tailscale/`), registers from the OAuth client secret +
  `AdvertiseTags`, advertises `svc:talon`, and hands the resulting
  listener (with its `FQDN`) to the gateway mux via
  `server.RunListener`. The stable URL is `https://talon.<tailnet>.ts.net`.
- `internal/tailscale` — provision-time REST v2 client (hand-rolled, not
  tsnet): OAuth token exchange, read the tailnet MagicDNS name, create
  the VIPService. Driven by the `talon configure tailscale` wizard.

Distinct from the legacy `tailscale serve` wrapper (`cmd/talon/tailscale.go`,
`gateway.tailscale.mode=serve|funnel`), which shells out to a
user-managed `tailscaled`. The wrapper exposes the gateway at the host
machine's name; the tsnet bind gives a host-independent service URL.

### What's NOT in scope here

- Auto-editing the tailnet policy. The wizard prints the ACL grant and
  the user pastes it (ADR 0008: print-and-confirm, never silent).
- Funnel, multi-backend service load-balancing, and `trusted-proxy`
  identity mapping via tsnet `WhoIs` — documented follow-ups, not v1.
- Talon's token auth is unchanged: it stays on as defense-in-depth even
  though Tailscale authenticates at the network layer.

## How they compose

```
                  ┌──────────────────────────────┐
                  │  talon-gateway (this repo)   │
                  │  WS framing, channels, FE    │
                  └──────────────┬───────────────┘
                                 │
                                 ▼
                  ┌──────────────────────────────┐
                  │   agentcore.Agent (loop)     │
                  │   subscribe → chat.event     │
                  └──────┬───────────────┬───────┘
                         │               │
                         ▼               ▼
              ┌───────────────────┐  ┌──────────────────┐
              │ agentcore.Tool    │  │ ChatModel        │
              │ (read/write/edit/ │  │ via agentcore/llm│
              │  bash/subagent/   │  │  → LiteLLM       │
              │  remember/recall) │  │  → provider HTTP │
              └────────┬──────────┘  └──────────────────┘
                       │
                       ▼
              ┌───────────────────┐
              │ jess/memory       │  ⇄  chromem-go store
              │ ContextManager    │     (vector + JSONL)
              │ RememberTool      │
              │ RecallTool        │
              └───────────────────┘
```

talon's job in this picture: assemble the agent (model + tools +
system prompt + context manager from merged config), translate
agentcore events to talon's wire shape (`chat.event` JSON frames over
WebSocket), and own platform-side concerns (channels, session prefs,
identity, cost cap, FE).
