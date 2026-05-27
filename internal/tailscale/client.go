// Package tailscale is a minimal, provision-time client for the Tailscale
// REST API v2. It exchanges an OAuth client (id+secret) for a short-lived
// access token, reads the tailnet's MagicDNS name, and creates/updates a
// VIPService.
//
// Scope boundary (ADR 0008): this package is used only by the
// `talon configure tailscale` wizard to provision tailnet objects. Runtime
// node bring-up lives in internal/tailnet. We hand-roll the HTTP calls
// rather than import an upstream client because the upstream VIPService
// methods are not available as an importable package at the pinned version.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAPIBase is the production Tailscale API root. Tests point
// newFromOAuthAt at an httptest server instead.
const defaultAPIBase = "https://api.tailscale.com"

// Client talks to the Tailscale REST API v2 with a bearer access token.
type Client struct {
	httpc   *http.Client
	apiBase string // e.g. https://api.tailscale.com
	token   string // bearer access token
}

// NewFromOAuth exchanges an OAuth client id+secret for an access token and
// returns a ready-to-use Client against the production API.
func NewFromOAuth(ctx context.Context, id, secret string) (*Client, error) {
	return newFromOAuthAt(ctx, defaultAPIBase, id, secret)
}

// newFromOAuthAt is NewFromOAuth with an injectable base URL so tests can
// point at an httptest server.
func newFromOAuthAt(ctx context.Context, base, id, secret string) (*Client, error) {
	base = strings.TrimRight(base, "/")
	httpc := &http.Client{Timeout: 30 * time.Second}

	form := url.Values{}
	form.Set("client_id", id)
	form.Set("client_secret", secret)
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tailscale: oauth token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("tailscale: oauth token exchange: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("tailscale: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("tailscale: oauth token exchange returned no access_token")
	}
	return &Client{httpc: httpc, apiBase: base, token: tok.AccessToken}, nil
}

// TailnetName returns the tailnet's MagicDNS base, e.g. "example.ts.net".
func (c *Client) TailnetName(ctx context.Context) (string, error) {
	var parsed struct {
		Name    string `json:"name"`
		DNSName string `json:"dnsName"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/tailnet/-", nil, &parsed); err != nil {
		return "", err
	}
	// Prefer the explicit DNS name (the ".ts.net" host); fall back to the
	// tailnet's name field when the API doesn't surface dnsName.
	if parsed.DNSName != "" {
		return parsed.DNSName, nil
	}
	if parsed.Name != "" {
		return parsed.Name, nil
	}
	return "", fmt.Errorf("tailscale: tailnet response had no name")
}

// vipServiceBody is the JSON body for create/update of a VIPService.
type vipServiceBody struct {
	Name    string   `json:"name"`
	Ports   []string `json:"ports"`
	Tags    []string `json:"tags"`
	Comment string   `json:"comment,omitempty"`
}

// CreateService creates (or updates) a VIPService for the svc-prefixed name
// (e.g. "svc:talon") advertising the given ports (e.g. []string{"443"}).
// Idempotent: an already-exists conflict is treated as success.
func (c *Client) CreateService(ctx context.Context, svcName string, ports []string) error {
	body := vipServiceBody{
		Name:    svcName,
		Ports:   ports,
		Tags:    []string{"tag:talon"},
		Comment: "talon gateway",
	}
	path := "/api/v2/tailnet/-/vip-services/" + url.PathEscape(svcName)
	err := c.do(ctx, http.MethodPut, path, body, nil)
	if err == nil {
		return nil
	}
	// Treat "already exists" as success — provisioning is idempotent.
	var apiErr *apiError
	if as(err, &apiErr) && (apiErr.status == http.StatusConflict || strings.Contains(strings.ToLower(apiErr.msg), "already exists")) {
		return nil
	}
	return err
}

// apiError carries the HTTP status + body for non-2xx API responses so
// callers (CreateService) can special-case conflicts.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("tailscale: HTTP %d: %s", e.status, e.msg)
}

// as is a tiny errors.As shim avoiding an import for one use.
func as(err error, target **apiError) bool {
	return errors.As(err, target)
}

// do issues a bearer-authed JSON request. When body is non-nil it is
// JSON-encoded; when out is non-nil a 2xx response body is decoded into it.
// Non-2xx responses return an *apiError.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tailscale: encode request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("tailscale: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return &apiError{status: resp.StatusCode, msg: truncate(string(raw), 300)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("tailscale: decode %s response: %w", path, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
