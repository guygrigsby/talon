// Package anthropic exposes the Anthropic Messages API as a Talon plugin.
// The subprocess entrypoint is `talon plugin run anthropic`.
//
// Auth resolution order:
//
//  1. ANTHROPIC_API_KEY env var (set by host translation from
//     plugins.entries.anthropic.config.apiKey, or by the user in
//     their shell).
//  2. Talon auth-profiles.json at
//     ~/.talon/agents/main/agent/auth-profiles.json, profile id
//     "anthropic:default".
//
// Empty key after both attempts surfaces as a clear init-time error
// so the gateway logs make the missing-config state obvious instead
// of failing the first chat.send.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	anthpkg "github.com/guygrigsby/talon/internal/provider/anthropic"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/secrets"
)

const modelsCacheTTL = 5 * time.Minute

// anthropicModels is the manifest's advertised model list. Kept in
// sync with the catalog's anthropic block so the gateway's resolver
// can route "anthropic/<id>" without extra config.
var anthropicModels = []string{
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-opus-4-5",
	"claude-sonnet-4-6",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
}

type anthropicPlugin struct {
	pb.UnimplementedPluginServer
	prov   *anthpkg.Provider
	apiKey string

	cacheMu sync.Mutex
	cached  []*pb.ModelDescriptor
	cacheAt time.Time
}

func (s *anthropicPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-anthropic",
			Version:     "0.1.0",
			Description: "Anthropic Messages API provider (Go plugin)",
			OffersProviders: []*pb.ProviderSpec{{
				Name:   "anthropic",
				Models: anthropicModels,
			}},
			Needs: []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG},
		},
	}, nil
}

func (s *anthropicPlugin) StreamCompletion(req *pb.StreamCompletionRequest, stream pb.Plugin_StreamCompletionServer) error {
	if s.prov == nil {
		return errors.New("anthropic plugin: provider not initialized")
	}
	preq, err := adaptRequest(req)
	if err != nil {
		return err
	}
	deltaCh, err := s.prov.Stream(stream.Context(), preq)
	if err != nil {
		return err
	}
	for d := range deltaCh {
		out := adaptDelta(d)
		if out == nil {
			continue
		}
		if err := stream.Send(out); err != nil {
			for range deltaCh {
			}
			return err
		}
	}
	return nil
}

func (s *anthropicPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

// ListProviderModels enumerates models from api.anthropic.com/v1/models.
// Cached with a 5min TTL; req.Refresh bypasses. Errors return an
// empty response so the host falls back to the manifest list. The
// provider name in the request is ignored — this plugin only serves
// "anthropic".
func (s *anthropicPlugin) ListProviderModels(ctx context.Context, req *pb.ListProviderModelsRequest) (*pb.ListProviderModelsResponse, error) {
	if !req.GetRefresh() {
		s.cacheMu.Lock()
		fresh := s.cached != nil && time.Now().Before(s.cacheAt.Add(modelsCacheTTL))
		out := s.cached
		s.cacheMu.Unlock()
		if fresh {
			return &pb.ListProviderModelsResponse{Models: out}, nil
		}
	}

	models, err := fetchAnthropicModels(ctx, s.apiKey)
	if err != nil {
		return &pb.ListProviderModelsResponse{}, nil
	}

	s.cacheMu.Lock()
	s.cached = models
	s.cacheAt = time.Now()
	s.cacheMu.Unlock()
	return &pb.ListProviderModelsResponse{Models: models}, nil
}

// fetchAnthropicModels calls GET /v1/models. Anthropic's response
// shape:
//
//	{"data":[{"id":"claude-opus-4-7","display_name":"Claude Opus 4.7","type":"model","created_at":"..."}],"has_more":false,"first_id":"...","last_id":"..."}
//
// We populate Id + Name. ContextWindow/Reasoning/Input aren't on the
// listing — those still come from user-config overlays in
// handleModelsList. All current Claude models have a 200K window
// which the host could backfill, but we keep the wire-level shape
// honest about what the upstream actually returns.
func fetchAnthropicModels(ctx context.Context, apiKey string) ([]*pb.ModelDescriptor, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, anthpkg.DefaultBaseURL+"/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", anthpkg.DefaultAPIVersion)
	httpReq.Header.Set("Accept", "application/json")

	cli := http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]*pb.ModelDescriptor, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, &pb.ModelDescriptor{
			Id:   m.ID,
			Name: m.DisplayName,
		})
	}
	return out, nil
}

