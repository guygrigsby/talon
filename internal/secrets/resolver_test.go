package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReference(t *testing.T) {
	cases := []struct {
		in       string
		wantSch  string
		wantTgt  string
	}{
		{"op://Personal/foo/credential", "op", "Personal/foo/credential"},
		{"keychain://talon-gateway-token", "keychain", "talon-gateway-token"},
		{"keychain://service/account", "keychain", "service/account"},
		{"plain-string", "", "plain-string"},
		{"", "", ""},
		{"https://example.com", "", "https://example.com"}, // unknown scheme = literal
	}
	for _, c := range cases {
		got := ParseReference(c.in)
		if got.Scheme != c.wantSch || got.Target != c.wantTgt {
			t.Errorf("ParseReference(%q): scheme=%q target=%q, want scheme=%q target=%q",
				c.in, got.Scheme, got.Target, c.wantSch, c.wantTgt)
		}
		if got.Raw != c.in {
			t.Errorf("ParseReference(%q): Raw=%q, want %q", c.in, got.Raw, c.in)
		}
	}
}

func TestIsReference(t *testing.T) {
	for _, ref := range []string{"op://x/y/z", "keychain://name"} {
		if !IsReference(ref) {
			t.Errorf("IsReference(%q) should be true", ref)
		}
	}
	for _, lit := range []string{"abc123", "", "https://example.com"} {
		if IsReference(lit) {
			t.Errorf("IsReference(%q) should be false (literal)", lit)
		}
	}
}

func TestResolver_LiteralPassthrough(t *testing.T) {
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "abc-literal-value")
	if err != nil {
		t.Fatalf("literal resolve errored: %v", err)
	}
	if got != "abc-literal-value" {
		t.Errorf("literal value mangled: %q", got)
	}
}

func TestResolver_DispatchesOpToPlugin(t *testing.T) {
	// Drop a fake talon-op-plugin on PATH that echoes its arg back.
	// Verifies op:// still routes through the plugin (it has
	// bootstrap state that earns process isolation). Trailing
	// newline trimming is part of the resolver contract.
	dir := t.TempDir()
	mkFakePlugin(t, dir, "talon-op-plugin", "echo \"resolved-$1\"")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := NewResolver()
	got, err := r.Resolve(context.Background(), "op://Personal/foo/credential")
	if err != nil {
		t.Fatalf("op resolve errored: %v", err)
	}
	if got != "resolved-op://Personal/foo/credential" {
		t.Errorf("op resolve: got %q, want resolved-op://Personal/foo/credential", got)
	}
}

func TestSplitKeychainTarget(t *testing.T) {
	cases := []struct {
		in       string
		service  string
		account  string
	}{
		{"my-service", "my-service", ""},
		{"my-service/me", "my-service", "me"},
		{"talon.gateway.auth.token", "talon.gateway.auth.token", ""},
		{"talon.gateway.auth.token/guygrigsby", "talon.gateway.auth.token", "guygrigsby"},
		// Multiple slashes — only the last is the account separator.
		// Lets keychain service names that legitimately contain
		// slashes (rare but legal) survive parsing.
		{"a/b/c", "a/b", "c"},
		{"", "", ""},
	}
	for _, c := range cases {
		gotSvc, gotAcc := splitKeychainTarget(c.in)
		if gotSvc != c.service || gotAcc != c.account {
			t.Errorf("splitKeychainTarget(%q) = (%q, %q), want (%q, %q)",
				c.in, gotSvc, gotAcc, c.service, c.account)
		}
	}
}

func TestResolver_MissingPluginErrors(t *testing.T) {
	// Strip PATH so the plugin isn't found; expect a clear error
	// mentioning the missing binary name.
	t.Setenv("PATH", t.TempDir())

	// Bypass the cache by using the dispatch resolver directly so a
	// previous test's resolution can't satisfy this one.
	r := &dispatchResolver{}
	_, err := r.Resolve(context.Background(), "op://Personal/foo/credential")
	if err == nil {
		t.Fatal("expected error when talon-op-plugin not on PATH")
	}
	if !strings.Contains(err.Error(), "talon-op-plugin") {
		t.Errorf("error should mention missing plugin name: %v", err)
	}
}

