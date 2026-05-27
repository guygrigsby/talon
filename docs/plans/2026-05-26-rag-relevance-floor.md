# RAG recall relevance floor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Status: Ready to implement (handoff)
Date: 2026-05-26
ADR: `docs/adr/0009-memory-recall-relevance-floor.md`

**Goal:** Stop the agent from injecting irrelevant memories (e.g. "pizza" on "Oh hi!") by gating vector recall with an absolute cosine relevance floor.

**Architecture:** The fix is upstream in jess: thread chromem's cosine similarity onto `Entry.Score`, then drop sub-floor entries inside `VectorRecaller` (before `HybridRecaller`'s RRF fusion). talon bumps the jess dep, adds a config knob, and passes the floor into `NewVectorRecaller`.

**Tech Stack:** Go; `github.com/guygrigsby/jess` `memory` package; chromem-go; talon `cmd/talon` + `internal/config`.

---

## Context (verified)

- jess @ `v0.0.0-20260525040818-2bdfd912fe1c`. Recall has NO relevance gate; chromem's cosine score is discarded in `SearchVector`.
- `memory/store_chromem.go:195` `SearchVector`: loops `results` from `QueryEmbedding`, builds entries via `metadataToEntry(r.ID, r.Metadata, r.Content)` at line 223 — never reads `r.Similarity`.
- `memory/recall_vector.go:39` `VectorRecaller.Recall(ctx, store, agentID, hint, max)`; `:92` `HybridRecaller.Recall` fuses VectorRecaller + SimpleRecaller via RRF.
- `memory/memory.go` `type Entry struct` (fields ID, Kind, AgentID, Text, Tags, Key, …); `:137` `type Recaller interface`.
- `memory/context_manager.go:68` `NewContextManager(store, recaller, opts ContextManagerOptions)`; K default 8 at `:82`; unconditional injection `:111-126`.
- talon wiring `cmd/talon/gateway_memory.go:63` `NewChromemStore`, `:75` `NewHybridRecaller(NewVectorRecaller(), NewSimpleRecaller())`, `:82` `ContextManagerOptions`.

## Cross-repo workflow

The jess change (Part A) ships first as a jess PR. Develop it against a local jess checkout wired into talon via a temporary `replace` directive, so the talon changes (Part B) can be built + tested end to end before jess is published. Final talon commit drops the `replace` and bumps to the published jess version.

- [ ] **Setup: local jess + replace directive**

```bash
# clone jess next to talon (one level up from the talon worktree's repo root)
git -C "$(git rev-parse --show-toplevel)/../.." clone git@github.com:guygrigsby/jess.git 2>/dev/null || true
# in the talon worktree, point at it for development (DO NOT COMMIT THIS until the end)
go mod edit -replace github.com/guygrigsby/jess=../../../jess
go mod tidy
```
Note the exact relative path to the jess clone and adjust. Verify `go build ./...` still works (with the embed placeholder: `mkdir -p web/build && touch web/build/.gitkeep`).

---

# Part A — jess (separate PR in the jess repo)

Work in the jess clone. Branch `feat/recall-relevance-floor`.

## Task A1: thread cosine score onto Entry

**Files:** `memory/memory.go` (Entry), `memory/store_chromem.go:195-230` (SearchVector), `memory/vector_test.go`

- [ ] **Step 1: Failing test** in `memory/vector_test.go`: append two entries, query with a vector close to one; assert the returned entries carry a non-zero `Score` and that the closer entry's `Score` is higher.
```go
func TestSearchVector_PopulatesScore(t *testing.T) {
	st := newTestChromemStore(t) // reuse existing test helper
	mustAppend(t, st, Entry{Text: "user likes spicy pizza", AgentID: "main"})
	mustAppend(t, st, Entry{Text: "deploy runs on fridays", AgentID: "main"})
	vec := mustEmbed(t, "what food does the user like")
	got, err := st.SearchVector(context.Background(), vec, 2, Query{AgentID: "main"})
	if err != nil { t.Fatal(err) }
	if len(got) == 0 || got[0].Score == 0 { t.Fatalf("score not threaded: %+v", got) }
	if len(got) == 2 && got[0].Score < got[1].Score { t.Fatalf("not sorted by score") }
}
```
- [ ] **Step 2: Run, verify fail** — `go test ./memory/ -run TestSearchVector_PopulatesScore -v` → FAIL (`Score` undefined / zero).
- [ ] **Step 3: Add `Score float32` to `Entry`** in `memory/memory.go` with a doc comment: "Score is the recall relevance (cosine similarity) on the vector path; 0 on non-vector recall. Not persisted." In `SearchVector` (`store_chromem.go` ~line 223), set `e.Score = r.Similarity` after `metadataToEntry`.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit** (`memory: thread chromem cosine score onto Entry.Score`).

## Task A2: relevance floor in VectorRecaller

**Files:** `memory/recall_vector.go:18-72` (VectorRecaller + NewVectorRecaller), `memory/recall_vector_test.go`

