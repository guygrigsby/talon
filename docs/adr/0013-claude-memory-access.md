# 0013 Claude-memory access for the talon agent

Status: Accepted

Date: 2026-05-27

## Context

Claude Code keeps a persistent, file-based memory about the user and project at
`~/.claude/projects/<slug>/memory/` — a `MEMORY.md` index plus per-fact
`feedback_*`/`project_*`/`user_*` files. The talon chat agent has no awareness
of any of it. Giving the agent read access to that memory lets it act on what
Claude already knows (preferences, project constraints, conventions) without the
user re-explaining.

This is a talon-only, personal feature. It is **not** a jess concern (jess owns
the agent's *own* durable memory; this is a read-only *external* source), and it
is gated by config (default off).

## Decision

Add a config-gated, read-only bridge from the talon agent to a Claude-memory
directory, with a hybrid access model.

### Config (native, default off; mirrors the `memory.*` gating)

- `memory.claude.enabled` — bool, default `false`.
- `memory.claude.path` — the Claude memory dir to read. Explicit (no
  path-guessing); if enabled but unset, the feature stays inert and logs a
  warning.
- `memory.claude.inject` — bool, default `true`. False = tool-only (escape
  hatch for tiny-context models).
- `memory.claude.max_inject_bytes` — default 4096. Caps the injected index.

### Hybrid access

1. **Injected index.** At system-prompt build time, read `MEMORY.md`, cap to
   `max_inject_bytes`, and fold it into the system prompt under a labeled
   section. Read fresh each turn (it's ~2KB; no cache, always current). On
   overflow, truncate with a "use the claude_memory tool for the rest" marker.
   The index is ~600 tokens today — trivial for any model — and the cap keeps it
   bounded as memories accumulate, sidestepping per-model context-size concerns
   without per-model logic.
2. **Read-only tool** `claude_memory`. `list` → memory slugs + one-line
   descriptions; `read <slug>` → that file's full content, output-bounded. The
   tool is **path-confined** to the configured dir: slugs are resolved against
   it and `..`/absolute paths are rejected, so the agent cannot read arbitrary
   files through it. This is the agent's pressure valve for detail.

### Wiring

`buildClaudeMemory(paths) (indexText string, tool agentcore.Tool, ok bool)` in
`cmd/talon/gateway_memory.go` (parallel to `buildMemorySidecar`) reads the
config and resolves the dir. The `internal/agentcore_chat` `Builder` gains
`WithClaudeMemory(indexText, tool)`: it appends the index to the system prompt
(in/after `buildSystemPrompt`, before `agentcore.WithSystemPrompt`) and appends
the tool to `toolSet` *before* `toolaccess.Resolve` filtering — so the existing
per-agent tool-access policy governs it like any other tool. The loader + tool
live in a new `internal/claudemem` package (one clear responsibility:
read-only, path-confined access to a memory dir).

### Safety

Read-only throughout; path-confined tool; bounded index + tool output. The
memory content is sent to whatever provider the session uses — acceptable for a
personal, opt-in feature, and memories shouldn't hold secrets by convention.

## Consequences

- The agent can be aware of, and pull, Claude's notes — gated, off by default,
  bounded footprint.
- Reachable from the CLI via `talon config set memory.claude.enabled true` +
  `…path …` (a non-secret scalar, so `config set` satisfies the reachability
  rule); a short `talon configure` step is a nice-to-have.
- New `internal/claudemem` package; small additions to native config, the
  builder, and the gateway wiring. No jess involvement.
- Out of scope (v1): model-aware injection fallback, writing to Claude memory,
  file-watching, cross-project / global `CLAUDE.md`, search/embeddings over the
  memories.
