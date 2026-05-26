//go:build integration

// Integration tests for the agentcore-based chat handler. Build-
// tagged so they don't run on `go test ./...` — each test hits a
// real provider API and costs real money (cents). Invoke with:
//
//	go test -tags=integration ./internal/agentcore_chat/ -v
//	  -run TestIntegration -timeout 120s
//
// Tests skip themselves when their provider's auth can't be
// resolved (no op-plugin / keychain / env var). This means CI
// without secrets just sees them as skipped, not failed.

package agentcore_chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
)

// integrationConfig loads the user's actual merged config so
// integration tests use the same auth + base URLs as production.
func integrationConfig(t *testing.T) ([]byte, openclaw.Paths) {
	t.Helper()
	paths := openclaw.DefaultPaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	return merged, paths
}

// silentSink discards every event except the final reply and any
// errors. Captures the first error so test failures surface the
// real reason (provider 400, auth issue, etc.).
type silentSink struct {
	final   string
	errKind string
	errMsg  string
}

func (s *silentSink) Delta(string, string)                    {}
func (s *silentSink) Thinking(string, string)                 {}
func (s *silentSink) Final(full string)                       { s.final = full }
func (s *silentSink) ToolStart(string, string, string)        {}
func (s *silentSink) ToolResult(string, string, string, bool) {}
func (s *silentSink) Error(kind, msg string) {
	if s.errKind == "" {
		s.errKind = kind
		s.errMsg = msg
	}
}

// ttfbSink wraps another sink and records time-to-first-byte
// (first text or thinking delta).
type ttfbSink struct {
	inner EventSink
	start time.Time
	ttfb  time.Duration
}

func (s *ttfbSink) Delta(full, delta string) {
	if s.ttfb == 0 {
		s.ttfb = time.Since(s.start)
	}
	s.inner.Delta(full, delta)
}
func (s *ttfbSink) Thinking(full, delta string) {
	if s.ttfb == 0 {
		s.ttfb = time.Since(s.start)
	}
	s.inner.Thinking(full, delta)
}
func (s *ttfbSink) Final(full string)               { s.inner.Final(full) }
func (s *ttfbSink) ToolStart(id, name, args string) { s.inner.ToolStart(id, name, args) }
func (s *ttfbSink) ToolResult(id, name, out string, isErr bool) {
	s.inner.ToolResult(id, name, out, isErr)
}
func (s *ttfbSink) Error(kind, msg string) { s.inner.Error(kind, msg) }

// runProbe fires the prompt against the named model and reports
// latency + result text. Skips when auth resolution returns no key
// for the provider. Uses the user's real auth (resolved from the
// merged config, including op:// refs) but a synthetic agents.*
// block so each test can pick its own model without mutating the
// real config file.
//
// The agent is built via Builder + driven via the same EventAdapter
// the gateway runner uses, so this tests the agentcore path
// end-to-end against a real provider.
func runProbe(t *testing.T, providerName, modelID, prompt string) (final string, ttfb, total time.Duration, err error) {
	t.Helper()
	merged, paths := integrationConfig(t)

	auth := ResolveProviderAuth(merged, paths)
	if _, ok := auth[providerName]; !ok {
		t.Skipf("no resolved auth for %q", providerName)
	}

	syntheticCfg := []byte(`{
		"agents": {
			"defaults": {"model": {"primary": "` + providerName + `/` + modelID + `"}, "workspace": "` + t.TempDir() + `"},
			"list": [{"id": "main"}]
		}
	}`)

	agent, choice, err := NewBuilder(syntheticCfg, paths).WithAuthOverride(auth).BuildAgent("main")
	if err != nil {
		return "", 0, 0, err
	}
	if choice.Provider != providerName {
		t.Fatalf("BuildAgent resolved %q, expected %q", choice.Provider, providerName)
	}

	silent := &silentSink{}
	sink := &ttfbSink{inner: silent, start: time.Now()}

	adapter := NewEventAdapter(sink)
	unsub := agent.Subscribe(func(ev agentcore.Event) {
		errStr := ""
		if ev.Err != nil {
			errStr = ev.Err.Error()
		}
		role := ""
		if ev.Message != nil {
			role = string(ev.Message.GetRole())
		}
		t.Logf("event: type=%s role=%q deltaKind=%q delta-len=%d msg-len=%d tool=%q err=%q",
			ev.Type, role, ev.DeltaKind, len(ev.Delta),
			textLen(ev.Message),
			ev.Tool,
			errStr)
		adapter.Handle(ev)
	})
	defer unsub()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	if err := agent.Prompt(prompt); err != nil {
		return "", 0, time.Since(start), err
	}

	done := make(chan struct{})
	go func() {
		agent.WaitForIdle()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		agent.Abort()
		<-done
		return "", sink.ttfb, time.Since(start), ctx.Err()
	}

	total = time.Since(start)
	ttfb = sink.ttfb
	final = silent.final
	if final == "" {
		// Some providers might emit no delta and rely on the
		// final-message fallback in the adapter; pull from the
		// adapter's snapshot.
		acc, _ := adapter.Snapshot()
		final = acc
	}
	// Surface any error event the adapter saw — when the upstream
	// API returns 400 / auth / network errors, the adapter calls
	// sink.Error and we need that text in the test report rather
	// than a generic "reply-len=0" diagnostic.
	if final == "" && silent.errMsg != "" {
		err = fmt.Errorf("%s: %s", silent.errKind, silent.errMsg)
	}
	return final, ttfb, total, err
}

