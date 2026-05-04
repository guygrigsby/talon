// talon-brave-plugin is a Go subprocess plugin offering a
// `web_search` tool backed by the Brave Search API. Native
// counterpart to the openclaw-bundled extensions/brave (Node);
// same intent (let the model search the web) without the Node
// runtime dep.
//
// Wire shape:
//
//   - Refuses to start without TALON_PLUGIN_HANDSHAKE +
//     TALON_PLUGIN_AUTH_COOKIE.
//   - Listens on 127.0.0.1:0, prints "1|TCP|<addr>|grpc" on stdout.
//   - Initialize: offers_tools=[{name: "web_search", schema: {query,
//     count?, country?, freshness?, mode?}}].
//   - RunTool("web_search", args): GET https://api.search.brave.com/
//     res/v1/web/search (or .../res/v1/llm/context for mode=
//     "llm-context") with X-Subscription-Token header, returns the
//     formatted result body. API key resolution: configured value at
//     plugins.entries.brave.config.webSearch.apiKey (resolved through
//     talon's secrets resolver — op:// / keychain:// references work)
//     OR fallback to BRAVE_API_KEY env var.
//
// Build:
//
//	go build -o bin/talon-brave-plugin ./apps/talon-brave-plugin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/grpc"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

const (
	handshakeMagic = "talon.plugin.v1"

	braveWebEndpoint        = "https://api.search.brave.com/res/v1/web/search"
	braveLlmContextEndpoint = "https://api.search.brave.com/res/v1/llm/context"

	// Cap for any single web_search call — we don't want a runaway
	// agent burning Brave-search quota in one shot.
	defaultCount = 10
	maxCount     = 20
	httpTimeout  = 20 * time.Second
)

type bravePlugin struct {
	pb.UnimplementedPluginServer
	http *http.Client
}

