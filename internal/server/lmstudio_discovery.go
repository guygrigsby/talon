package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/netutil"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/tidwall/gjson"
)

// lmstudioDiscoveryTimeout caps how long models.list will wait for
// LM Studio to respond before giving up and returning the static
// catalog alone. LM Studio is local, so the only realistic way this
// times out is the server isn't running — at which point we don't
// want to delay the UI's model picker.
const lmstudioDiscoveryTimeout = 800 * time.Millisecond

// lmstudioModelEntry is the OpenAI-compatible /v1/models row shape.
// LM Studio returns extra fields (architecture, max_context_length,
// state); we pull what we use and ignore the rest.
type lmstudioModelEntry struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	OwnedBy  string `json:"owned_by"`
	MaxCtx   int64  `json:"max_context_length"`
	State    string `json:"state"` // "loaded" | "not-loaded" per LM Studio
}

type lmstudioModelList struct {
	Object string               `json:"object"`
	Data   []lmstudioModelEntry `json:"data"`
}

// discoverLMStudioModels asks LM Studio's OpenAI-compatible
// /models endpoint what's actually loaded. Returns rows in the
// same shape handleModelsList builds for static-catalog entries.
// Errors (network, timeout, non-200, parse) collapse to (nil, err)
// so the caller can degrade gracefully.
//
// Honors the same loopback→host.docker.internal rewrite as the
// chat path so a containerized gateway hitting "localhost:1234"
// resolves to the user's host machine.
//
// authKey, when non-empty, is sent as the Authorization Bearer token.
// LM Studio installs that require auth (e.g. a proxy in front, or
// LM Studio's own auth toggle) reject unauthenticated requests with
// a misleading "Unexpected endpoint" warning — same key the chat
// path uses keeps both paths consistent.
func discoverLMStudioModels(ctx context.Context, merged []byte, httpClient *http.Client, authKey string) ([]map[string]any, error) {
	baseURL := lookupLMStudioBaseURLForDiscovery(merged)
	if baseURL == "" {
		return nil, errors.New("lmstudio: no base URL")
	}
	url := strings.TrimRight(baseURL, "/") + "/models"

	ctx, cancel := context.WithTimeout(ctx, lmstudioDiscoveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("lmstudio discover: build request: %w", err)
	}
	if authKey != "" {
		req.Header.Set("Authorization", "Bearer "+authKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lmstudio discover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		// Read a short prefix for the error message so the operator
		// can debug 401 / 502 / etc. without flooding logs.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("lmstudio discover: http %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}

	var body lmstudioModelList
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("lmstudio discover: parse: %w", err)
	}

	out := make([]map[string]any, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID == "" {
			continue
		}
		// Only surface loaded entries when the server reports state.
		// LM Studio versions that don't include `state` get the
		// benefit of the doubt and show through.
		if m.State != "" && m.State != "loaded" {
			continue
		}
		row := map[string]any{
			"id":            m.ID,
			"provider":      "lmstudio",
			"name":          m.ID + " (loaded)",
			"contextWindow": m.MaxCtx,
			"reasoning":     false,
		}
		out = append(out, row)
	}
	return out, nil
}

// lookupLMStudioBaseURLForDiscovery returns the same base URL the
// chat factory's lookupLMStudioBaseURL would use, but reads only the
// merged config (no openclaw.Paths needed at the call site here).
// Defaults match: http://localhost:1234/v1, with the
// loopback-in-container rewrite applied.
func lookupLMStudioBaseURLForDiscovery(merged []byte) string {
	const defaultURL = "http://localhost:1234/v1"
	raw := defaultURL
	if v := gjson.GetBytes(merged, "models.providers.lmstudio.baseUrl"); v.Exists() && v.Str != "" {
		raw = v.Str
	}
	return netutil.RewriteLoopbackForContainer(raw)
}

// httpClientForDiscovery is the http client models.list uses for
// dynamic discovery. Pulled out so tests can substitute. Default
// has no timeout because lmstudioDiscoveryTimeout is enforced via
// ctx instead — keeps cancel semantics clean.
var httpClientForDiscovery = &http.Client{Transport: http.DefaultTransport}

// resolveLMStudioAuthKey reads "lmstudio:default" from the main
// agent's auth-profiles.json — the same on-disk file the chat
// factory uses for openai/deepseek/lmstudio per-agent keys. Returns
// "" when the profile isn't set; callers send the request without
// a Bearer header in that case.
//
// LM Studio is a gateway-shared local resource (one server, many
// agents could chat with it), so picking "main" is a reasonable
// default. Future option: a gateway-level
// `models.providers.lmstudio.apiKey` config so the key isn't
// pinned to one agent.
func resolveLMStudioAuthKey(paths openclaw.Paths) string {
	authPath := filepath.Join(paths.Openclaw.AgentDir("main"), "agent", "auth-profiles.json")
	key, err := openai.LoadProfileKeyOptional(authPath, "lmstudio:default", "lmstudio")
	if err != nil {
		// Malformed profile — log nothing here; the chat factory will
		// surface the same error on the next chat.send.
		return ""
	}
	return key
}

// callDiscoverLMStudio is the seam handleModelsList uses, so tests
// can stub the discovery without spinning up a real LM Studio.
var callDiscoverLMStudio = func(ctx context.Context, paths openclaw.Paths, merged []byte) ([]map[string]any, error) {
	return discoverLMStudioModels(ctx, merged, httpClientForDiscovery, resolveLMStudioAuthKey(paths))
}

// Compile-time use of imported config to avoid unused-import errors
// while we keep the package surface ready for future per-paths
// discovery (e.g. agent-scoped auth keys for non-LM-Studio backends).
var _ = config.MergedBytes
