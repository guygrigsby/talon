package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRenderModelsTestReport_SummaryAndRows(t *testing.T) {
	var buf bytes.Buffer
	renderModelsTestReport(&buf, []modelTestResult{
		{Provider: "openai", ID: "gpt-4o-mini", TTFB: 800 * time.Millisecond, Total: 1200 * time.Millisecond, OutputTokens: 3, OK: true, Status: "ok"},
		{Provider: "openai", ID: "gpt-5.4-mini", Status: "openai: http 404: model not found", Total: 300 * time.Millisecond},
		{Provider: "mistral", ID: "mistral-large-3-25-12", Skipped: true, Status: "no API key resolved"},
	})
	out := buf.String()
	for _, want := range []string{"gpt-4o-mini", "800ms", "1.2s", "ok", "gpt-5.4-mini", "404", "mistral-large-3-25-12", "no API key resolved", "1 ok, 1 fail, 1 skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestTrimErr_CollapsesNewlinesAndCaps(t *testing.T) {
	if got := trimErr("hello\nworld"); got != "hello world" {
		t.Errorf("newlines should collapse: %q", got)
	}
	long := strings.Repeat("x", 300)
	got := trimErr(long)
	// 200 ASCII chars + multi-byte ellipsis. Verify the truncation
	// happened, not exact byte count.
	if !strings.HasSuffix(got, "…") || strings.Count(got, "x") != 200 {
		t.Errorf("expected 200 x's + ellipsis, got %d bytes: %q", len(got), got[len(got)-10:])
	}
}

func TestIsLoopbackBase_PicksLoopback(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8080/v1": true,
		"http://127.0.0.1:1234/v1": true,
		"http://[::1]:8080/v1":     true,
		"https://api.openai.com/v1": false,
		"http://example.com/v1":    false,
	}
	for u, want := range cases {
		if got := isLoopbackBase(u); got != want {
			t.Errorf("isLoopbackBase(%q) = %v, want %v", u, got, want)
		}
	}
}