- [ ] **Step 1: Failing test**: a VectorRecaller with `MinScore: 0.9` over a store containing one far entry returns zero entries; with `MinScore: 0` it returns it.
```go
func TestVectorRecaller_MinScoreFloor(t *testing.T) {
	st := newTestChromemStore(t)
	mustAppend(t, st, Entry{Text: "deploy runs on fridays", AgentID: "main"})
	high := NewVectorRecaller(WithMinScore(0.9))
	got, _ := high.Recall(context.Background(), st, "main", "oh hi", 8)
	if len(got) != 0 { t.Fatalf("floor 0.9 should drop far entry, got %d", len(got)) }
	none := NewVectorRecaller(WithMinScore(0))
	got2, _ := none.Recall(context.Background(), st, "main", "oh hi", 8)
	if len(got2) == 0 { t.Fatalf("floor 0 should keep entries") }
}
```
- [ ] **Step 2: Run, verify fail** (`WithMinScore` undefined).
- [ ] **Step 3: Implement.** Add `minScore float32` to `VectorRecaller`; add functional option `WithMinScore(f float32) VectorRecallerOption` and accept variadic options in `NewVectorRecaller`. In `Recall`, after `SearchVector`, drop entries with `Score < r.minScore`. Apply BEFORE returning (so `HybridRecaller` fusion never sees sub-floor vector hits). `minScore` default 0 (no behavior change unless set) — talon sets the real default.
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Confirm HybridRecaller path** — add/extend a test asserting `HybridRecaller(NewVectorRecaller(WithMinScore(0.9)), NewSimpleRecaller())` does not resurrect the floored vector entry via fusion (the SimpleRecaller may still surface an exact lexical match — assert that's the only way it appears). Document this in the test name.
- [ ] **Step 6: Commit** (`memory: add absolute cosine floor to VectorRecaller`).

## Task A3: jess PR

- [ ] Run `go test ./memory/...` in jess — all green.
- [ ] Open the jess PR. After merge + tag/publish, note the new pseudo-version for Part B Task B3.

---

# Part B — talon (this worktree / branch)

## Task B1: config knob

**Files:** `internal/talonconfig/native.go` (memory config), `internal/config/reload.go` classification if needed, tests mirroring existing config tests.

- [ ] **Step 1: Failing test** asserting `memory.recall.min_score` round-trips through native config (read it back after a `config.Set`). Mirror an existing `internal/config` round-trip test.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Add a `MemoryRecallMinScore float64` field** to the memory section of the native config (follow the existing memory config struct + `decodeViper`/`gatewayFromJSON`/`applyGatewayRuntime`/`MarshalTOML` round-trip pattern — same end-to-end wiring the tailscale fields used in ADR 0008; do not use `mapstructure:"-"`). Default unset → talon applies the code default (see B2).
- [ ] **Step 4: Run, verify pass; commit.**

## Task B2: pass the floor into the recaller

**Files:** `cmd/talon/gateway_memory.go:75-82`, `cmd/talon/gateway_memory_test.go` (or nearest existing test)

- [ ] **Step 1: Failing test** asserting that when `memory.recall.min_score` is set in config, `NewVectorRecaller` is constructed with that floor (inject/observe via a seam, or assert end-to-end that a far memory is not recalled through the built ContextManager).
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Read the configured min score (default `0.30` if unset — `const defaultRecallMinScore = 0.30`), pass it: `memory.NewVectorRecaller(memory.WithMinScore(float32(minScore)))` at `gateway_memory.go:76`.
- [ ] **Step 4: Run, verify pass; commit.**

## Task B3: bump jess, drop replace

- [ ] **Step 1:** `go get github.com/guygrigsby/jess@<published-version-from-A3>`; `go mod edit -dropreplace github.com/guygrigsby/jess`; `go mod tidy`.
- [ ] **Step 2:** `go build ./... && go test ./cmd/talon/... ./internal/config/... ./internal/server/...` green.
- [ ] **Step 3: Commit** (`talon-<id>: bump jess for recall relevance floor`). Ensure no `replace` directive remains in the committed go.mod.

## Task B4: CLI reachability + docs

- [ ] Confirm `talon config set memory.recall.min_score 0.35` works and `talon config get` reads it back (the knob must be reachable per the "feature done needs CLI" rule; a dedicated wizard step is optional since this is a non-secret scalar).
- [ ] Note the knob in `docs/dependencies.md` (jess memory section) or the memory docs. Commit.

---

## Verification

```bash
# jess
( cd ../../../jess && go test ./memory/... )
# talon
go build ./... && go vet ./... && go test ./cmd/talon/... ./internal/config/... ./internal/server/...
```

**Calibration (the default floor).** With a populated `~/.talon/memory`, run the gateway and check: (a) "Oh hi!" injects no off-topic memory (inspect the assembled context / logs), (b) a genuinely on-topic prompt still recalls the right memory. Adjust `defaultRecallMinScore` until both hold; record the chosen value + reasoning in the ADR consequences. Start at 0.30, expect to land in 0.25–0.40 for MiniLM-L6-v2 cosine.

**Regression manual e2e:** reproduce the original bug first (pre-fix: "Oh hi!" → pizza), then confirm post-fix it's gone, on the same memory store.

## Follow-ups (file as beads issues)

- Relative/top-gap trimming layered on the floor (deferred per the threshold decision).
- Per-agent floor override.
- Surface recall scores in the web inspector for debuggability.
