# talon: port the chat driver to the jess facade — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace talon's direct-agentcore chat driver with a jess-facade-backed driver behind the (renamed) `ChatRunFn` seam, with per-turn rebuild + seed and ChatStore as the source of truth.

**Architecture:** Implementation swap behind talon's existing server seam. Renames so "agentcore" leaves talon's vocabulary (`AgentcoreRunFn`→`ChatRunFn`, package `agentcore_chat`→`chatdriver`, `gateway_agentcore.go`→`gateway_chat.go`). The agent loop, event stream, context injection, and model dispatch all become jess's; talon retains `agentcore/tools` only as filesystem tool implementations (they satisfy `jess/tool.Tool` structurally).

**Tech Stack:** Go 1.26+. `github.com/guygrigsby/jess` consumed via `replace ../jess` until the jess PR #5 facade additions merge. Talon's existing config/auth/model-resolution code (`ResolveModel`, `ResolveProviderAuth`, `resolveModelMaxTokens`) is reused.

**Spec:** `docs/superpowers/specs/2026-05-29-talon-jess-facade-port-design.md`.

**Confirmed facts from exploration:**
- `internal/agentcore_chat/handler.go` has NO non-test callers (only `handler_test.go`); safe to delete with that test.
- `internal/server/chat.go`'s agentcore surface is ONLY: `AgentcoreRunFn`, `AgentcoreRunResult`, `AgentcoreUsage`, `agentcoreRun` field, `WithAgentcoreRunner` method. Nothing else.
- agentcore is imported by 10 talon files; after this port, only `agentcore/tools` remains (the rest go).

**Invariant:** the `ChatRunFn` SIGNATURE stays identical — same inputs (agentID, sessionKey, runID, userText, selectedModelID, `[]ChatMessage`, four emit callbacks) and outputs (FinalText, ModelID, Usage, error). Only names change at that seam.

---

## File structure

- Rename: `internal/agentcore_chat/` → `internal/chatdriver/` (package `chatdriver`).
- Rename: `cmd/talon/gateway_agentcore.go` → `cmd/talon/gateway_chat.go`.
- Rename: `internal/server/chat_agentcore.go` → `internal/server/chat_run.go`.
- Delete: `internal/chatdriver/handler.go`, `internal/chatdriver/handler_test.go`, `internal/chatdriver/model_cap.go`.
- Rewrite (new bodies): `chatdriver/build.go` (returns `*jess.Agent`); `chatdriver/events.go` (jess `event.Event`→`EventSink`); `chatdriver/history.go` (new, `chatMessagesToJess`); `chatdriver/onboarding.go` (jess `tool.Tool`); `cmd/talon/gateway_chat.go` (jess runner).
- Seam rename in: `internal/server/chat.go`, `internal/server/chat_run.go`.
- Wiring rename in: `cmd/talon/gateway.go`.

---

## Task 1: sanity assertions — agentcore/tools satisfy `jess/tool.Tool`

This is a precondition: the entire plan assumes the fs tools pass through `jess.WithTools` unchanged. Lock it as a compile-time assertion before doing anything substantive.

**Files:** modify `internal/agentcore_chat/build.go` (will be renamed in Task 2, but doing this first proves the precondition).

- [ ] **Step 1:** Verify by adding compile-time assertions next to the imports:

```go
// Compile-time proof that agentcore/tools satisfy jess/tool.Tool — the port
// depends on this so they can pass through jess.WithTools unchanged.
var (
	_ tool.Tool = tools.NewRead("", nil)
	_ tool.Tool = tools.NewWrite("", nil)
	_ tool.Tool = tools.NewEdit("", nil)
	_ tool.Tool = tools.NewBash("")
	_ tool.Tool = tools.NewGlob("")
	_ tool.Tool = tools.NewGrep("")
	_ tool.Tool = tools.NewLs("")
)
```

Add `"github.com/guygrigsby/jess/tool"` to the imports.

- [ ] **Step 2:** `go build ./internal/agentcore_chat/`. If it fails, the structural-compat assumption is wrong; STOP and report — the spec needs revision.

Expected: build succeeds.

- [ ] **Step 3:** Commit:
```bash
git add internal/agentcore_chat/build.go
git commit -m "chore(chatdriver): assert agentcore/tools satisfy jess/tool.Tool"
```

---

## Task 2: rename `internal/agentcore_chat` → `internal/chatdriver`

Mechanical: `git mv` + `package` rename + importers updated.

**Files:** all under `internal/agentcore_chat/` (9 files), plus importers (`cmd/talon/gateway_agentcore.go`, `cmd/talon/gateway_agentcore_test.go`).

- [ ] **Step 1:** `git mv internal/agentcore_chat internal/chatdriver`

- [ ] **Step 2:** In every `.go` file under `internal/chatdriver/`, change `package agentcore_chat` → `package chatdriver`.

- [ ] **Step 3:** Update importers: grep then replace.
```bash
grep -rln "guygrigsby/talon/internal/agentcore_chat" --include='*.go' .
```
For each hit, change the import path `github.com/guygrigsby/talon/internal/agentcore_chat` → `github.com/guygrigsby/talon/internal/chatdriver` and the identifier `agentcore_chat.` → `chatdriver.` (`cmd/talon/gateway_agentcore.go` and `cmd/talon/gateway_agentcore_test.go`).

- [ ] **Step 4:** `go build ./... && go vet ./...` — expected: pass.

- [ ] **Step 5:** Commit:
```bash
git add -A
git commit -m "refactor(chatdriver): rename internal/agentcore_chat to internal/chatdriver"
```

---

## Task 3: rename the server seam (`AgentcoreRunFn`→`ChatRunFn`)

Mechanical name-only change at the server boundary; signature identical.

