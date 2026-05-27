package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNewFromOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oauth/token" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "client_credentials" {
			t.Fatalf("grant_type = %q", got)
		}
		if got := r.Form.Get("client_id"); got != "id" {
			t.Fatalf("client_id = %q", got)
		}
		if got := r.Form.Get("client_secret"); got != "secret" {
			t.Fatalf("client_secret = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok-123","token_type":"Bearer"}`)
	}))
	defer srv.Close()

	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "tok-123" {
		t.Fatalf("token = %q", c.token)
	}
}

func TestNewFromOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":"invalid_client"}`)
	}))
	defer srv.Close()
	if _, err := newFromOAuthAt(context.Background(), srv.URL, "id", "bad"); err == nil {
		t.Fatal("want error on 401")
	}
}

func TestTailnetName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oauth/token":
			fmt.Fprintln(w, `{"access_token":"tok","token_type":"Bearer"}`)
		case "/api/v2/tailnet/-":
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Fatalf("authz = %q", got)
			}
			fmt.Fprintln(w, `{"name":"example.com","dnsName":"example.ts.net"}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	name, err := c.TailnetName(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "example.ts.net" {
		t.Fatalf("tailnet name = %q, want example.ts.net", name)
	}
}

func TestCreateService(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth/token":
			fmt.Fprintln(w, `{"access_token":"tok","token_type":"Bearer"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v2/tailnet/-/vip-services/"):
			if r.Method != http.MethodPut {
				t.Fatalf("method = %s", r.Method)
			}
			// Name segment must be the URL-escaped svc-prefixed name.
			wantSuffix := "/api/v2/tailnet/-/vip-services/" + url.PathEscape("svc:talon")
			if r.URL.EscapedPath() != wantSuffix {
				t.Fatalf("escaped path = %q, want %q", r.URL.EscapedPath(), wantSuffix)
			}
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateService(context.Background(), "svc:talon", []string{"443"}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, gotBody)
	}
	if body["name"] != "svc:talon" {
		t.Fatalf("body name = %v", body["name"])
	}
	ports, _ := body["ports"].([]any)
	if len(ports) != 1 || ports[0] != "443" {
		t.Fatalf("ports = %v, want [443]", body["ports"])
	}
	tags, _ := body["tags"].([]any)
	if len(tags) != 1 || tags[0] != "tag:talon" {
		t.Fatalf("tags = %v, want [tag:talon]", body["tags"])
	}
}

func TestCreateServiceAlreadyExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth/token":
			fmt.Fprintln(w, `{"access_token":"tok","token_type":"Bearer"}`)
		default:
			// Simulate "already exists" conflict.
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintln(w, `{"message":"service already exists"}`)
		}
	}))
	defer srv.Close()

	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateService(context.Background(), "svc:talon", []string{"443"}); err != nil {
		t.Fatalf("409 already-exists should be treated as success, got %v", err)
	}
}

func TestCreateServiceServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" {
			fmt.Fprintln(w, `{"access_token":"tok","token_type":"Bearer"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	c, err := newFromOAuthAt(context.Background(), srv.URL, "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CreateService(context.Background(), "svc:talon", []string{"443"}); err == nil {
		t.Fatal("want error on 500")
	}
}
