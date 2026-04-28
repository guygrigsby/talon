package pb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// These tests don't exercise gRPC transport (that's the host runtime's
// job, talon-bql). They just confirm the generated stubs compile and
// roundtrip correctly so a bad regeneration breaks loudly.

func TestManifest_RoundTrip(t *testing.T) {
	m := &Manifest{
		Name:        "telegram",
		Version:     "0.1.0",
		Description: "Telegram channel adapter",
		OffersChannels: []string{"telegram"},
		OffersTools: []*ToolSpec{
			{Name: "send_telegram_dm", Description: "DM a user", ParametersSchema: []byte(`{"type":"object"}`)},
		},
		Needs: []Capability{
			Capability_CAPABILITY_READ_CONFIG,
			Capability_CAPABILITY_SEND_CHANNEL_MESSAGE,
			Capability_CAPABILITY_LIST_AGENTS,
		},
	}
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got Manifest
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != m.Name || got.Version != m.Version {
		t.Errorf("roundtrip name/version drift: %+v", &got)
	}
	if len(got.Needs) != 3 {
		t.Errorf("needs len = %d, want 3", len(got.Needs))
	}
	if len(got.OffersTools) != 1 || got.OffersTools[0].Name != "send_telegram_dm" {
		t.Errorf("offers_tools roundtrip wrong: %+v", got.OffersTools)
	}
}

func TestDelta_OneofVariants(t *testing.T) {
	cases := []*Delta{
		{Kind: &Delta_Text{Text: "hello"}},
		{Kind: &Delta_Reasoning{Reasoning: "thinking..."}},
		{Kind: &Delta_Usage{Usage: &Usage{InputTokens: 12, OutputTokens: 7}}},
		{Kind: &Delta_ToolCall{ToolCall: &ToolCall{Id: "call_1", Name: "bash", ArgumentsJson: `{}`}}},
		{Kind: &Delta_Error{Error: "rate limit"}},
	}
	for i, want := range cases {
		raw, err := proto.Marshal(want)
		if err != nil {
			t.Fatalf("case %d: marshal: %v", i, err)
		}
		var got Delta
		if err := proto.Unmarshal(raw, &got); err != nil {
			t.Fatalf("case %d: unmarshal: %v", i, err)
		}
		if got.GetText() != want.GetText() ||
			got.GetReasoning() != want.GetReasoning() ||
			got.GetError() != want.GetError() {
			t.Errorf("case %d roundtrip drift: got=%+v want=%+v", i, &got, want)
		}
	}
}

func TestMessage_ToolTurnRoundTrip(t *testing.T) {
	// Assistant turn carrying a tool_call → tool result turn pointing
	// back at it. Same shape provider.Message uses internally.
	asst := &Message{
		Role:    Role_ROLE_ASSISTANT,
		Content: "let me check",
		ToolCalls: []*ToolCall{
			{Id: "call_a", Name: "glob", ArgumentsJson: `{"pattern":"*"}`},
		},
	}
	res := &Message{
		Role:       Role_ROLE_TOOL,
		Content:    "main.go\n",
		ToolCallId: "call_a",
	}
	for _, m := range []*Message{asst, res} {
		raw, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var got Message
		if err := proto.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Role != m.Role || got.Content != m.Content {
			t.Errorf("drift: got=%+v want=%+v", &got, m)
		}
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Name != "glob" {
		t.Errorf("assistant tool_calls drift")
	}
}

// Closed-set sanity: the Capability enum names match the documented set
// so a typo or reorder fails loudly.
func TestCapabilityEnum_Stable(t *testing.T) {
	want := map[string]Capability{
		"CAPABILITY_READ_CONFIG":          Capability_CAPABILITY_READ_CONFIG,
		"CAPABILITY_READ_CHAT_HISTORY":    Capability_CAPABILITY_READ_CHAT_HISTORY,
		"CAPABILITY_READ_AGENT_IDENTITY":  Capability_CAPABILITY_READ_AGENT_IDENTITY,
		"CAPABILITY_LIST_AGENTS":          Capability_CAPABILITY_LIST_AGENTS,
		"CAPABILITY_LIST_SESSIONS":        Capability_CAPABILITY_LIST_SESSIONS,
		"CAPABILITY_LIST_MODELS":          Capability_CAPABILITY_LIST_MODELS,
		"CAPABILITY_APPEND_MEMORY":        Capability_CAPABILITY_APPEND_MEMORY,
		"CAPABILITY_PATCH_SESSION":        Capability_CAPABILITY_PATCH_SESSION,
		"CAPABILITY_RUN_SUBAGENT":         Capability_CAPABILITY_RUN_SUBAGENT,
		"CAPABILITY_SEND_CHANNEL_MESSAGE": Capability_CAPABILITY_SEND_CHANNEL_MESSAGE,
	}
	for name, want := range want {
		if Capability(want).String() != name {
			t.Errorf("capability %v stringifies as %q, want %q", want, Capability(want), name)
		}
	}
}