**Files:** `internal/server/chat.go`, `internal/server/chat_agentcore.go` (also renamed to `chat_run.go` in this task), `cmd/talon/gateway.go`, `cmd/talon/gateway_agentcore.go`, plus any test that references the seam.

- [ ] **Step 1:** In `internal/server/chat.go`, replace types/methods/field:
  - `AgentcoreRunFn` → `ChatRunFn`
  - `AgentcoreRunResult` → `ChatRunResult`
  - `AgentcoreUsage` → `ChatUsage`
  - field `agentcoreRun` → `chatRun`
  - method `WithAgentcoreRunner` → `WithChatRunner`
  - doc comments that mention "agentcore" in this region: reword to "chat driver" / "jess-backed runner" (the driver is no longer agentcore).

- [ ] **Step 2:** `git mv internal/server/chat_agentcore.go internal/server/chat_run.go`. In that file rename `runStreamAgentcore` → `runStream` (or `runChat`); update the field reference `h.agentcoreRun` → `h.chatRun`; rewrite the doc/log strings that say "agentcore" to a generic chat-driver wording.

- [ ] **Step 3:** Update `recordAgentcoreUsage` → `recordChatUsage` if it exists; check `chat.go` for any other "agentcore"/"Agentcore" identifier references and update.

- [ ] **Step 4:** In `cmd/talon/gateway.go`, change `srv.ChatHandler().WithAgentcoreRunner(buildAgentcoreRunner(paths, mem))` to `srv.ChatHandler().WithChatRunner(buildChatRunner(paths, mem))` (the gateway file rename + builder rename happens in Task 7).

- [ ] **Step 5:** `git mv cmd/talon/gateway_agentcore.go cmd/talon/gateway_chat.go`. In that file rename `buildAgentcoreRunner` → `buildChatRunner` (just the function name for now — its body is rewritten in Task 7). Also update `cmd/talon/gateway_agentcore_test.go` import + identifier accordingly.

- [ ] **Step 6:** `go build ./... && go vet ./...` — expected: pass. The code still drives agentcore; only names changed.

- [ ] **Step 7:** Commit:
```bash
git add -A
git commit -m "refactor(server,gateway): rename Agentcore* seam to Chat* (signature unchanged)"
```

---

## Task 4: delete the test-only handler

**Files:** delete `internal/chatdriver/handler.go`, `internal/chatdriver/handler_test.go`.

- [ ] **Step 1:** Confirm again there are no non-test callers:
```bash
grep -rn "chatdriver.Handler\|chatdriver.NewHandler\|RunRequest{" --include='*.go' . | grep -v _test.go
```
Expected: no output. If any non-test caller is found, STOP and surface it.

- [ ] **Step 2:**
```bash
git rm internal/chatdriver/handler.go internal/chatdriver/handler_test.go
go build ./... && go vet ./... && go test -short ./...
```
Expected: pass.

- [ ] **Step 3:** Commit:
```bash
git commit -m "refactor(chatdriver): delete test-only Handler driver"
```

---

## Task 5: delete `model_cap.go` (replaced by jess `WithLLMMaxTokens`)

**Files:** delete `internal/chatdriver/model_cap.go`; preserve `resolveModelMaxTokens` (move to `build.go` or a new `model.go` if it lives in `model_cap.go`).

