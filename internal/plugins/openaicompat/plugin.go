// Package openaicompat is a multi-tenant plugin that exposes any
// OpenAI-compatible HTTP endpoint as a talon provider. One process
// serves N "providers" (openai, deepseek, mistral, mlx, lmstudio,
// ollama, vllm, ...), each configured under
//
//	plugins.entries.openai-compat.config.providers.<name>: {
//	    apiKey:   "..."  (optional for local loopback servers)
//	    baseUrl:  "https://api.example.com/v1"
//	    compat?: { reasoningContent?: true, ... }
//	}
//
// The plugin's Initialize() builds one openai.Provider instance per
// configured entry and advertises them all in the manifest. The host
// dispatches StreamCompletion by the model id's provider segment;
// this plugin routes to the matching Provider instance.
//
// Defaults: when a known-name entry is missing from config, the
// plugin still registers it with the canonical base URL (api.openai.
// com, api.deepseek.com, localhost:8080 for mlx, etc.). Local
// providers can be registered without an API key when the baseUrl
// is loopback; if a key IS configured (LM Studio supports one), it
// flows through to upstream requests unchanged. The RequiresAPIKey
// flag only gates whether registration is skipped on missing key,
// not whether the key is forwarded.
package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
	"github.com/guygrigsby/talon/internal/provider"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/tidwall/gjson"
)

// modelsCacheTTL caps how long a successful /v1/models response is
// reused before we re-query. 5min is the sweet spot: short enough
// that a newly-pulled model on a local server (LM Studio / Ollama
// hot-swap) surfaces quickly; long enough that the picker doesn't
// hammer cloud providers when a user clicks around.
const modelsCacheTTL = 5 * time.Minute

// providerDefaults seeds canonical base URLs for well-known
// OpenAI-compatible endpoints. User config overrides any field; an
// entry that isn't in this map still works as long as the user
// supplies a baseUrl explicitly.
var providerDefaults = map[string]providerSeed{
	"openai":   {BaseURL: "https://api.openai.com/v1", RequiresAPIKey: true},
	"deepseek": {BaseURL: "https://api.deepseek.com/v1", RequiresAPIKey: true, Compat: compatFlags{ReasoningContent: true}},
	"mistral":  {BaseURL: "https://api.mistral.ai/v1", RequiresAPIKey: true},
	"mlx":      {BaseURL: "http://localhost:8080/v1", RequiresAPIKey: false},
	"lmstudio": {BaseURL: "http://localhost:1234/v1", RequiresAPIKey: false},
	"ollama":   {BaseURL: "http://localhost:11434/v1", RequiresAPIKey: false},
}

// providerSeed is the default shape for a known provider. Mirrors
// the fields the user can set under config.providers.<name>.
type providerSeed struct {
	BaseURL        string
	RequiresAPIKey bool
	Compat         compatFlags
}

// compatFlags carries per-provider quirks for OpenAI-compat
// deviations. New fields land here as new endpoints expose new
// behaviors; the openai.Provider Options struct already covers most.
type compatFlags struct {
	// ReasoningContent: provider streams the model's reasoning trace
	// in a `delta.reasoning_content` field (deepseek-reasoner, o1).
	// The openai.Provider already handles this — flag stays for
	// future per-provider divergence.
	ReasoningContent bool
}

// providerEntry is one resolved provider after merging defaults +
// user config. Holds the openai.Provider that StreamCompletion will
// route to.
type providerEntry struct {
	Name    string
	BaseURL string
	APIKey  string
	Prov    *openai.Provider
}

type plug struct {
	pb.UnimplementedPluginServer
	providers map[string]*providerEntry // keyed by provider name

	cacheMu sync.Mutex
	cache   map[string]modelCacheEntry // keyed by provider name
}

type modelCacheEntry struct {
	models    []*pb.ModelDescriptor
	expiresAt time.Time
}