func (s *bravePlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-brave",
			Version:     "0.1.0",
			Description: "Brave Search web search (Go plugin)",
			OffersTools: []*pb.ToolSpec{{
				Name: "web_search",
				Description: "Search the web via Brave Search. Returns structured results " +
					"(title, url, description). Use for fresh / general-knowledge questions " +
					"the model can't answer from training data.",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"query":     {"type": "string", "description": "Search query."},
						"count":     {"type": "integer", "description": "Max results (default 10, max 20)."},
						"country":   {"type": "string", "description": "Two-letter country code (US, GB, ...)."},
						"freshness": {"type": "string", "description": "Time filter: pd (past day), pw (week), pm (month), py (year)."},
						"mode":      {"type": "string", "enum": ["web", "llm-context"], "description": "Result style. llm-context returns plain text, easier for an LLM to read; web returns the structured catalog."}
					},
					"required": ["query"],
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

func (s *bravePlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	if req.GetToolName() != "web_search" {
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("brave plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
	var args struct {
		Query     string `json:"query"`
		Count     int    `json:"count"`
		Country   string `json:"country"`
		Freshness string `json:"freshness"`
		Mode      string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return &pb.RunToolResponse{Output: "web_search: invalid arguments JSON: " + err.Error(), IsError: true}, nil
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return &pb.RunToolResponse{Output: "web_search: query is required", IsError: true}, nil
	}
	if args.Count <= 0 {
		args.Count = defaultCount
	}
	if args.Count > maxCount {
		args.Count = maxCount
	}
	if args.Mode == "" {
		args.Mode = "web"
	}
	apiKey := resolveBraveAPIKey(ctx)
	if apiKey == "" {
		return &pb.RunToolResponse{
			Output: "web_search: Brave Search API key missing. Set BRAVE_API_KEY env var, " +
				"or configure plugins.entries.brave.config.webSearch.apiKey (op:// references work).",
			IsError: true,
		}, nil
	}

	switch args.Mode {
	case "llm-context":
		return s.callLlmContext(ctx, apiKey, args.Query, args.Country, args.Freshness)
	default:
		return s.callWebSearch(ctx, apiKey, args.Query, args.Count, args.Country, args.Freshness)
	}
}

// callWebSearch hits the structured /res/v1/web/search endpoint and
// formats results as a numbered list — easier for the model to parse
// + cite back to the user.
func (s *bravePlugin) callWebSearch(ctx context.Context, apiKey, query string, count int, country, freshness string) (*pb.RunToolResponse, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	if country != "" {
		q.Set("country", country)
	}
	if freshness != "" {
		q.Set("freshness", freshness)
	}
	endpoint := braveWebEndpoint + "?" + q.Encode()
	body, err := s.do(ctx, endpoint, apiKey)
	if err != nil {
		return &pb.RunToolResponse{Output: "web_search: " + err.Error(), IsError: true}, nil
	}
	out, err := formatWebSearchResults(body)
	if err != nil {
		// Fall back to raw body — better the model sees something.
		return &pb.RunToolResponse{Output: string(body)}, nil
	}
	return &pb.RunToolResponse{Output: out}, nil
}

// callLlmContext hits the /res/v1/llm/context endpoint which returns
// already-formatted text Brave intends for direct LLM consumption.
// We pass it through verbatim.
func (s *bravePlugin) callLlmContext(ctx context.Context, apiKey, query, country, freshness string) (*pb.RunToolResponse, error) {
	q := url.Values{}
	q.Set("q", query)
	if country != "" {
		q.Set("country", country)
	}
	if freshness != "" {
		q.Set("freshness", freshness)
	}
	endpoint := braveLlmContextEndpoint + "?" + q.Encode()
	body, err := s.do(ctx, endpoint, apiKey)
	if err != nil {
		return &pb.RunToolResponse{Output: "web_search (llm-context): " + err.Error(), IsError: true}, nil
	}
	return &pb.RunToolResponse{Output: string(body)}, nil
}

// do issues a GET with the Brave subscription header and returns the
// response body (or error). Wraps the request timeout + non-200
// handling in one place.
func (s *bravePlugin) do(ctx context.Context, endpoint, apiKey string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("brave http %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, nil
}

// formatWebSearchResults turns the Brave JSON envelope into a
// compact numbered text block. Falls back to error if the shape
// isn't what we expect — caller sends the raw body to the model.
func formatWebSearchResults(body []byte) (string, error) {
	var env struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", err
	}
	if len(env.Web.Results) == 0 {
		return "(no results)", nil
	}
	var sb strings.Builder
	for i, r := range env.Web.Results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, stripHTML(r.Description))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// stripHTML removes the <strong> / <em> tags Brave wraps matched
// terms in. Cheap version — we don't need full HTML parsing for
// snippet text.
func stripHTML(s string) string {
	for _, t := range []string{"<strong>", "</strong>", "<em>", "</em>"} {
		s = strings.ReplaceAll(s, t, "")
	}
	return s
}

// resolveBraveAPIKey returns the Brave API key by trying:
//  1. BRAVE_API_KEY env var (highest precedence — quick override)
//  2. talon-op-plugin / talon-keychain-plugin if BRAVE_API_KEY_REF
//     is an op:// or keychain:// reference
//
// The host is responsible for setting one of these env vars when
// it spawns the plugin (see cmd/talon/gateway.go's plugin spawn
// path — the `plugins.entries.brave.config.webSearch.apiKey`
// merged-config value gets routed in via env).
func resolveBraveAPIKey(ctx context.Context) string {
	if v := strings.TrimSpace(os.Getenv("BRAVE_API_KEY")); v != "" {
		return v
	}
	ref := strings.TrimSpace(os.Getenv("BRAVE_API_KEY_REF"))
	if ref == "" {
		return ""
	}
	// Resolve via the same plugin pattern internal/secrets uses.
	// Direct subprocess shell-out keeps this plugin from importing
	// the gateway internals.
	scheme := ""
	switch {
	case strings.HasPrefix(ref, "op://"):
		scheme = "op"
	case strings.HasPrefix(ref, "keychain://"):
		scheme = "keychain"
	default:
		return ref // literal value passed via _REF env (unusual but supported)
	}
	binary := "talon-" + scheme + "-plugin"
	path, err := exec.LookPath(binary)
	if err != nil {
		fallback := "/usr/local/bin/" + binary
		if _, statErr := os.Stat(fallback); statErr == nil {
			path = fallback
		} else {
			return ""
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	log := slog.With("plugin", "brave")

	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != handshakeMagic {
		log.Error("handshake env mismatch — refusing to start outside the host",
			"got", got, "want", handshakeMagic)
		os.Exit(1)
	}
	if os.Getenv("TALON_PLUGIN_AUTH_COOKIE") == "" {
		log.Error("missing TALON_PLUGIN_AUTH_COOKIE")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	plug := &bravePlugin{http: &http.Client{Timeout: httpTimeout + 5*time.Second}}
	pb.RegisterPluginServer(server, plug)

	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := server.Serve(listener); err != nil {
		log.Error("grpc serve failed", "err", err)
		os.Exit(1)
	}
}