- [ ] **Step 1:** Read `internal/chatdriver/model_cap.go`. If `resolveModelMaxTokens` lives there, MOVE it to `internal/chatdriver/build.go` (it's still needed to look up per-model caps from config). Delete the rest (`cappedChatModel`, `newCappedChatModel`).

- [ ] **Step 2:**
```bash
git rm internal/chatdriver/model_cap.go   # if resolveModelMaxTokens has been moved
```
(If you preferred to keep `model_cap.go` as a thin file with only `resolveModelMaxTokens`, rename it `internal/chatdriver/model_config.go` instead and trim its imports.)

- [ ] **Step 3:** `go build ./...` — likely FAILS at `build.go`'s `newCappedChatModel(...)` call. That call is REMOVED in Task 8; until then, build will not be clean. Confirm the only failure is that call site (no other consumers of `cappedChatModel`).

```bash
go build ./... 2>&1 | head
```

- [ ] **Step 4:** Commit (intentionally leaving build broken until Task 8 — keep commits small and topical):
```bash
git add -A
git commit -m "refactor(chatdriver): drop cappedChatModel (jess.WithLLMMaxTokens replaces it)"
```

Note: a brief broken-build window is acceptable here; Tasks 8/9 close it within the same plan. If you prefer to keep main green, swap Task 5 and Task 8 — the dependency is symmetric.

---

## Task 6: jess event adapter — `chatdriver/events.go`

Replace the agentcore `EventAdapter` body with a jess-event adapter implementing the same `EventSink` interface and accumulation behavior. The `EventSink` interface itself is unchanged.

**Files:**
- Rewrite: `internal/chatdriver/events.go`
- Rewrite: `internal/chatdriver/events_test.go`

- [ ] **Step 1: Write the failing test** in `internal/chatdriver/events_test.go`:

```go
package chatdriver

import (
	"errors"
	"reflect"
	"testing"

	"github.com/guygrigsby/jess/event"
)

// recordingSink captures calls in order.
type recordingSink struct {
	calls []string
}

func (r *recordingSink) Delta(full, delta string) {
	r.calls = append(r.calls, "Delta("+full+","+delta+")")
}
func (r *recordingSink) Thinking(full, delta string) {
	r.calls = append(r.calls, "Thinking("+full+","+delta+")")
}
func (r *recordingSink) Final(full string)                  { r.calls = append(r.calls, "Final("+full+")") }
func (r *recordingSink) ToolStart(id, name, args string)    {
	r.calls = append(r.calls, "ToolStart("+id+","+name+","+args+")")
}
func (r *recordingSink) ToolResult(id, name, out string, isErr bool) {
	r.calls = append(r.calls, "ToolResult("+id+","+name+","+out+")")
}
func (r *recordingSink) Error(kind, msg string) {
	r.calls = append(r.calls, "Error("+kind+","+msg+")")
}

func TestEventAdapter_AccumulatesDeltaAndFinal(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "Hel", DeltaKind: event.DeltaText})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "lo", DeltaKind: event.DeltaText})
	a.Finalize("Hello") // adapter convention: Finalize wraps up after run.Wait()
	want := []string{"Delta(Hel,Hel)", "Delta(Hello,lo)", "Final(Hello)"}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}

func TestEventAdapter_ThinkingSeparate(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "thi", DeltaKind: event.DeltaThinking})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "nking", DeltaKind: event.DeltaThinking})
	a.Handle(event.Event{Kind: event.KindMessageDelta, Delta: "ans", DeltaKind: event.DeltaText})
	want := []string{"Thinking(thi,thi)", "Thinking(thinking,nking)", "Delta(ans,ans)"}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}

func TestEventAdapter_ToolAndError(t *testing.T) {
	sink := &recordingSink{}
	a := NewEventAdapter(sink)
	a.Handle(event.Event{Kind: event.KindToolStart, ToolCallID: "c1", Tool: "remember", Args: []byte(`{"k":"v"}`)})
	a.Handle(event.Event{Kind: event.KindToolEnd, ToolCallID: "c1", Tool: "remember", Result: []byte(`{"ok":true}`), IsError: false})
	a.Handle(event.Event{Kind: event.KindError, Err: errors.New("boom")})
	want := []string{
		`ToolStart(c1,remember,{"k":"v"})`,
		`ToolResult(c1,remember,{"ok":true})`,
		`Error(agent,boom)`,
	}
	if !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
}
```

- [ ] **Step 2:** `go test ./internal/chatdriver/ -run TestEventAdapter` — expected: FAIL (`event.Event` type undefined references or NewEventAdapter shape wrong).

- [ ] **Step 3: Rewrite `internal/chatdriver/events.go`** entirely:

```go
package chatdriver

import (
	"strings"

	"github.com/guygrigsby/jess/event"
)

// EventSink is the wire-shape contract the chat handler implements. The
// event adapter calls into this; tests pass a recording sink to assert
// behavior, and the gateway-level adapter wraps talon's existing
// ChatHandler.emit* methods.
//
// Calls are serialized — no concurrent calls per session. The fullText
// passed to Delta/Thinking is the running snapshot so far.
type EventSink interface {
	Delta(fullText, deltaText string)
	Thinking(fullText, deltaText string)
	Final(fullText string)
	ToolStart(toolCallID, name, argumentsJSON string)
	ToolResult(toolCallID, name, output string, isError bool)
	Error(kind, msg string)
}

// EventAdapter translates a stream of jess event.Events into EventSink
// calls, accumulating running text so each Delta/Thinking carries the
// full-so-far snapshot. Lifecycle: one EventAdapter per chat run.
// Range over run.Events() calling Handle; after the channel closes,
// call Finalize(finalText) with run.Wait()'s final assistant text.
type EventAdapter struct {
	sink        EventSink
	accumulated strings.Builder
	thinking    strings.Builder
}

func NewEventAdapter(sink EventSink) *EventAdapter {
	return &EventAdapter{sink: sink}
}

// Handle dispatches one jess event onto the sink. Returns the number of
// sink calls made (1 or 0).
func (a *EventAdapter) Handle(ev event.Event) int {
	switch ev.Kind {
	case event.KindMessageDelta:
		if ev.Delta == "" {
			return 0
		}
		if ev.DeltaKind == event.DeltaThinking {
			a.thinking.WriteString(ev.Delta)
			a.sink.Thinking(a.thinking.String(), ev.Delta)
			return 1
		}
		// DeltaText (or any other kind, including DeltaToolCall):
		// accumulate as visible text. Streamed tool-call argument
		// JSON is rendered alongside text in the same wire stream
		// today; if a host needs separate handling later, add a
		// branch.
		a.accumulated.WriteString(ev.Delta)
		a.sink.Delta(a.accumulated.String(), ev.Delta)
		return 1
	case event.KindToolStart:
		a.sink.ToolStart(ev.ToolCallID, ev.Tool, string(ev.Args))
		return 1
	case event.KindToolEnd:
		a.sink.ToolResult(ev.ToolCallID, ev.Tool, string(ev.Result), ev.IsError)
		return 1
	case event.KindError:
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		a.sink.Error("agent", msg)
		return 1
	}
	return 0
}

// Finalize emits Final with the assistant text. Called by the runner
// after run.Events() closes and run.Wait() returns. If finalText is
// empty the adapter falls back to the accumulated delta total so the FE
// still sees a Final.
func (a *EventAdapter) Finalize(finalText string) {
	full := finalText
	if full == "" {
		full = a.accumulated.String()
	}
	a.sink.Final(full)
}

// Snapshot returns the current accumulators (test helper).
func (a *EventAdapter) Snapshot() (accumulated, thinking string) {
	return a.accumulated.String(), a.thinking.String()
}
```

- [ ] **Step 4:** `go test ./internal/chatdriver/ -run TestEventAdapter` — expected: PASS.

- [ ] **Step 5:** Commit:
```bash
git add internal/chatdriver/events.go internal/chatdriver/events_test.go
git commit -m "feat(chatdriver): jess event adapter replaces agentcore EventAdapter"
```

---

## Task 7: history conversion — `chatdriver/history.go`

Mirror the old `agentcoreHistoryFromChatStore` but to jess `message.Message`. Lives in `chatdriver`, not the gateway, so the runner can call it directly and tests can drive it without provider deps.

**Files:**
- Create: `internal/chatdriver/history.go`
- Create: `internal/chatdriver/history_test.go`

- [ ] **Step 1: Write the failing test** `internal/chatdriver/history_test.go`:

```go
package chatdriver

import (
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/server"
)

func TestChatMessagesToJess_AllRoles(t *testing.T) {
	in := []server.ChatMessage{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "checking", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "recall", ArgumentsJSON: `{"q":"x"}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "recall", Content: `{"hits":0}`},
		{Role: "assistant", Content: "no hits"},
	}
	got := ChatMessagesToJess(in)
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	if got[0].Role != message.RoleSystem || got[0].Text() != "you are helpful" {
		t.Errorf("msg[0] = %+v", got[0])
	}
	if got[1].Role != message.RoleUser || got[1].Text() != "hi" {
		t.Errorf("msg[1] = %+v", got[1])
	}
	// Assistant with tool call: one text block + one tool-call block.
	if got[2].Role != message.RoleAssistant || len(got[2].Content) != 2 {
		t.Fatalf("msg[2] = %+v", got[2])
	}
	if got[2].Content[1].Kind != message.BlockToolCall || got[2].Content[1].ToolID != "c1" || got[2].Content[1].ToolName != "recall" {
		t.Errorf("tool-call block = %+v", got[2].Content[1])
	}
	// Tool result.
	if got[3].Role != message.RoleTool || len(got[3].Content) != 1 || got[3].Content[0].Kind != message.BlockToolResult {
		t.Fatalf("msg[3] = %+v", got[3])
	}
	if got[3].Content[0].ToolID != "c1" || string(got[3].Content[0].Result) != `{"hits":0}` {
		t.Errorf("tool-result = %+v", got[3].Content[0])
	}
	if got[4].Role != message.RoleAssistant || got[4].Text() != "no hits" {
		t.Errorf("msg[4] = %+v", got[4])
	}
	// Args round-trips as JSON.
	if !json.Valid(got[2].Content[1].Args) {
		t.Errorf("tool-call args not valid JSON: %s", got[2].Content[1].Args)
	}
}

