package main

import (
	"strings"
	"testing"
)

// TestLogSafeURL_RedactsTokenFragment covers the leak: when the
// gateway prints "UI: <url>" at startup, a resolved cleartext
// token in the fragment lands in stdout (Docker logs, terminal
// scrollback, anything tail -f'd). logSafeURL must strip the
// token's value while leaving the rest of the URL diagnostic.
func TestLogSafeURL_RedactsTokenFragment(t *testing.T) {
	in := "http://localhost:5173/chat?session=main#gatewayUrl=ws%3A%2F%2Flocalhost%3A18789&token=d5269e9cb6e33b939a62c03d90a488dfe77208a590bea195"
	out := logSafeURL(in)
	if strings.Contains(out, "d5269e9cb6") {
		t.Errorf("token leaked: %s", out)
	}
	// Diagnostic shape preserved — gatewayUrl + path + session
	// remain so the operator can still see WHERE talon is
	// pointing the UI.
	for _, want := range []string{"localhost:5173", "session=main", "gatewayUrl=", "token=[set]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in redacted output: %s", want, out)
		}
	}
}

func TestLogSafeURL_NoTokenPassesThrough(t *testing.T) {
	in := "http://localhost:5173/chat?session=main"
	out := logSafeURL(in)
	if out != in {
		t.Errorf("URL with no token should pass unchanged: got %q want %q", out, in)
	}
}

func TestLogSafeURL_TokenInQuery(t *testing.T) {
	// Some clients put token in the query rather than the
	// fragment. Catch both shapes.
	in := "http://localhost:5173/chat?token=abc123&session=main"
	out := logSafeURL(in)
	if strings.Contains(out, "abc123") {
		t.Errorf("query-token leaked: %s", out)
	}
	if !strings.Contains(out, "token=[set]") {
		t.Errorf("expected token=[set] placeholder: %s", out)
	}
}

func TestLogSafeURL_EmptyTokenSurfacesAsSuch(t *testing.T) {
	// An explicitly empty token is a meaningful diagnostic
	// (auth=none) — keep it visible rather than rewriting.
	in := "http://localhost:5173/chat#token=&session=main"
	out := logSafeURL(in)
	if strings.Contains(out, "token=[set]") {
		t.Errorf("empty token should not become [set]: %s", out)
	}
}
