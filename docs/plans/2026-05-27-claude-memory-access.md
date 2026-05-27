# Claude-memory access — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Status: Ready to implement (handoff)
Date: 2026-05-27
ADR: `docs/adr/0013-claude-memory-access.md`

**Goal:** Config-gated, read-only access for the talon agent to a Claude-memory dir — inject the `MEMORY.md` index (capped) into the system prompt and expose a path-confined `claude_memory` list/read tool.

**Architecture:** New `internal/claudemem` package (loader + tool, path-confined). `cmd/talon/gateway_memory.go` resolves config → `buildClaudeMemory`. `internal/agentcore_chat.Builder.WithClaudeMemory` injects the index into the system prompt and registers the tool (subject to existing `toolaccess` filtering).

**Tech Stack:** Go, agentcore `Tool` interface, `internal/talonconfig` native config, `internal/agentcore_chat` builder.

---

## Context (verified)

- System prompt built at `internal/agentcore_chat/build.go:169` `systemPrompt := buildSystemPrompt(b.merged, agentID, personaDir)`; applied at `:230` `agentcore.WithSystemPrompt(systemPrompt)`.
- Tools appended to `toolSet` (build.go ~201) then filtered by `toolaccess.Resolve` (~207) — register the new tool before that filter.
- Config gating pattern: `cmd/talon/gateway_memory.go` `buildMemorySidecar`/`readMemorySettings` + the `MemoryRecallConfig` round-trip in `internal/talonconfig/native.go`.
- An agentcore `Tool` implements `Name() / Description() / Schema() / Run(ctx, input)` (see jess `RememberTool` / `internal/tools/*` for the shape).

## File structure

| File | Responsibility |
|---|---|
| `internal/claudemem/claudemem.go` | loader (read+cap MEMORY.md), path-confined file read, slug listing |
| `internal/claudemem/tool.go` | `claude_memory` agentcore.Tool (list/read) |
| `internal/claudemem/*_test.go` | unit tests incl. path-traversal rejection + cap |
| `internal/talonconfig/native.go` | `memory.claude.{enabled,path,inject,max_inject_bytes}` round-trip |
| `cmd/talon/gateway_memory.go` | `buildClaudeMemory(paths)` + settings read |
| `cmd/talon/gateway.go` (or chat wiring) | pass index+tool into the builder |
| `internal/agentcore_chat/build.go` | `WithClaudeMemory`; inject index into system prompt; register tool |
| `docs/dependencies.md` | short note |

---

## Task 1: `internal/claudemem` loader + path confinement

**Files:** Create `internal/claudemem/claudemem.go`, `internal/claudemem/claudemem_test.go`

Interface:
```go
package claudemem

// Store is a read-only, path-confined view of a Claude memory dir.
type Store struct{ dir string }

func New(dir string) (*Store, error) // err if dir missing/not a dir

// Index returns MEMORY.md content capped to maxBytes (0 = uncapped). On
// overflow it truncates on a line boundary and appends a marker.
func (s *Store) Index(maxBytes int) (string, error)

// List returns the memory slugs (filenames without .md, excluding MEMORY.md).
func (s *Store) List() ([]string, error)

// Read returns the content of <slug>.md, path-confined to dir. Rejects slugs
// containing '/', '\\', '..', or that resolve outside dir. maxBytes bounds output.
func (s *Store) Read(slug string, maxBytes int) (string, error)
```