func TestChatMessagesToJess_EmptyToolCallArgs(t *testing.T) {
	// Empty/whitespace args must become "{}" so the model sees valid JSON.
	in := []server.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "recall", ArgumentsJSON: "   "}}},
	}
	got := ChatMessagesToJess(in)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("got = %+v", got)
	}
	if string(got[0].Content[0].Args) != "{}" {
		t.Errorf("args = %q, want %q", got[0].Content[0].Args, "{}")
	}
}
```

- [ ] **Step 2:** `go test ./internal/chatdriver/ -run TestChatMessagesToJess` — FAIL (undefined `ChatMessagesToJess`).

- [ ] **Step 3: Implement** `internal/chatdriver/history.go`:

```go
package chatdriver

import (
	"encoding/json"
	"strings"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/talon/internal/server"
)

// ChatMessagesToJess converts talon's stored chat history into jess
// message.Messages suitable for seeding a Session via
// jess.Agent.NewSessionWithHistory. Mirrors the deleted
// agentcoreHistoryFromChatStore but produces jess types. Unknown roles
// are skipped (defensive; shouldn't occur in valid ChatStore state).
func ChatMessagesToJess(history []server.ChatMessage) []message.Message {
	out := make([]message.Message, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, message.Message{
				Role:    message.RoleUser,
				Content: []message.ContentBlock{{Kind: message.BlockText, Text: m.Content}},
			})
		case "assistant":
			blocks := make([]message.ContentBlock, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				blocks = append(blocks, message.ContentBlock{Kind: message.BlockText, Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := strings.TrimSpace(tc.ArgumentsJSON)
				if args == "" {
					args = "{}"
				}
				blocks = append(blocks, message.ContentBlock{
					Kind:     message.BlockToolCall,
					ToolID:   tc.ID,
					ToolName: tc.Name,
					Args:     json.RawMessage(args),
				})
			}
			out = append(out, message.Message{Role: message.RoleAssistant, Content: blocks})
		case "tool":
			out = append(out, message.Message{
				Role: message.RoleTool,
				Content: []message.ContentBlock{{
					Kind:    message.BlockToolResult,
					ToolID:  m.ToolCallID,
					Result:  json.RawMessage(m.Content),
					IsError: false,
				}},
			})
		case "system":
			out = append(out, message.Message{
				Role:    message.RoleSystem,
				Content: []message.ContentBlock{{Kind: message.BlockText, Text: m.Content}},
			})
		}
	}
	return out
}
```

- [ ] **Step 4:** `go test ./internal/chatdriver/ -run TestChatMessagesToJess` — PASS.

- [ ] **Step 5:** Delete the old `agentcoreHistoryFromChatStore` from `cmd/talon/gateway_chat.go` (formerly gateway_agentcore.go) and its test in `gateway_agentcore_test.go` (rename that test file to `gateway_chat_test.go` and rewrite its test to assert `chatdriver.ChatMessagesToJess` — same shape, different types).

- [ ] **Step 6:** `go build ./... && go test -short ./...` — expected: pass (the runner still builds via the half-old, half-new path; Task 10 finishes the runner rewrite).

- [ ] **Step 7:** Commit:
```bash
git add -A
git commit -m "feat(chatdriver): ChatMessagesToJess history conversion (replaces agentcoreHistoryFromChatStore)"
```

---

## Task 8: reimplement onboarding tool as a jess `tool.Tool`

**Files:** rewrite `internal/chatdriver/onboarding.go` and its test.

- [ ] **Step 1:** Read the current `internal/chatdriver/onboarding.go`. It defines `finishOnboardingTool` implementing `agentcore.Tool` with `Name/Label/Description/Schema/Execute` using `agentcore/schema` builders.

- [ ] **Step 2: Write the failing test** `internal/chatdriver/onboarding_test.go`:

```go
package chatdriver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/tool"
)