// textLen safely returns the assistant message's text length even
// when ev.Message is nil. Used by the debug logger.
func textLen(m agentcore.AgentMessage) int {
	if m == nil {
		return 0
	}
	return len(m.TextContent())
}

func TestIntegration_OpenAI_GPT4oMini(t *testing.T) {
	final, ttfb, total, err := runProbe(t, "openai", "gpt-4o-mini", "Reply with the single word: ok")
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !strings.Contains(strings.ToLower(final), "ok") {
		t.Errorf("response should contain 'ok', got len=%d", len(final))
	}
	t.Logf("openai/gpt-4o-mini  ttfb=%v  total=%v  reply-len=%d",
		ttfb.Truncate(time.Millisecond), total.Truncate(time.Millisecond), len(final))
}

func TestIntegration_Anthropic_Haiku(t *testing.T) {
	// SKIPPED: agentcore's litellm client auto-fills top_p=1.0 from
	// its default config when the agentcore layer didn't set it.
	// Anthropic's Messages API rejects requests with both
	// `temperature` and `top_p` set ("cannot both be specified for
	// this model"). Needs an upstream patch in litellm
	// (providers/anthropic.go) to drop top_p when temperature is
	// present. Tracked in docs/migration-agentcore.md Phase 5.
	t.Skip("agentcore + anthropic blocked on upstream litellm patch: top_p / temperature conflict")
	final, ttfb, total, err := runProbe(t, "anthropic", "claude-haiku-4-5", "Reply with the single word: ok")
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !strings.Contains(strings.ToLower(final), "ok") {
		t.Errorf("response should contain 'ok', got len=%d", len(final))
	}
	t.Logf("anthropic/claude-haiku-4-5  ttfb=%v  total=%v  reply-len=%d",
		ttfb.Truncate(time.Millisecond), total.Truncate(time.Millisecond), len(final))
}

func TestIntegration_DeepSeek_Chat(t *testing.T) {
	final, ttfb, total, err := runProbe(t, "deepseek", "deepseek-chat", "Reply with the single word: ok")
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !strings.Contains(strings.ToLower(final), "ok") {
		t.Errorf("response should contain 'ok', got len=%d", len(final))
	}
	t.Logf("deepseek/deepseek-chat  ttfb=%v  total=%v  reply-len=%d",
		ttfb.Truncate(time.Millisecond), total.Truncate(time.Millisecond), len(final))
}

// TestIntegration_Mistral_Small covers the openai-compat tenant
// path (litellm registers mistral via providers_init.go since
// LiteLLM upstream doesn't ship it). Catches breaks specific to
// the custom-base-URL path — a different shape from openai +
// deepseek which use LiteLLM's first-class provider entries.
func TestIntegration_Mistral_Small(t *testing.T) {
	// Using the canonical `-latest` alias rather than a dated
	// version id. The dated IDs from the docs page (mistral-small-
	// 4-0-26-03 etc.) don't always resolve on the real API; the
	// floating aliases do. Reveals a separate concern: the user's
	// talon.json lists dated IDs that won't work for chat dispatch
	// either. Documenting via this test.
	final, ttfb, total, err := runProbe(t, "mistral", "mistral-small-latest", "Reply with the single word: ok")
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !strings.Contains(strings.ToLower(final), "ok") {
		t.Errorf("response should contain 'ok', got len=%d", len(final))
	}
	t.Logf("mistral/mistral-small-latest  ttfb=%v  total=%v  reply-len=%d",
		ttfb.Truncate(time.Millisecond), total.Truncate(time.Millisecond), len(final))
}
