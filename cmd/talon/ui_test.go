package main

import (
	"net/url"
	"strings"
	"testing"
)

// Default uiHost ("") means "use the gateway's own URL". The
// embedded SvelteKit SPA is served same-origin from the gateway,
// so the URL needs no separate UI host nor a gatewayUrl fragment.
func TestBuildUIURL_EmptyUIHostUsesGateway(t *testing.T) {
	got := buildUIURL("", "localhost", 18789, "", "", "/")
	if got != "http://localhost:18789/" {
		t.Errorf("got %q, want http://localhost:18789/", got)
	}
}

func TestBuildUIURL_EmptyUIHostCustomPort(t *testing.T) {
	got := buildUIURL("", "127.0.0.1", 28800, "", "main", "/")
	if got != "http://127.0.0.1:28800/?session=main" {
		t.Errorf("got %q", got)
	}
}

// Explicit uiHost (Vite dev server, custom proxy, etc.) is passed
// through as-is. No more gatewayUrl fragment — the FE always uses
// location.origin for Connect calls; in dev Vite's proxy stitches
// /talon.v1.* back to the gateway.
func TestBuildUIURL_ExplicitUIHostPassthrough(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18789, "", "", "/")
	if got != "http://localhost:5173/" {
		t.Errorf("got %q", got)
	}
}

// Token always lands in the URL fragment so it stays out of HTTP
// request logs + referrers. The FE reads it client-side and
// forwards as Authorization: Bearer ... on Connect calls.
func TestBuildUIURL_TokenLandsInFragment(t *testing.T) {
	got := buildUIURL("", "localhost", 18789, "secret-tok", "main", "/")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("invalid URL: %v\n%s", err, got)
	}
	if parsed.Query().Get("session") != "main" {
		t.Errorf("session query missing: %s", got)
	}
	frag, _ := url.ParseQuery(parsed.Fragment)
	if frag.Get("token") != "secret-tok" {
		t.Errorf("token fragment = %q", frag.Get("token"))
	}
	// Token must NOT leak into the query string.
	if strings.Contains(parsed.RawQuery, "token") {
		t.Errorf("token leaked into query: %s", parsed.RawQuery)
	}
}

func TestBuildUIURL_NoSessionMeansNoQuery(t *testing.T) {
	got := buildUIURL("", "localhost", 18790, "", "", "/")
	parsed, _ := url.Parse(got)
	if parsed.RawQuery != "" {
		t.Errorf("expected empty query when no session: %q", parsed.RawQuery)
	}
}

func TestBuildUIURL_CustomPath(t *testing.T) {
	got := buildUIURL("", "localhost", 18789, "", "", "/agents")
	parsed, _ := url.Parse(got)
	if parsed.Path != "/agents" {
		t.Errorf("path = %q, want /agents", parsed.Path)
	}
}

// Token + session + custom path + custom UI host: the canonical
// `talon dashboard --ui-host http://localhost:5173 --token X` shape.
func TestBuildUIURL_FullShape(t *testing.T) {
	got := buildUIURL("http://localhost:5173", "localhost", 18789, "tok", "agent:main:main", "/")
	parsed, _ := url.Parse(got)
	if parsed.Host != "localhost:5173" {
		t.Errorf("host = %q", parsed.Host)
	}
	if parsed.Query().Get("session") != "agent:main:main" {
		t.Errorf("session = %q", parsed.Query().Get("session"))
	}
	frag, _ := url.ParseQuery(parsed.Fragment)
	if frag.Get("token") != "tok" {
		t.Errorf("token = %q", frag.Get("token"))
	}
}