func TestFinishOnboardingTool_SatisfiesToolTool(t *testing.T) {
	var _ tool.Tool = newFinishOnboardingTool(t.TempDir())
}

func TestFinishOnboardingTool_SchemaShape(t *testing.T) {
	tl := newFinishOnboardingTool(t.TempDir())
	s := tl.Schema()
	if s["type"] != "object" {
		t.Fatalf("schema type = %v, want object", s["type"])
	}
	props, _ := s["properties"].(map[string]any)
	if _, ok := props["agentName"]; !ok {
		t.Errorf("schema missing 'agentName' property")
	}
	req, _ := s["required"].([]string)
	wantReq := map[string]bool{"agentName": true}
	for _, r := range req {
		if wantReq[r] {
			delete(wantReq, r)
		}
	}
	if len(wantReq) > 0 {
		t.Errorf("required missing %v", wantReq)
	}
}

func TestFinishOnboardingTool_ExecuteWritesSentinel(t *testing.T) {
	dir := t.TempDir()
	tl := newFinishOnboardingTool(dir)
	// Args must match the current onboarding contract — see the
	// existing onboarding.go for the exact field set. Test the
	// observable side-effect: bootstrap sentinel cleared after a
	// successful Execute. Keep the assertion the same as the
	// pre-port test (read it from the old test file before rewrite).
	args, _ := json.Marshal(map[string]any{"agentName": "Test"})
	out, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) == 0 {
		t.Error("Execute returned empty JSON")
	}
	// Assert no longer in bootstrap mode (sentinel cleared).
	// (The exact API for checking this is in internal/agentcontext.)
}
```

(Adjust the args and side-effect assertion to match the actual contract — copy from the existing test before deleting it.)

- [ ] **Step 3:** `go test ./internal/chatdriver/ -run TestFinishOnboardingTool` — FAIL (assertion fails or type doesn't satisfy `tool.Tool` — depending on current shape).

- [ ] **Step 4: Rewrite `internal/chatdriver/onboarding.go`** so `newFinishOnboardingTool(personaDir string) tool.Tool` returns a value implementing the jess `tool.Tool` interface (`Name/Description/Schema/Execute`). Drop the `Label` method (agentcore-specific). Build the JSON schema directly as `map[string]any`:

```go
func (t *finishOnboardingTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agentName": map[string]any{
				"type":        "string",
				"description": "The agent's chosen identity name.",
			},
			// ... copy remaining properties from the old schema.Object/Property calls,
			// translating each schema.Property("foo", schema.String(desc)) into
			// "foo": {"type":"string","description":desc}.
		},
		"required": []string{"agentName"}, // copy the .Required() set from the old schema
	}
}
```

Keep the existing `Execute` body byte-for-byte (it just writes files); only the imports and Schema method change. Drop the `github.com/voocel/agentcore/schema` import.

- [ ] **Step 5:** `go test ./internal/chatdriver/ -run TestFinishOnboardingTool` — PASS.

- [ ] **Step 6:** Commit:
```bash
git add internal/chatdriver/onboarding.go internal/chatdriver/onboarding_test.go
git commit -m "feat(chatdriver): reimplement onboarding tool as jess tool.Tool (drop agentcore/schema)"
```

---

## Task 9: reshape `Builder.BuildAgent` to return `*jess.Agent`

The biggest single change. Reshape the existing `Builder` so `BuildAgent(agentID) (*jess.Agent, ModelChoice, error)` and the agent is built with `jess.New(...)` instead of `agentcore.NewAgent(...)`.

**Files:** rewrite `internal/chatdriver/build.go`. Update `internal/chatdriver/build_test.go` to assert the new shape.

- [ ] **Step 1: Rewrite `build.go`**. Drop the `agentcore`, `agentcore/llm`, `memorySourceMiddleware`, and `memoryContextManager` machinery. Keep `Builder` + `WithSelectedModel` + `WithMemory` + `WithMemorySource` + `WithAuthOverride` and the helpers (`resolveAgentWorkspace`, `resolvePersonaDir`, `buildSystemPrompt`, `composeSystemPrompt`, `filterAgentcoreTools` renamed `filterTools`, `resolveModelMaxTokens` moved here from `model_cap.go`). Drop `WithMemoryOptions` (jess defaults per the spec).

The new `BuildAgent`:

```go
func (b *Builder) BuildAgent(agentID string) (*jess.Agent, ModelChoice, error) {
	choice := ResolveModel(b.merged, agentID)
	if b.selectedModelID != "" {
		choice = ModelChoiceFromID(b.selectedModelID, choice.Fallbacks)
	}
	if choice.ID == "" {
		return nil, choice, fmt.Errorf("no model configured for agent %q (set agents.list[].model.primary or agents.defaults.model.primary)", agentID)
	}
	if choice.Provider == "" {
		return nil, choice, fmt.Errorf("model %q has no provider segment (expected '<provider>/<model>')", choice.ID)
	}

	auth := b.authOverride
	if auth == nil {
		auth = ResolveProviderAuth(b.merged, b.paths)
	}
	pa, ok := auth[choice.Provider]
	if !ok {
		return nil, choice, fmt.Errorf("provider %q is unauthenticated — configure plugins.entries.openai-compat.config.providers.%s.apiKey (or plugins.entries.anthropic.config.apiKey for anthropic)", choice.Provider, choice.Provider)
	}
	apiKey := pa.APIKey
	if apiKey == "" && isLoopbackURL(pa.BaseURL) {
		apiKey = "no-auth"
	}

	cap := resolveModelMaxTokens(b.merged, choice.Provider, choice.Model)
	if cap <= 0 {
		cap = 4096
	}

	model, err := jess.LiteLLM(choice.Provider, choice.Model,
		jess.WithLLMAPIKey(apiKey),
		jess.WithLLMBaseURL(pa.BaseURL),
		jess.WithLLMMaxTokens(cap),
	)
	if err != nil {
		return nil, choice, fmt.Errorf("init model %s/%s: %w", choice.Provider, choice.Model, err)
	}

	workspace := resolveAgentWorkspace(b.merged, agentID)
	personaDir := resolvePersonaDir(workspace, b.paths.Talon.Dir)
	systemPrompt := buildSystemPrompt(b.merged, agentID, personaDir)
	fileState := tools.NewFileReadState()

	toolSet := []tool.Tool{}
	if workspace != "" {
		toolSet = append(toolSet,
			tools.NewRead(workspace, fileState),
			tools.NewWrite(workspace, fileState),
			tools.NewEdit(workspace, fileState),
			tools.NewBash(workspace),
			tools.NewGlob(workspace),
			tools.NewGrep(workspace),
			tools.NewLs(workspace),
		)
	}
	if agentcontext.BootstrapActive(personaDir) {
		toolSet = append(toolSet, newFinishOnboardingTool(personaDir))
	}
	if b.memStore != nil && agentID != "" {
		toolSet = append(toolSet, memory.NewRememberTool(b.memStore, memory.RememberOptions{AgentID: agentID}))
		if b.memRecaller != nil {
			toolSet = append(toolSet, memory.NewRecallTool(b.memStore, b.memRecaller, memory.RecallOptions{AgentID: agentID}))
		}
	}
	policy, err := toolaccess.Resolve(b.merged, b.paths, agentID)
	if err != nil {
		return nil, choice, fmt.Errorf("resolve tool access for %q: %w", agentID, err)
	}
	toolSet = filterTools(toolSet, policy)

	maxTurns := int(gjson.GetBytes(b.merged, "agents.defaults.maxTurns").Int())
	if v := gjson.GetBytes(b.merged, fmt.Sprintf("agents.list.#(id==%q).maxTurns", agentID)).Int(); v > 0 {
		maxTurns = int(v)
	}
	if maxTurns <= 0 {
		maxTurns = 20
	}

	opts := []jess.Option{
		jess.WithModel(model),
		jess.WithAgentID(agentID),
		jess.WithMaxTurns(maxTurns),
	}
	if b.memStore != nil && b.memRecaller != nil {
		opts = append(opts, jess.WithMemory(b.memStore, b.memRecaller))
	}
	if systemPrompt != "" {
		opts = append(opts, jess.WithSystemPrompt(systemPrompt))
	}
	if len(toolSet) > 0 {
		opts = append(opts, jess.WithTools(toolSet...))
	}
	agent, err := jess.New(opts...)
	if err != nil {
		return nil, choice, fmt.Errorf("jess.New: %w", err)
	}
	return agent, choice, nil
}