// New constructs the plugin. Reads merged config, materializes one
// openai.Provider per configured (or defaulted) entry, and returns
// the populated server.
//
// "Configured" here means either: (a) the user has an entry under
// plugins.entries.openai-compat.config.providers.<name>, or (b) the
// name is in providerDefaults AND auth resolves (api key found for
// cloud providers; loopback baseUrl for local ones).
//
// Entries that need auth but don't have it are silently skipped —
// the auth-status RPC surfaces the missing key on the FE; the
// plugin's job is just to register what's reachable today.
func New() (pb.PluginServer, error) {
	paths := talonpath.DefaultPaths()
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return nil, fmt.Errorf("openai-compat: read merged config: %w", err)
	}

	authPath := filepath.Join(paths.Talon.AgentDir("main"), "agent", "auth-profiles.json")

	providers := map[string]*providerEntry{}

	// Walk providerDefaults first so well-known providers register
	// without explicit user config (assuming auth resolves).
	for name, seed := range providerDefaults {
		e := resolveEntry(name, seed, merged, authPath)
		if e != nil {
			providers[name] = e
		}
	}

	// Then walk user-config entries — these win on collision and
	// can introduce providers not in defaults (e.g. fireworks,
	// together, openrouter).
	gjson.GetBytes(merged, "plugins.entries.openai-compat.config.providers").ForEach(func(k, _ gjson.Result) bool {
		name := k.Str
		if name == "" {
			return true
		}
		seed := providerDefaults[name] // zero value when unknown
		e := resolveEntry(name, seed, merged, authPath)
		if e != nil {
			providers[name] = e
		}
		return true
	})

	if len(providers) == 0 {
		return nil, errors.New("openai-compat: no providers resolved — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure plugins.entries.openai-compat.config.providers.<name>")
	}
	return &plug{providers: providers, cache: map[string]modelCacheEntry{}}, nil
}

// ListProviderModels enumerates models from one configured tenant by
// calling its /v1/models endpoint. Cached per-provider with a 5min
// TTL; req.Refresh bypasses the cache. Errors fall back to manifest
// behavior (empty response → host uses ProviderSpec.Models if any).
func (s *plug) ListProviderModels(ctx context.Context, req *pb.ListProviderModelsRequest) (*pb.ListProviderModelsResponse, error) {
	name := req.GetProvider()
	entry, ok := s.providers[name]
	if !ok {
		return nil, fmt.Errorf("openai-compat: no provider tenant %q (configured: %v)", name, s.providerNames())
	}

	if !req.GetRefresh() {
		s.cacheMu.Lock()
		ce, hit := s.cache[name]
		s.cacheMu.Unlock()
		if hit && time.Now().Before(ce.expiresAt) {
			return &pb.ListProviderModelsResponse{Models: ce.models}, nil
		}
	}

	models, err := fetchOpenAIModels(ctx, entry.BaseURL, entry.APIKey)
	if err != nil {
		// Soft failure: the host falls back to the manifest's
		// static list (which is empty for us — but we don't want
		// a transient /models 500 to error the whole models.list
		// chain).
		return &pb.ListProviderModelsResponse{}, nil
	}

	s.cacheMu.Lock()
	s.cache[name] = modelCacheEntry{models: models, expiresAt: time.Now().Add(modelsCacheTTL)}
	s.cacheMu.Unlock()
	return &pb.ListProviderModelsResponse{Models: models}, nil
}

// fetchOpenAIModels calls GET <baseURL>/models with bearer auth (or
// no auth on loopback when key is empty) and returns the result as
// []*pb.ModelDescriptor. OpenAI's /models shape:
//
//	{"object":"list","data":[{"id":"...","object":"model",...}, ...]}
//
// We only populate ModelDescriptor.ID — /models doesn't carry
// contextWindow, reasoning, or input modalities. Those still come
// from user config overlays at the host's models.list layer.
func fetchOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]*pb.ModelDescriptor, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("Accept", "application/json")

	cli := http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
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
		out = append(out, &pb.ModelDescriptor{Id: m.ID})
	}
	return out, nil
}

