package server

import (
	"testing"

	"github.com/guygrigsby/talon/internal/audit"
	"github.com/guygrigsby/talon/internal/provider"
)

type fakeRec struct{ evs []audit.Event }

func (f *fakeRec) Record(e audit.Event) { f.evs = append(f.evs, e) }
func (f *fakeRec) Close() error         { return nil }

func TestChatHandler_RecordsAuditFromEmitChokePoints(t *testing.T) {
	rec := &fakeRec{}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	).WithAudit(rec)

	const session = "agent:main:web"
	const run = "run-1"

	h.emitAgentToolStart(session, run, session, "tc-1", "bash", `{"cmd":"ls","token":"sk-secret-abc"}`)
	h.emitAgentToolResult(session, run, session, "tc-1", "bash", "files listed", false)
	h.emitAgentToolResult(session, run, session, "tc-2", "bash", "boom", true)
	_ = h.emitError(session, run, session, 3, "provider_error", "upstream 500")

	if len(rec.evs) != 4 {
		t.Fatalf("expected 4 audit events, got %d: %+v", len(rec.evs), rec.evs)
	}

	start := rec.evs[0]
	if start.Kind != audit.KindToolCall || start.Session != session || start.Run != run ||
		start.ToolCallID != "tc-1" || start.Tool != "bash" {
		t.Fatalf("tool_call event mismatch: %+v", start)
	}
	// Args are forwarded verbatim here; redaction happens at the recorder
	// (verified in internal/audit). The handler must not drop the field.
	if start.Args == "" {
		t.Fatalf("tool_call args not forwarded: %+v", start)
	}

	res := rec.evs[1]
	if res.Kind != audit.KindToolResult || res.Output != "files listed" || res.IsError {
		t.Fatalf("tool_result event mismatch: %+v", res)
	}
	if errRes := rec.evs[2]; errRes.Kind != audit.KindToolResult || !errRes.IsError {
		t.Fatalf("errored tool_result event mismatch: %+v", errRes)
	}

	errEv := rec.evs[3]
	if errEv.Kind != audit.KindError || errEv.ErrKind != "provider_error" || errEv.ErrMsg != "upstream 500" {
		t.Fatalf("error event mismatch: %+v", errEv)
	}

	// Seq is monotonic within the run.
	for i := 1; i < len(rec.evs); i++ {
		if rec.evs[i].Seq <= rec.evs[i-1].Seq {
			t.Fatalf("seq not monotonic: %d then %d", rec.evs[i-1].Seq, rec.evs[i].Seq)
		}
	}
}

func TestChatHandler_NilAuditIsNoOp(t *testing.T) {
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "openai/gpt-4o-mini"}},
		&stubFactory{provider: provider.NewStub("openai", nil)},
		NewChatStore(),
	)
	// No audit wired: emit methods must not panic.
	h.emitAgentToolStart("agent:main:web", "r", "agent:main:web", "tc", "bash", "{}")
	h.emitAgentToolResult("agent:main:web", "r", "agent:main:web", "tc", "bash", "ok", false)
	_ = h.emitError("agent:main:web", "r", "agent:main:web", 1, "k", "m")
}