// adaptRequest translates the wire-shape pb.StreamCompletionRequest
// into the in-tree provider.Request the anthropic provider expects.
// Model is prefixed because the host strips the provider segment
// before dispatch — the provider re-validates against its own key.
func adaptRequest(req *pb.StreamCompletionRequest) (provider.Request, error) {
	out := provider.Request{
		Model:  provider.ModelID("anthropic/" + req.GetModel()),
		System: req.GetSystem(),
	}
	for _, m := range req.GetMessages() {
		out.Messages = append(out.Messages, provider.Message{
			Role:       roleFromProto(m.GetRole()),
			Content:    m.GetContent(),
			ToolCalls:  toolCallsFromProto(m.GetToolCalls()),
			ToolCallID: m.GetToolCallId(),
		})
	}
	for _, t := range req.GetTools() {
		out.Tools = append(out.Tools, provider.ToolSpec{
			Name:             t.GetName(),
			Description:      t.GetDescription(),
			ParametersSchema: t.GetParametersSchema(),
		})
	}
	if req.Temperature != nil {
		v := req.GetTemperature()
		out.Options.Temperature = &v
	}
	if n := req.GetMaxOutputTokens(); n > 0 {
		out.Options.MaxOutputTokens = int(n)
	}
	return out, nil
}

func roleFromProto(r pb.Role) provider.Role {
	switch r {
	case pb.Role_ROLE_SYSTEM:
		return provider.RoleSystem
	case pb.Role_ROLE_USER:
		return provider.RoleUser
	case pb.Role_ROLE_ASSISTANT:
		return provider.RoleAssistant
	case pb.Role_ROLE_TOOL:
		return provider.RoleTool
	default:
		return provider.RoleUser
	}
}

func toolCallsFromProto(in []*pb.ToolCall) []provider.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]provider.ToolCall, 0, len(in))
	for _, c := range in {
		out = append(out, provider.ToolCall{
			ID:            c.GetId(),
			Name:          c.GetName(),
			ArgumentsJSON: c.GetArgumentsJson(),
		})
	}
	return out
}

func adaptDelta(d provider.Delta) *pb.Delta {
	switch d.Kind {
	case provider.DeltaText:
		return &pb.Delta{Kind: &pb.Delta_Text{Text: d.Text}}
	case provider.DeltaReasoning:
		return &pb.Delta{Kind: &pb.Delta_Reasoning{Reasoning: d.Text}}
	case provider.DeltaUsage:
		if d.Usage == nil {
			return nil
		}
		return &pb.Delta{Kind: &pb.Delta_Usage{Usage: &pb.Usage{
			InputTokens:  int32(d.Usage.InputTokens),
			OutputTokens: int32(d.Usage.OutputTokens),
		}}}
	case provider.DeltaToolCall:
		if d.ToolCall == nil {
			return nil
		}
		return &pb.Delta{Kind: &pb.Delta_ToolCall{ToolCall: &pb.ToolCall{
			Id:            d.ToolCall.ID,
			Name:          d.ToolCall.Name,
			ArgumentsJson: d.ToolCall.ArgumentsJSON,
		}}}
	case provider.DeltaError:
		msg := "stream error"
		if d.Err != nil {
			msg = d.Err.Error()
		}
		return &pb.Delta{Kind: &pb.Delta_Error{Error: msg}}
	}
	return nil
}

// loadAPIKey returns the Anthropic API key from, in order:
//
//  1. ANTHROPIC_API_KEY env (literal or already-resolved value)
//  2. ANTHROPIC_API_KEY_REF env (op:// / keychain:// reference,
//     resolved via the secrets resolver — matches the host-side
//     buildPluginEnv translation of plugins.entries.anthropic.
//     config.apiKey)
//  3. Talon auth-profiles.json profile "anthropic:default"
//
// Empty + nil err means none of the three sources had a key.
func loadAPIKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); v != "" {
		return v, nil
	}
	if ref := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY_REF")); ref != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		v, err := secrets.ResolveOrLiteral(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("resolve ANTHROPIC_API_KEY_REF: %w", err)
		}
		if v != "" {
			return v, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	authPath := filepath.Join(home, ".talon", "agents", "main", "agent", "auth-profiles.json")
	// LoadProfileKey errors when the file is missing; the env paths
	// already covered "no profile + env-only" so callers expect a
	// profile here. If the file truly isn't there we still want a
	// helpful message rather than an opaque wrapper.
	key, err := openai.LoadProfileKey(authPath, "anthropic:default", "anthropic")
	if err != nil {
		return "", fmt.Errorf("load %s: %w", authPath, err)
	}
	return key, nil
}

// New loads the Anthropic API key and returns a configured
// PluginServer.
func New() (pb.PluginServer, error) {
	apiKey, err := loadAPIKey()
	if err != nil {
		return nil, fmt.Errorf("anthropic plugin: %w", err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic plugin: no API key found in $ANTHROPIC_API_KEY or anthropic:default auth profile")
	}
	return &anthropicPlugin{
		prov:   anthpkg.New(anthpkg.Options{APIKey: apiKey}),
		apiKey: apiKey,
	}, nil
}