- [ ] **Step 1: Failing tests** in `claudemem_test.go` (temp dir with `MEMORY.md` + `feedback_x.md`):
```go
func TestStore_IndexCapTruncates(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": strings.Repeat("- line\n", 1000)})
	s, _ := New(d)
	got, _ := s.Index(100)
	if len(got) > 200 || !strings.Contains(got, "claude_memory") { t.Fatalf("not capped/marked: %d", len(got)) }
}
func TestStore_ReadRejectsTraversal(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": "x", "feedback_x.md": "secret-ok body"})
	s, _ := New(d)
	if _, err := s.Read("../../etc/passwd", 4096); err == nil { t.Fatal("traversal allowed") }
	if _, err := s.Read("feedback_x", 4096); err != nil { t.Fatalf("legit read failed: %v", err) }
}
func TestStore_ListExcludesIndex(t *testing.T) {
	d := seed(t, map[string]string{"MEMORY.md": "x", "feedback_x.md": "y", "project_z.md": "z"})
	s, _ := New(d); got, _ := s.List()
	// want [feedback_x project_z], not MEMORY
}
```
- [ ] **Step 2: Run, verify fail** (`go test ./internal/claudemem/`).
- [ ] **Step 3: Implement** `claudemem.go`. `New` expands `~`, `filepath.Abs`, `os.Stat` is-dir. `Read`: reject slug with `/`,`\`,`..`; `p := filepath.Join(dir, slug+".md")`; re-check `filepath.Clean(p)` has `dir+separator` prefix (belt-and-suspenders); read + bound. `Index`/`Read` truncate on the last newline ≤ maxBytes and append `"\n…(truncated — use the claude_memory tool to read full entries)"`.
- [ ] **Step 4: Run, pass. Commit** (`claudemem: read-only, path-confined Claude-memory store`).

## Task 2: `claude_memory` tool

**Files:** Create `internal/claudemem/tool.go`, `internal/claudemem/tool_test.go`

- [ ] **Step 1: Failing test** — the tool's `Run` lists when `op=list`, reads when `op=read,slug=…`, errors on traversal:
```go
func TestTool_ListAndRead(t *testing.T) {
	s, _ := New(seed(t, map[string]string{"MEMORY.md":"i","feedback_x.md":"body-x"}))
	tl := NewTool(s, 4096)
	out, err := tl.Run(context.Background(), json.RawMessage(`{"op":"read","slug":"feedback_x"}`))
	if err != nil || !strings.Contains(out, "body-x") { t.Fatalf("read: %v %q", err, out) }
	lst, _ := tl.Run(context.Background(), json.RawMessage(`{"op":"list"}`))
	if !strings.Contains(lst, "feedback_x") { t.Fatalf("list: %q", lst) }
}
```
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** `tool.go`: `NewTool(s *Store, maxRead int) agentcore.Tool`. `Name()="claude_memory"`, description explains read-only access to Claude's saved notes, `Schema()` = `{op: "list"|"read", slug?: string}`. `Run` dispatches to `s.List()`/`s.Read(slug, maxRead)`; a read error (incl. traversal) returns a tool-level error string (the agent sees it; no panic). Confine via the Store (Task 1).
- [ ] **Step 4: Run, pass. Commit** (`claudemem: claude_memory list/read agentcore tool`).

## Task 3: config round-trip

**Files:** `internal/talonconfig/native.go`; test mirrors existing config round-trip tests

- [ ] **Step 1: Failing round-trip test** asserting `memory.claude.enabled`/`path`/`inject`/`max_inject_bytes` survive `config.Set`→read.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** a `ClaudeMemoryConfig` (Enabled `*bool` default-false-when-nil; Path string; Inject `*bool` default-true-when-nil; MaxInjectBytes int) wired through the struct + `decodeViper`/`*FromJSON`/`applyRuntime`/`MarshalTOML` like `MemoryRecallConfig` (real `mapstructure` tags). Runtime JSON path `memory.claude.*`.
- [ ] **Step 4: Run, pass. Commit** (`config: memory.claude.* (claude-memory access) round-trip`).

## Task 4: gateway wiring

**Files:** `cmd/talon/gateway_memory.go` (build), `cmd/talon/gateway.go`/chat wiring (inject into builder)

- [ ] **Step 1:** `buildClaudeMemory(paths) (index string, tool agentcore.Tool, ok bool)`: read settings; if `!enabled` return `ok=false`; if `path==""` log a warning + return false; `claudemem.New(path)`; `index = ""` when `inject` false, else `store.Index(maxInjectBytes)`; `tool = claudemem.NewTool(store, maxRead)`; return `..., true`. Resolve the same way `buildMemorySidecar` reads native config.
- [ ] **Step 2:** Where the agentcore `Builder` is constructed for a run, call `buildClaudeMemory` and, when ok, `builder.WithClaudeMemory(index, tool)`.
- [ ] **Step 3:** `go build ./...`. **Commit** (`gateway: wire claude-memory (buildClaudeMemory + builder)`).

## Task 5: builder injection + tool registration

**Files:** `internal/agentcore_chat/build.go`; `internal/agentcore_chat/build_test.go`

- [ ] **Step 1: Failing test** — a builder `WithClaudeMemory("INDEX-MARKER", fakeTool)` produces a system prompt containing `INDEX-MARKER` and a tool set containing `claude_memory` (subject to tool access). Drive `BuildAgent`/the system-prompt path with a fake tool.
- [ ] **Step 2: Run, verify fail** (`WithClaudeMemory` undefined).
- [ ] **Step 3: Implement.** Add fields `claudeIndex string`, `claudeTool agentcore.Tool` + `func (b *Builder) WithClaudeMemory(index string, tool agentcore.Tool) *Builder`. After `systemPrompt := buildSystemPrompt(...)` (build.go:169), if `b.claudeIndex != ""` append a labeled section: `systemPrompt += "\n\n## Notes Claude has saved about the user/project\n\n" + b.claudeIndex`. In the tool-append block (~201), if `b.claudeTool != nil` `toolSet = append(toolSet, b.claudeTool)` (before the `toolaccess.Resolve` filter so policy applies).
- [ ] **Step 4: Run, pass. Commit** (`agentcore_chat: inject claude-memory index + register tool`).

## Task 6: CLI reachability + docs

**Files:** `docs/dependencies.md` (or architecture note); confirm config-set path

- [ ] **Step 1:** Confirm reachable: build, then against a temp `TALON_STATE_DIR`, `talon config set memory.claude.enabled true`, `talon config set memory.claude.path <dir>`, `talon config get memory.claude.path` round-trips. (A `talon configure` step is a follow-up; non-secret config is reachable via `config set`.)
- [ ] **Step 2:** Doc note in `docs/dependencies.md` (or `docs/architecture.md`): the feature, config keys, hybrid access, path confinement, read-only, default off.
- [ ] **Step 3: Commit.**

---

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/claudemem/... ./internal/talonconfig/... ./internal/agentcore_chat/... ./cmd/talon/...
golangci-lint run ./...   # gate must stay green
```
Manual: point `memory.claude.path` at `~/.claude/projects/<slug>/memory`, `memory.claude.enabled true`, run a chat turn and confirm the agent references the saved notes; ask it to read a specific memory and confirm the `claude_memory` tool fires; try `read ../../etc/passwd` semantics in a test to confirm rejection.

## Follow-ups (file as beads issues)
- `talon configure` wizard step for claude-memory (path picker that defaults to the detected project slug).
- Model-aware injection fallback (skip inject when the active model's context window is below a threshold).
- Optional: detect the project's Claude slug automatically instead of an explicit path.
