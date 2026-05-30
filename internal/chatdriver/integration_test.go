//go:build integration

// Integration tests for the chatdriver-based chat handler. Build-
// tagged so they don't run on `go test ./...` — each test hits a
// real provider API and costs real money (cents). Invoke with:
//
//	go test -tags=integration ./internal/chatdriver/ -v
//	  -run TestIntegration -timeout 120s
//
// Tests skip themselves when their provider's auth can't be
// resolved (no op-plugin / keychain / env var). This means CI
// without secrets just sees them as skipped, not failed.

package chatdriver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// integrationConfig loads the user's actual merged config so
// integration tests use the same auth + base URLs as production.
func integrationConfig(t *testing.T) ([]byte, talonpath.Paths) {
	t.Helper()
	paths := talonpath.DefaultPaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	return merged, paths
}

// runProbe fires the prompt against the named model via
// NewChatRunner and reports latency + result text. Skips when auth
// resolution returns no key for the provider. Uses the user's real
// auth resolved from the merged config (including op:// refs).
func runProbe(t *testing.T, providerName, modelID, prompt string) (final string, ttfb, total time.Duration, err error) {
	t.Helper()
	merged, paths := integrationConfig(t)

	auth := ResolveProviderAuth(merged, paths)
	if _, ok := auth[providerName]; !ok {
		t.Skipf("no resolved auth for %q", providerName)
	}

	runner := NewChatRunner(paths, nil, nil)

	var (
		finalText    string
		firstDelta   time.Time
		mu           sync.Mutex
		ttfbRecorded bool
		start        = time.Now()
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, runErr := runner(
		ctx,
		"main",                    // agentID — use "main" so auth resolves from real config
		"integration",             // sessionKey
		"run-1",                   // runID
		prompt,
		providerName+"/"+modelID,  // selectedModelID overrides config model
		nil,                       // no prior history
		func(seq int, state, full, delta string) {
			mu.Lock()
			defer mu.Unlock()
			if !ttfbRecorded && (state == "delta" || state == "thinking") && delta != "" {
				firstDelta = time.Now()
				ttfbRecorded = true
			}
			if state == "final" {
				finalText = full
			}
			t.Logf("text event: seq=%d state=%q delta-len=%d full-len=%d",
				seq, state, len(delta), len(full))
		},
		func(toolCallID, name, args string) {
			t.Logf("tool start: id=%q name=%q args-len=%d", toolCallID, name, len(args))
		},
		func(toolCallID, name, output string, isErr bool) {
			t.Logf("tool result: id=%q name=%q out-len=%d isErr=%v", toolCallID, name, len(output), isErr)
		},
		func(seq int, kind, msg string) {
			t.Logf("error event: seq=%d kind=%q msg=%q", seq, kind, msg)
		},
	)

	total = time.Since(start)
	if ttfbRecorded {
		ttfb = firstDelta.Sub(start)
	}

	// Prefer the streamed final text; fall back to res.FinalText.
	if finalText != "" {
		final = finalText
	} else {
		final = res.FinalText
	}

	return final, ttfb, total, runErr
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

func TestIntegration_OpenAI_GPT54Mini(t *testing.T) {
	final, ttfb, total, err := runProbe(t, "openai", "gpt-5.4-mini", "Reply with the single word: ok")
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if !strings.Contains(strings.ToLower(final), "ok") {
		t.Errorf("response should contain 'ok', got len=%d", len(final))
	}
	t.Logf("openai/gpt-5.4-mini  ttfb=%v  total=%v  reply-len=%d",
		ttfb.Truncate(time.Millisecond), total.Truncate(time.Millisecond), len(final))
}

func TestIntegration_Anthropic_Haiku(t *testing.T) {
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
