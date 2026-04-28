package deepseek

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/provider"
)

const recordedSSE = `data: {"id":"ds-1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"ds-1","choices":[{"index":0,"delta":{"content":"Hi"}}]}

data: {"id":"ds-1","choices":[{"index":0,"delta":{"content":" there"}}]}

data: {"id":"ds-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func TestNew_NameAndProviderKey(t *testing.T) {
	p := New(Options{APIKey: "sk-test"})
	if p.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", p.Name())
	}
}

func TestStream_RejectsNonDeepSeekModelID(t *testing.T) {
	p := New(Options{APIKey: "sk-test"})
	_, err := p.Stream(context.Background(), provider.Request{Model: "openai/gpt-4"})
	if err == nil || !strings.Contains(err.Error(), "deepseek") || !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("expected provider-mismatch error mentioning deepseek, got %v", err)
	}
}

func TestStream_AcceptsDeepSeekModelIDAndHitsConfiguredBaseURL(t *testing.T) {
	var hitURL, authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitURL = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(recordedSSE))
	}))
	defer srv.Close()

	p := New(Options{APIKey: "sk-deepseek", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := p.Stream(ctx, provider.Request{
		Model:    "deepseek/deepseek-reasoner",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "yo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for d := range ch {
		if d.Kind == provider.DeltaText {
			b.WriteString(d.Text)
		}
	}
	if got := b.String(); got != "Hi there" {
		t.Errorf("assembled text = %q, want %q", got, "Hi there")
	}
	if hitURL != "/chat/completions" {
		t.Errorf("hit unexpected path: %q", hitURL)
	}
	if authHeader != "Bearer sk-deepseek" {
		t.Errorf("auth header = %q", authHeader)
	}
}

func TestLoadAPIKey_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{
		"version": 1,
		"profiles": {
			"deepseek:default": {"type":"api_key","provider":"deepseek","key":"sk-real"},
			"openai:default":   {"type":"api_key","provider":"openai","key":"sk-other"}
		}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAPIKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-real" {
		t.Errorf("got %q, want sk-real (must read the deepseek:default profile, not openai:default)", got)
	}
}

func TestLoadAPIKey_RejectsWrongProvider(t *testing.T) {
	// A profile under deepseek:default that claims provider=openai must
	// be rejected — caller-side mistake we want to fail loudly.
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	body := `{"version":1,"profiles":{"deepseek:default":{"type":"api_key","provider":"openai","key":"sk-x"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAPIKey(path)
	if err == nil || !strings.Contains(err.Error(), "want deepseek") {
		t.Errorf("expected provider-mismatch error, got %v", err)
	}
}

func TestLoadAPIKey_MissingProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-profiles.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"profiles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAPIKey(path)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

// Compile-time interface assertion via the openai.Provider it returns.
func TestProviderInterfaceSatisfied(t *testing.T) {
	var _ provider.Provider = New(Options{APIKey: "x"})
}