// resolveEntry merges defaults + user config + auth resolution into
// a usable providerEntry. Returns nil when the resolved entry can't
// be made to work (e.g. cloud provider with no API key found).
func resolveEntry(name string, seed providerSeed, merged []byte, authPath string) *providerEntry {
	cfgPath := "plugins.entries.openai-compat.config.providers." + name

	baseURL := seed.BaseURL
	if v := gjson.GetBytes(merged, cfgPath+".baseUrl").Str; v != "" {
		baseURL = v
	}
	if baseURL == "" {
		return nil
	}

	apiKey := gjson.GetBytes(merged, cfgPath+".apiKey").Str
	// Env fallback — accepts an op:// / keychain:// reference too via
	// <NAME>_API_KEY_REF, matching the anthropic plugin's convention.
	if apiKey == "" {
		envName := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
		if v := os.Getenv(envName); v != "" {
			apiKey = v
		} else if ref := os.Getenv(envName + "_REF"); ref != "" {
			apiKey = ref
		}
	}
	// Profile fallback — auth-profiles.json's `key` field can also
	// carry an op:// / keychain:// ref; resolver below handles it.
	if apiKey == "" {
		if k, err := openai.LoadProfileKeyOptional(authPath, name+":default", name); err == nil && k != "" {
			apiKey = k
		}
	}

	// Resolve op:// / keychain:// references. Plain string values
	// pass through unchanged. Failures here surface as "no key" and
	// the entry is skipped — preferable to silently using the
	// literal ref string as a bogus credential.
	if apiKey != "" && secrets.IsReference(apiKey) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		resolved, err := secrets.ResolveOrLiteral(ctx, apiKey)
		cancel()
		if err != nil || resolved == "" {
			return nil
		}
		apiKey = resolved
	}

	needsKey := seed.RequiresAPIKey && !isLoopback(baseURL)
	if needsKey && apiKey == "" {
		return nil
	}

	prov := openai.New(openai.Options{
		APIKey:      apiKey,
		BaseURL:     baseURL,
		Name:        name,
		ProviderKey: name,
	})
	return &providerEntry{
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Prov:    prov,
	}
}

// isLoopback reports whether u points at the local machine. Used
// to skip the "API key required" check for local servers (LM Studio,
// MLX, Ollama, etc.) which run unauthenticated by default.
func isLoopback(u string) bool {
	low := strings.ToLower(u)
	return strings.Contains(low, "://localhost") ||
		strings.Contains(low, "://127.0.0.1") ||
		strings.Contains(low, "://[::1]") ||
		strings.Contains(low, "://0.0.0.0")
}

// Initialize advertises every resolved provider as a ProviderSpec.
// Models is left empty here — Phase 3's ListProviderModels RPC
// fills it dynamically. The bridge in handleModelsList still
// surfaces the provider name even with empty Models so the FE auth-
// status badge has something to point at.
func (s *plug) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	specs := make([]*pb.ProviderSpec, 0, len(s.providers))
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		specs = append(specs, &pb.ProviderSpec{Name: n})
	}
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:            "talon-openai-compat",
			Version:         "0.1.0",
			Description:     "OpenAI-compatible providers (multi-tenant): openai, deepseek, mistral, lmstudio, mlx, ollama, …",
			OffersProviders: specs,
			Needs:           []pb.Capability{pb.Capability_CAPABILITY_READ_CONFIG},
		},
	}, nil
}

// StreamCompletion routes by the model id's provider segment to
// the matching openai.Provider instance. The host already split
// on the segment to pick this plugin; here we use it again to pick
// the right tenant.
func (s *plug) StreamCompletion(req *pb.StreamCompletionRequest, stream pb.Plugin_StreamCompletionServer) error {
	modelID := req.GetModel()
	// Tenant resolution, in order:
	//  1. req.Provider — host-supplied, the canonical signal for
	//     multi-tenant routing (added in plugin.proto v2).
	//  2. <provider>/<model> embedded prefix — legacy/manual path.
	//  3. Single-tenant fallback — when only one tenant is configured
	//     in the plugin, the model id is unambiguous.
	providerName := req.GetProvider()
	model := modelID
	if providerName == "" {
		providerName, model = splitModelID(modelID)
	}
	if providerName == "" {
		if len(s.providers) == 1 {
			for n := range s.providers {
				providerName = n
			}
		} else {
			return fmt.Errorf("openai-compat: cannot route model %q — host did not supply provider and %d tenants configured", modelID, len(s.providers))
		}
	}
	entry, ok := s.providers[providerName]
	if !ok {
		return fmt.Errorf("openai-compat: no provider tenant %q (configured: %v)", providerName, s.providerNames())
	}

	preq, err := adaptRequest(model, providerName, req)
	if err != nil {
		return err
	}
	deltaCh, err := entry.Prov.Stream(stream.Context(), preq)
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

func (s *plug) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

func (s *plug) providerNames() []string {
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// splitModelID splits "<provider>/<model>" into the two halves.
// Returns ("", whole) when there's no slash — caller resolves that
// case with single-tenant fallback.
func splitModelID(in string) (string, string) {
	for i := 0; i < len(in); i++ {
		if in[i] == '/' {
			return in[:i], in[i+1:]
		}
	}
	return "", in
}

func adaptRequest(model, providerName string, req *pb.StreamCompletionRequest) (provider.Request, error) {
	out := provider.Request{
		Model:  provider.ModelID(providerName + "/" + model),
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
