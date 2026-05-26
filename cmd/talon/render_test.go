package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderModels_TabularOutput(t *testing.T) {
	payload := []byte(`{
		"models": [
			{"id":"claude-opus-4-7","name":"Claude Opus 4.7","provider":"anthropic","contextWindow":1000000,"input":["text","image"],"reasoning":true,"alias":"opus"},
			{"id":"claude-haiku-4-5","name":"Claude Haiku 4.5","provider":"anthropic","contextWindow":200000,"input":["text","image"],"reasoning":true},
			{"id":"deepseek-chat","name":"DeepSeek Chat","provider":"deepseek","contextWindow":131072,"input":["text"],"reasoning":false}
		]
	}`)
	var buf bytes.Buffer
	if err := renderModels(&buf, payload); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Header
	if !strings.Contains(out, "ID") || !strings.Contains(out, "MODALITIES") || !strings.Contains(out, "CTX") {
		t.Errorf("missing header columns:\n%s", out)
	}
	// Sorted by ID, so haiku before opus.
	haikuIdx := strings.Index(out, "claude-haiku-4-5")
	opusIdx := strings.Index(out, "claude-opus-4-7")
	if haikuIdx < 0 || opusIdx < 0 || haikuIdx > opusIdx {
		t.Errorf("rows not sorted by id:\n%s", out)
	}
	// Modality compaction.
	if !strings.Contains(out, "text,image") || !strings.Contains(out, "text  ") {
		t.Errorf("modality column wrong:\n%s", out)
	}
	// Context window compaction (1M, 200K, 128K).
	for _, want := range []string{"1M", "200K", "128K"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing ctx %q:\n%s", want, out)
		}
	}
	// Reasoning glyph + alias dash.
	if !strings.Contains(out, "yes") || !strings.Contains(out, "no") {
		t.Errorf("reasoning column wrong:\n%s", out)
	}
	if !strings.Contains(out, "opus") {
		t.Errorf("alias column missing:\n%s", out)
	}
}

func TestRenderModels_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	if err := renderModels(&buf, []byte(`{"models":[]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(no models)") {
		t.Errorf("empty fallback missing:\n%s", buf.String())
	}
}

func TestRenderAgents_DefaultFirstThenAlpha(t *testing.T) {
	payload := []byte(`{
		"agents": [
			{"id":"research","model":{"primary":"anthropic/claude-sonnet-4-6","fallbacks":["a","b"]},"workspace":"/Users/x/.talon"},
			{"id":"coding","model":{"primary":"anthropic/claude-opus-4-7","fallbacks":["a"]},"workspace":"/Users/x/.talon"},
			{"id":"main","model":{"primary":"openai/gpt-5.4-mini","fallbacks":["a","b","c"]},"workspace":"/Users/x/.talon"}
		],
		"defaultId":"main"
	}`)
	var buf bytes.Buffer
	if err := renderAgents(&buf, payload); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// "main" must appear before any other row (it's the default).
	mainIdx := strings.Index(out, "main (default)")
	codingIdx := strings.Index(out, "coding")
	researchIdx := strings.Index(out, "research")
	if mainIdx < 0 {
		t.Fatalf("default agent not flagged:\n%s", out)
	}
	if !(mainIdx < codingIdx && codingIdx < researchIdx) {
		t.Errorf("agents not ordered (default, then alpha):\n%s", out)
	}
	// FALLBACKS column shows count, not full list.
	if !strings.Contains(out, "  3  ") {
		t.Errorf("fallback count column wrong:\n%s", out)
	}
}

func TestRenderAgents_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	if err := renderAgents(&buf, []byte(`{"agents":[]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(no agents)") {
		t.Errorf("empty fallback missing:\n%s", buf.String())
	}
}

func TestFormatCtx(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "-"},
		{-1, "-"},
		{500, "500"},
		{1000, "1K"},
		{200000, "200K"},
		{131072, "128K"},   // odd-sized but a familiar 128K window
		{1_000_000, "1M"},
		{2_000_000, "2M"},
	}
	for _, tc := range cases {
		if got := formatCtx(tc.in); got != tc.want {
			t.Errorf("formatCtx(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
