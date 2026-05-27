// Package gateway is the CLI's transport to a running talon
// gateway. Talks HTTP/Connect against the gateway's RpcService.
// The old WebSocket-based client was retired with the server-side
// WS strip; streaming surfaces (chat events) moved to typed
// Connect clients used by the web FE. The CLI today only needs
// request/response RPCs.
//
// Public surface preserved so existing call sites compile:
//   NewClient(url, token) *Client
//   (*Client).Connect(ctx) error   — no-op (HTTP is connectionless)
//   (*Client).Close() error        — no-op (no persistent socket)
//   (*Client).Request(ctx, method, params) (json.RawMessage, error)
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client posts JSON to the gateway's RpcService.Dispatch endpoint.
// One Client is safe for concurrent use — http.Client is — but in
// practice the CLI builds one per command.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// NewClient constructs a Client. url is the gateway's HTTP origin
// (the same address the gateway prints at startup); the legacy
// "ws://" prefix from older configs is rewritten to "http://".
func NewClient(url, token string) *Client {
	url = wsURLToHTTP(strings.TrimRight(url, "/"))
	return &Client{
		httpClient: http.DefaultClient,
		baseURL:    url,
		token:      token,
	}
}

// Connect is a no-op kept for source-compat. HTTP is connectionless;
// the first Request opens a per-call connection (pooled by
// http.DefaultClient).
func (c *Client) Connect(_ context.Context) error { return nil }

// Close is a no-op for the same reason — http.Client owns its
// transport pool across the process lifetime.
func (c *Client) Close() error { return nil }

// Request invokes one registry method by name. Same signature as
// the legacy WS path's Request so call sites don't change.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method == "" {
		return nil, fmt.Errorf("gateway: method is required")
	}
	var paramsJSON string
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("gateway: marshal params: %w", err)
		}
		paramsJSON = string(b)
	}
	body, err := json.Marshal(map[string]any{
		"method":     method,
		"paramsJson": paramsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/talon.v1.RpcService/Dispatch", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gateway: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gateway: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseConnectError(method, resp.StatusCode, raw)
	}

	var out struct {
		ResultJSON string `json:"resultJson"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("gateway: %s: decode response: %w", method, err)
	}
	if out.ResultJSON == "" {
		return nil, nil
	}
	return json.RawMessage(out.ResultJSON), nil
}

// parseConnectError pulls a useful message out of a Connect error
// JSON body: `{"code":"<code>","message":"<reason>"}`. Falls back
// to the raw bytes when the body isn't Connect-shaped.
func parseConnectError(method string, status int, body []byte) error {
	var wire struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &wire); err == nil && wire.Message != "" {
		return fmt.Errorf("gateway: %s: [%s] %s", method, wire.Code, wire.Message)
	}
	return fmt.Errorf("gateway: %s: http %d: %s", method, status, strings.TrimSpace(string(body)))
}

// wsURLToHTTP rewrites legacy ws://host:port (still in some
// users' configs from the WS era) to http://host:port. wss
// becomes https. Anything else passes through unchanged.
func wsURLToHTTP(u string) string {
	switch {
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	}
	return u
}