// filterTools replaces filterAgentcoreTools, generic over jess tool.Tool.
func filterTools(in []tool.Tool, policy toolaccess.Policy) []tool.Tool {
	if !policy.Enabled {
		return nil
	}
	if !policy.Restricted {
		return in
	}
	out := make([]tool.Tool, 0, len(in))
	for _, t := range in {
		if policy.Allows(t.Name()) {
			out = append(out, t)
		}
	}
	return out
}
```

Imports become:

```go
import (
	"fmt"
	"strings"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/tool"
	"github.com/tidwall/gjson"
	"github.com/voocel/agentcore/tools"

	"github.com/guygrigsby/talon/internal/agentcontext"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/toolaccess"
)
```

Also drop the `Builder` fields `memKinds`, `memMaxItems`, `memHeader`, and the `WithMemoryOptions` method (jess defaults are accepted per the spec). Keep `source memory.Source` and `WithMemorySource`.

Move `resolveModelMaxTokens` into this file (from the deleted `model_cap.go`). Drop the `var _ tool.Tool = tools.New*(...)` assertions from Task 1 (they were a sanity check; the new build.go uses these tools as `tool.Tool` directly, which is a stronger compile-time proof).

- [ ] **Step 2:** Rewrite `internal/chatdriver/build_test.go` to assert the new shape: `agent, choice, err := b.BuildAgent("main")` returns `*jess.Agent`; remove tests that reach for `agent.State().Tools` (an agentcore internal). Replace those assertions with: the memory tools are present when WithMemory is wired (introspect via a test sink that runs a Prompt against a fake `jess.LiteLLM`-equivalent or just assert the Builder returns no error and a non-nil Agent for the test config). Keep tests for the auth/error paths (provider missing, etc.).

- [ ] **Step 3:** `go build ./... && go vet ./... && go test -short ./internal/chatdriver/` — expected: pass.

- [ ] **Step 4:** Commit:
```bash
git add -A
git commit -m "feat(chatdriver): Builder.BuildAgent returns *jess.Agent (drops direct agentcore use)"
```

---

## Task 10: rewrite the runner — `cmd/talon/gateway_chat.go`

Replace the body of `buildChatRunner` so it builds a `*jess.Agent`, opens a Session seeded from ChatStore, Prompts, streams to the EventSink, and returns `ChatRunResult`.

**Files:** rewrite `cmd/talon/gateway_chat.go`.

- [ ] **Step 1: Rewrite the file** (the existing renamed file from Tasks 2/3). The body of `buildChatRunner`:

```go
package main

