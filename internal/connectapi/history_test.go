package connectapi

import "testing"

// History translation tests stand on the row fixtures the chat.history
// handler produces today. Covers the real role variants plus the
// pure-tool-use shortcut and the unknown-role drop.

func TestHistoryRowToProto_UserRow(t *testing.T) {
	row := chatHistoryRow{
		Meta: chatHistoryMeta{ID: "u1", Seq: 1},
		Role: "user",
		Content: []chatHistoryContent{
			{Type: "text", Text: "hi"},
		},
	}
	got := historyRowToProto(row)
	if got == nil {
		t.Fatal("user row should not be dropped")
	}
	if got.GetId() != "u1" || got.GetSeq() != 1 {
		t.Errorf("meta wrong: id=%q seq=%d", got.GetId(), got.GetSeq())
	}
	u := got.GetUser()
	if u == nil {
		t.Fatalf("expected User variant, got %T", got.GetBody())
	}
	if u.GetText() != "hi" {
		t.Errorf("text = %q, want %q", u.GetText(), "hi")
	}
}

func TestHistoryRowToProto_AssistantText(t *testing.T) {
	row := chatHistoryRow{
		Meta: chatHistoryMeta{ID: "a1", Seq: 2},
		Role: "assistant",
		Content: []chatHistoryContent{
			{Type: "text", Text: "ack"},
		},
	}
	got := historyRowToProto(row)
	a := got.GetAssistant()
	if a == nil {
		t.Fatalf("expected Assistant variant, got %T", got.GetBody())
	}
	if a.GetText() != "ack" {
		t.Errorf("text = %q", a.GetText())
	}
	if len(a.GetToolUses()) != 0 {
		t.Errorf("text-only assistant turn should have no tool uses; got %d", len(a.GetToolUses()))
	}
}

func TestHistoryRowToProto_AssistantWithToolUses(t *testing.T) {
	row := chatHistoryRow{
		Meta: chatHistoryMeta{ID: "a2", Seq: 3},
		Role: "assistant",
		Content: []chatHistoryContent{
			{Type: "text", Text: "let me check"},
			{Type: "tool_use", ID: "call_a", Name: "glob", Input: []byte(`{"pattern":"*.go"}`)},
			{Type: "tool_use", ID: "call_b", Name: "grep", Input: []byte(`{"q":"x"}`)},
		},
	}
	got := historyRowToProto(row)
	a := got.GetAssistant()
	if a == nil {
		t.Fatalf("multi-tool assistant turn should stay Assistant, got %T", got.GetBody())
	}
	if a.GetText() != "let me check" {
		t.Errorf("text = %q", a.GetText())
	}
	if len(a.GetToolUses()) != 2 {
		t.Fatalf("expected 2 tool uses, got %d", len(a.GetToolUses()))
	}
	if a.GetToolUses()[0].GetName() != "glob" || a.GetToolUses()[1].GetName() != "grep" {
		t.Errorf("tool names = %q,%q", a.GetToolUses()[0].GetName(), a.GetToolUses()[1].GetName())
	}
	if a.GetToolUses()[0].GetArgsJson() != `{"pattern":"*.go"}` {
		t.Errorf("args_json passthrough wrong: %q", a.GetToolUses()[0].GetArgsJson())
	}
}

// Pure-tool-use turns (no visible assistant text, single tool
// call) take the ToolUse shortcut variant so the FE doesn't render
// an empty assistant bubble.
func TestHistoryRowToProto_PureToolUseShortcut(t *testing.T) {
	row := chatHistoryRow{
		Meta: chatHistoryMeta{ID: "a3", Seq: 4},
		Role: "assistant",
		Content: []chatHistoryContent{
			{Type: "tool_use", ID: "call_x", Name: "bash", Input: []byte(`{"cmd":"ls"}`)},
		},
	}
	got := historyRowToProto(row)
	tu := got.GetToolUse()
	if tu == nil {
		t.Fatalf("single-tool-only turn should use ToolUse shortcut, got %T", got.GetBody())
	}
	if tu.GetName() != "bash" || tu.GetToolCallId() != "call_x" {
		t.Errorf("tool fields wrong: name=%q id=%q", tu.GetName(), tu.GetToolCallId())
	}
}

func TestHistoryRowToProto_ToolResult(t *testing.T) {
	row := chatHistoryRow{
		Meta:       chatHistoryMeta{ID: "t1", Seq: 5},
		Role:       "toolResult",
		ToolCallID: "call_x",
		ToolName:   "bash",
		Content: []chatHistoryContent{
			{Type: "text", Text: "exit 0"},
		},
	}
	got := historyRowToProto(row)
	tr := got.GetToolResult()
	if tr == nil {
		t.Fatalf("expected ToolResult variant, got %T", got.GetBody())
	}
	if tr.GetToolCallId() != "call_x" || tr.GetName() != "bash" || tr.GetOutput() != "exit 0" {
		t.Errorf("tool result fields wrong: %+v", tr)
	}
}

// Unknown roles drop. System messages aren't persisted today; the
// drop keeps the surface honest if a future role appears before
// the proto catches up.
func TestHistoryRowToProto_UnknownRoleDrops(t *testing.T) {
	row := chatHistoryRow{
		Meta: chatHistoryMeta{ID: "x1", Seq: 6},
		Role: "system",
		Content: []chatHistoryContent{
			{Type: "text", Text: "you are a helpful assistant"},
		},
	}
	if got := historyRowToProto(row); got != nil {
		t.Errorf("unknown role should be dropped; got %+v", got)
	}
}

// firstText returns the first text block or empty — used by user
// and toolResult variants whose content is a flat single-text-block.
func TestFirstText(t *testing.T) {
	if got := firstText(nil); got != "" {
		t.Errorf("nil content: got %q, want empty", got)
	}
	parts := []chatHistoryContent{
		{Type: "tool_use", Name: "skip"},
		{Type: "text", Text: "match"},
		{Type: "text", Text: "second"},
	}
	if got := firstText(parts); got != "match" {
		t.Errorf("got %q, want %q", got, "match")
	}
}