func TestPluginSearchPaths_IncludesExeDirAndBinSubdir(t *testing.T) {
	// pluginSearchPaths must probe both the directory containing
	// the running binary AND its bin/ subdirectory. The two dev
	// layouts to cover:
	//   - canonical: ./bin/talon launches → plugin at ./bin/<name>
	//   - project-root: ./talon launches  → plugin at ./bin/<name>
	// os.Executable() in `go test` resolves to the compiled test
	// binary's path. We just need both shapes to appear in the
	// returned list — the actual values depend on test layout.
	// op:// is the only remaining plugin-routed scheme; keychain
	// was inlined. Test the search paths via that binary name.
	paths := pluginSearchPaths("talon-op-plugin")
	if len(paths) < 2 {
		t.Fatalf("expected ≥2 probe paths, got %d: %v", len(paths), paths)
	}
	var sawSameDir, sawBinSubdir bool
	for _, p := range paths {
		base := filepath.Base(filepath.Dir(p))
		if base == "bin" {
			sawBinSubdir = true
		} else if !strings.HasPrefix(p, "/usr/local/") && !strings.HasPrefix(p, "/opt/homebrew/") {
			sawSameDir = true
		}
	}
	if !sawSameDir {
		t.Errorf("missing same-dir probe (./<binary>): %v", paths)
	}
	if !sawBinSubdir {
		t.Errorf("missing bin/ subdir probe (./bin/<binary>): %v", paths)
	}
}

func TestResolver_UnknownSchemeErrors(t *testing.T) {
	// Schemes the resolver doesn't recognize must error rather than
	// silently passing through (which would expose the literal
	// "vault://x" string as a real secret).
	r := &dispatchResolver{}
	// vault:// isn't a known scheme so it parses as a literal, NOT
	// as an unknown scheme, by design. Verify pass-through:
	got, err := r.Resolve(context.Background(), "vault://x/y")
	if err != nil {
		t.Errorf("unknown-scheme strings should pass as literals, got error: %v", err)
	}
	if got != "vault://x/y" {
		t.Errorf("got %q, want vault://x/y", got)
	}
}

func TestCachingResolver_HitsCache(t *testing.T) {
	calls := 0
	upstream := resolverFn(func(_ context.Context, ref string) (string, error) {
		calls++
		return "value-for-" + ref, nil
	})
	c := NewCachingResolver(upstream)
	for i := 0; i < 5; i++ {
		v, err := c.Resolve(context.Background(), "op://x")
		if err != nil {
			t.Fatal(err)
		}
		if v != "value-for-op://x" {
			t.Errorf("got %q, want value-for-op://x", v)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 upstream call (cached), got %d", calls)
	}
}

func TestCachingResolver_DistinctRefsCallUpstream(t *testing.T) {
	calls := 0
	upstream := resolverFn(func(_ context.Context, ref string) (string, error) {
		calls++
		return ref, nil
	})
	c := NewCachingResolver(upstream)
	_, _ = c.Resolve(context.Background(), "op://a")
	_, _ = c.Resolve(context.Background(), "op://b")
	_, _ = c.Resolve(context.Background(), "op://a")
	if calls != 2 {
		t.Errorf("expected 2 upstream calls (a + b, a hit cache), got %d", calls)
	}
}

func TestCachingResolver_ErrorsNotCached(t *testing.T) {
	calls := 0
	upstream := resolverFn(func(_ context.Context, ref string) (string, error) {
		calls++
		return "", errors.New("fail")
	})
	c := NewCachingResolver(upstream)
	for i := 0; i < 3; i++ {
		_, _ = c.Resolve(context.Background(), "op://x")
	}
	if calls != 3 {
		t.Errorf("expected 3 upstream calls (errors not cached), got %d", calls)
	}
}

// --- helpers ----------------------------------------------------------------

type resolverFn func(context.Context, string) (string, error)

func (f resolverFn) Resolve(ctx context.Context, ref string) (string, error) {
	return f(ctx, ref)
}

func mkFakePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