import (
	"context"
	"fmt"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/memory"
	jessmsg "github.com/guygrigsby/jess/message"

	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildChatRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.ChatRunFn {
	return func(
		ctx context.Context,
		agentID, sessionKey, runID, userText, selectedModelID string,
		priorHistory []server.ChatMessage,
		emitText func(seq int, state, full, delta string),
		emitToolStart func(toolCallID, name, args string),
		emitToolResult func(toolCallID, name, output string, isErr bool),
		emitError func(seq int, kind, msg string),
	) (server.ChatRunResult, error) {
		sink := &gatewayEventSink{
			text:       emitText,
			toolStart:  emitToolStart,
			toolResult: emitToolResult,
			err:        emitError,
		}
		adapter := chatdriver.NewEventAdapter(sink)

		merged, err := config.MergedBytes(paths)
		if err != nil {
			return server.ChatRunResult{}, fmt.Errorf("merged config: %w", err)
		}
		builder := chatdriver.NewBuilder(merged, paths)
		if selectedModelID != "" {
			builder = builder.WithSelectedModel(selectedModelID)
		}
		if mem != nil {
			builder = builder.
				WithMemory(mem.Store, mem.Recaller).
				WithMemorySource(sessionKey, runID)
		}
		agent, choice, err := builder.BuildAgent(agentID)
		if err != nil {
			return server.ChatRunResult{}, err
		}

		sess, err := agent.NewSessionWithHistory(chatdriver.ChatMessagesToJess(priorHistory))
		if err != nil {
			return server.ChatRunResult{}, err
		}

		// Stamp the run's memory provenance onto the Prompt ctx. jess
		// re-applies it onto each tool's Execute ctx so memory.RememberTool
		// picks up Source via memory.SourceFromContext.
		promptCtx := memory.WithSource(ctx, memory.Source{
			SessionID: sessionKey,
			MessageID: runID,
			Tool:      "remember",
			Reason:    "model decided",
		})

		run, err := sess.Prompt(promptCtx, userText)
		if err != nil {
			return server.ChatRunResult{}, err
		}

		// Stream events to the sink while the run goroutine drives the model.
		for ev := range run.Events() {
			adapter.Handle(ev)
		}
		res, runErr := run.Wait()
		// Final visible text: the last assistant text in res.Messages.
		final := lastAssistantText(res.Messages)
		adapter.Finalize(final)

		usage := server.ChatUsage{}
		if res.Summary != nil {
			usage.InputTokens = res.Summary.Usage.Input
			usage.OutputTokens = res.Summary.Usage.Output
		}
		return server.ChatRunResult{
			FinalText: final,
			ModelID:   choice.ID,
			Usage:     usage,
		}, runErr
	}
}

func lastAssistantText(msgs []jessmsg.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == jessmsg.RoleAssistant {
			return msgs[i].Text()
		}
	}
	return ""
}

// gatewayEventSink implements chatdriver.EventSink by calling the
// runner's emit closures.
type gatewayEventSink struct {
	text       func(seq int, state, full, delta string)
	toolStart  func(toolCallID, name, args string)
	toolResult func(toolCallID, name, output string, isErr bool)
	err        func(seq int, kind, msg string)
}

func (s *gatewayEventSink) Delta(full, delta string)      { s.text(0, "delta", full, delta) }
func (s *gatewayEventSink) Thinking(full, delta string)   { s.text(0, "thinking", full, delta) }
func (s *gatewayEventSink) Final(full string)             { s.text(0, "final", full, "") }
func (s *gatewayEventSink) ToolStart(id, name, args string) { s.toolStart(id, name, args) }
func (s *gatewayEventSink) ToolResult(id, name, out string, isErr bool) {
	s.toolResult(id, name, out, isErr)
}
func (s *gatewayEventSink) Error(kind, msg string) { s.err(0, kind, msg) }
```

Notes:
- This replaces the previous `buildAgentcoreRunner` body entirely.
- The runner per-turn rebuilds the agent (matches talon today; no session registry).
- `res.Summary.Usage` comes from sub-project 1's addition.
- `agentcoreHistoryFromChatStore` is gone; `chatdriver.ChatMessagesToJess` replaces it.
- ChatStore persistence of the assistant message happens in the server layer (existing `h.store.Append(sessionKey, "assistant", result.FinalText)` in `runStream`). Tool results during the run are NOT yet persisted back to ChatStore by this runner — preserving the pre-port behavior. (The server stored only the final assistant text before; that stays.)

- [ ] **Step 2:** `go build ./... && go vet ./...` — expected: pass. Run gateway-related tests: `go test -short ./cmd/talon/ ./internal/server/`.

- [ ] **Step 3:** Commit:
```bash
git add cmd/talon/gateway_chat.go
git commit -m "feat(gateway): jess-backed buildChatRunner replaces buildAgentcoreRunner"
```

---

## Task 11: rework integration tests onto the real runner

`internal/chatdriver/integration_test.go` currently builds an agentcore agent via `NewBuilder` + `NewEventAdapter` and drives it through `agent.Prompt`/`WaitForIdle`. Replace with: build a `*jess.Agent` via `chatdriver.NewBuilder` (jess-returning), then drive ONE run by either (a) calling `buildChatRunner(...)` directly (the production path), or (b) `agent.NewSessionWithHistory(nil).Prompt(ctx, "...")` and range Events. (a) is preferred — it tests the same production code the gateway runs.

**Files:** rewrite `internal/chatdriver/integration_test.go`.

- [ ] **Step 1:** Read the existing integration test for the assertions and provider gating logic; KEEP those (skip-if-no-key, TTFB measurements, etc.). REPLACE the driver code with a production-shaped invocation:

```go
// Sketch — adapt the surrounding fixtures and assertions:
runner := buildChatRunnerForTest(paths, memSidecar) // a tiny test-local helper that wraps buildChatRunner
res, err := runner(ctx, agentID, sessionKey, runID, "hello", "", nil,
    func(seq int, state, full, delta string) { /* record */ },
    func(id, name, args string) { /* record */ },
    func(id, name, out string, isErr bool) { /* record */ },
    func(seq int, kind, msg string) { /* record */ },
)
```

Since `buildChatRunner` lives in package `main` (cmd/talon), the integration test in `chatdriver` cannot call it directly. Two options:
- Move the production-shaped runner-constructor into `chatdriver` (a tiny exported `NewChatRunner(paths, mem) server.ChatRunFn`) and have `gateway_chat.go` call that. Recommended — fewer test seams, no test-only constructors.
- Or, in the integration test, recreate the runner inline (duplicates a bit of code; rejected per "no test-only handlers").

Recommendation: implement option 1 — move `buildChatRunner`'s body into `chatdriver.NewChatRunner`. `cmd/talon/gateway_chat.go` becomes a one-liner: `return chatdriver.NewChatRunner(paths, mem)`. `gatewayEventSink` moves into `chatdriver` too.

- [ ] **Step 2:** Make the move (this is the cleanest realization of "tests use the production path"). The runner body, the event sink, and `lastAssistantText` move into `internal/chatdriver/runner.go`. `cmd/talon/gateway_chat.go` is reduced to:

```go
package main

