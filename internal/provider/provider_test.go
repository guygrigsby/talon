package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestModelID_Split(t *testing.T) {
	cases := []struct {
		in       ModelID
		provider string
		model    string
	}{
		{"openai/gpt-5.4-mini", "openai", "gpt-5.4-mini"},
		{"anthropic/claude-opus-4-7", "anthropic", "claude-opus-4-7"},
		{"deepseek/deepseek-reasoner", "deepseek", "deepseek-reasoner"},
		{"plain-no-slash", "", "plain-no-slash"},
		{"", "", ""},
		{"openai/gpt-5.4-mini/extra", "openai", "gpt-5.4-mini/extra"}, // anything after first slash is the model
	}
	for _, tc := range cases {
		if got := tc.in.Provider(); got != tc.provider {
			t.Errorf("Provider(%q) = %q, want %q", tc.in, got, tc.provider)
		}
		if got := tc.in.Model(); got != tc.model {
			t.Errorf("Model(%q) = %q, want %q", tc.in, got, tc.model)
		}
	}
}

func TestStub_StreamReplaysScriptInOrder(t *testing.T) {
	script := []Delta{
		{Kind: DeltaText, Text: "Hello"},
		{Kind: DeltaText, Text: ", "},
		{Kind: DeltaText, Text: "world"},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: 4, OutputTokens: 3}},
	}
	p := NewStub("openai", script)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := p.Stream(ctx, Request{Model: "openai/gpt-5.4-mini"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var b strings.Builder
	var sawUsage *Usage
	count := 0
	for d := range ch {
		count++
		switch d.Kind {
		case DeltaText:
			b.WriteString(d.Text)
		case DeltaUsage:
			sawUsage = d.Usage
		}
	}
	if count != 4 {
		t.Errorf("got %d deltas, want 4", count)
	}
	if got := b.String(); got != "Hello, world" {
		t.Errorf("assembled text = %q, want %q", got, "Hello, world")
	}
	if sawUsage == nil || sawUsage.InputTokens != 4 || sawUsage.OutputTokens != 3 {
		t.Errorf("usage delta missing or wrong: %+v", sawUsage)
	}
	if calls := p.Calls(); len(calls) != 1 || calls[0].Model != "openai/gpt-5.4-mini" {
		t.Errorf("Calls() = %+v, want one call recorded with the request model", calls)
	}
}

func TestStub_StreamClosesChannelOnError(t *testing.T) {
	streamErr := errors.New("upstream 503")
	script := []Delta{
		{Kind: DeltaText, Text: "partial"},
		{Kind: DeltaError, Err: streamErr},
		{Kind: DeltaText, Text: "should not arrive"},
	}
	p := NewStub("openai", script)
	ch, err := p.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	var got []Delta
	for d := range ch {
		got = append(got, d)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 deltas (text + error) before close, got %d: %+v", len(got), got)
	}
	if got[1].Kind != DeltaError || !errors.Is(got[1].Err, streamErr) {
		t.Errorf("second delta should be the error: %+v", got[1])
	}
}

func TestStub_StreamHonorsContextCancel(t *testing.T) {
	// Long script that would otherwise drain entirely.
	script := make([]Delta, 100)
	for i := range script {
		script[i] = Delta{Kind: DeltaText, Text: "tok"}
	}
	p := NewStub("openai", script)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, Request{})
	if err != nil {
		t.Fatal(err)
	}
	// Read one delta then cancel.
	if _, ok := <-ch; !ok {
		t.Fatalf("channel closed before any delta arrived")
	}
	cancel()
	// Drain — channel must close promptly. Timeout if it doesn't.
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatalf("Stub did not close channel after context cancel within 2s")
	}
}

// staticAssertion: the package compiles only if Stub satisfies Provider.
var _ Provider = (*Stub)(nil)