import (
	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildChatRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.ChatRunFn {
	return chatdriver.NewChatRunner(paths, mem)
}
```

- [ ] **Step 3:** Rewrite `internal/chatdriver/integration_test.go` to call `chatdriver.NewChatRunner(paths, mem)` and drive one turn through it, asserting the same things the old test asserted (final text non-empty, no error, etc.).

- [ ] **Step 4:** Verify: `go build ./... && go vet ./... && go test -short ./...`. Integration test is `-tags=integration`; running it requires real provider keys — skip in unit-test verification.

- [ ] **Step 5:** Commit:
```bash
git add -A
git commit -m "refactor(chatdriver): move runner into package; integration test drives production runner"
```

---

## Task 12: final gate + finish

**Files:** none (verification only).

- [ ] **Step 1:** Format & vet:
```bash
gofmt -l .                    # expect: empty
go vet ./...                  # expect: ok
```

- [ ] **Step 2:** Unit tests:
```bash
go test -race ./...           # expect: ALL PASS
```

- [ ] **Step 3:** Build the binary:
```bash
make build                    # expect: bin/talon built
```

- [ ] **Step 4:** Confirm agentcore is gone from talon's driver code (only `agentcore/tools` should remain as tool impls):
```bash
grep -rln "voocel/agentcore" --include='*.go' . | grep -v "/agentcore/tools" || echo "DRIVER CLEAN"
```
Expected: `DRIVER CLEAN`. The only `agentcore` import remaining is `voocel/agentcore/tools` in `internal/chatdriver/build.go`.

- [ ] **Step 5:** Manual smoke (optional): run `bin/talon gateway run` with a configured provider and send one `chat.send` — verify streaming + tool calls + memory recall behave like before.

- [ ] **Step 6:** Hand off to `superpowers:finishing-a-development-branch` to push `feat/port-to-jess-facade`, open a PR, and run the per-PR Copilot review loop.

---

## Self-Review

**Spec coverage:**
- Rename (AgentcoreRunFn→ChatRunFn, agentcore_chat→chatdriver, gateway_agentcore.go→gateway_chat.go): Tasks 2, 3. ✓
- Drop handler.go + no test-only handlers: Tasks 4, 11. ✓
- Per-turn rebuild + seed via NewSessionWithHistory: Task 10 (and the chatdriver.NewChatRunner moved-in-Task-11 final shape). ✓
- Builder returns *jess.Agent: Task 9. ✓
- Event adapter: Task 6. ✓
- History conversion: Task 7. ✓
- Provenance via memory.WithSource on Prompt ctx: Task 10. ✓
- jess.LiteLLM with WithLLMAPIKey/BaseURL/MaxTokens, drop model_cap: Tasks 5, 9. ✓
- Tools via WithTools incl. agentcore/tools fs + memory + onboarding-as-tool.Tool: Tasks 1, 8, 9. ✓
- jess defaults for memory injection: Task 9 (no WithMemoryOptions call). ✓
- Drop memorySourceMiddleware: Task 9. ✓
- gateway.go wiring rename: Task 3 Step 4. ✓
- Integration tests use the real runner: Task 11. ✓
- Final gate + verify-only-agentcore/tools remains: Task 12. ✓

**Placeholder scan:** Task 8 Step 4 says "copy remaining properties from the old schema.Object/Property calls" — that's a transformation rule against an existing file the implementer has open, not a placeholder for missing requirements. Task 11 ("a tiny test-local helper") is replaced in Step 2 with a concrete move (`chatdriver.NewChatRunner`); no placeholder remains.

**Type consistency:**
- `tool.Tool` (`github.com/guygrigsby/jess/tool`) used identically in Tasks 1, 8, 9 (assertions; onboarding return type; filterTools slice element; WithTools args).
- `event.Event` / `event.KindMessageDelta` / `event.DeltaText` / `event.DeltaThinking` / `event.KindToolStart` / `event.KindToolEnd` / `event.KindError` used in Task 6 match jess's actual `event/event.go` types.
- `jess.WithLLMAPIKey/BaseURL/MaxTokens` (Task 9) match sub-project 1's additions.
- `server.ChatRunFn`/`ChatRunResult`/`ChatUsage` introduced in Task 3 are used identically in Tasks 10, 11.
- `chatdriver.ChatMessagesToJess` (Task 7) signature `[]server.ChatMessage -> []message.Message` matches its caller in Task 10.
- `NewChatRunner` introduced in Task 11 Step 2 is referenced consistently between `chatdriver` and `cmd/talon/gateway_chat.go`.
