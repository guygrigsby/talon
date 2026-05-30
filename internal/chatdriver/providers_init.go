package chatdriver

import (
	"context"
	"strings"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/providers"
)

// init registers extra OpenAI-compatible providers with litellm so
// `llm.NewModel("mistral", ...)`, `llm.NewModel("mlx", ...)`, etc.
// work without the caller having to know about litellm's builtin
// provider list (which today covers openai, anthropic, deepseek,
// gemini, glm, grok, mimo, ollama, openrouter, qwen, bedrock —
// missing mistral, mlx, lmstudio).
//
// Each registration is idempotent: if a builtin already covers the
// name (e.g. someone added mistral upstream after we wrote this),
// litellm.RegisterProvider rejects with an "already registered"
// error which we swallow.
//
// BaseURL is the floor; user config still overrides via llm.WithBaseURL.
func init() {
	registerOpenAIResponsesShim()
	registerAnthropicNoTopPShim()
	registerCompat("mistral", "https://api.mistral.ai/v1")
	registerCompat("mlx", "http://localhost:8080/v1")
	registerCompat("lmstudio", "http://localhost:1234/v1")
}

func registerCompat(name, defaultBaseURL string) {
	_ = litellm.RegisterProvider(name, func(cfg litellm.ProviderConfig) litellm.Provider {
		return providers.NewOpenAICompat(cfg, providers.Compat{
			ProviderName:   name,
			DefaultBaseURL: defaultBaseURL,
		})
	})
}

func registerOpenAIResponsesShim() {
	providers.RegisterBuiltin("openai", func(cfg providers.ProviderConfig) providers.Provider {
		return &openAIResponsesProvider{inner: providers.NewOpenAI(cfg)}
	}, "https://api.openai.com")
}

func registerAnthropicNoTopPShim() {
	providers.RegisterBuiltin("anthropic", func(cfg providers.ProviderConfig) providers.Provider {
		return &anthropicNoTopPProvider{inner: providers.NewAnthropic(cfg)}
	}, "https://api.anthropic.com")
}

type openAIResponsesProvider struct {
	inner *providers.OpenAIProvider
}

func (p *openAIResponsesProvider) Name() string    { return p.inner.Name() }
func (p *openAIResponsesProvider) Validate() error { return p.inner.Validate() }
func (p *openAIResponsesProvider) ListModels(ctx context.Context) ([]providers.ModelInfo, error) {
	return p.inner.ListModels(ctx)
}

func (p *openAIResponsesProvider) Chat(ctx context.Context, req *providers.Request) (*providers.Response, error) {
	if useOpenAIResponses(req) {
		return p.inner.Responses(ctx, toOpenAIResponsesRequest(req))
	}
	return p.inner.Chat(ctx, req)
}

func (p *openAIResponsesProvider) Stream(ctx context.Context, req *providers.Request) (providers.StreamReader, error) {
	if useOpenAIResponses(req) {
		return p.inner.ResponsesStream(ctx, toOpenAIResponsesRequest(req))
	}
	return p.inner.Stream(ctx, req)
}

func useOpenAIResponses(req *providers.Request) bool {
	return req != nil && isOpenAIReasoningModel(req.Model)
}

func isOpenAIReasoningModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	return strings.HasPrefix(model, "gpt-5")
}

func toOpenAIResponsesRequest(req *providers.Request) *providers.OpenAIResponsesRequest {
	if req == nil {
		return nil
	}
	out := &providers.OpenAIResponsesRequest{
		Model:           req.Model,
		Messages:        req.Messages,
		MaxOutputTokens: req.MaxTokens,
		ResponseFormat:  req.ResponseFormat,
		Tools:           openAIResponsesTools(req.Tools),
		ToolChoice:      req.ToolChoice,
		Thinking:        req.Thinking,
		APIKey:          req.APIKey,
		OnPayload:       req.OnPayload,
	}
	if !isOpenAIReasoningModel(req.Model) {
		out.Temperature = req.Temperature
		out.TopP = req.TopP
	}
	if req.Extra != nil {
		if v, ok := req.Extra["prompt_cache_key"].(string); ok {
			out.PromptCacheKey = v
		}
		if v, ok := req.Extra["prompt_cache_retention"].(string); ok {
			out.PromptCacheRetention = v
		}
	}
	return out
}

func openAIResponsesTools(in []providers.Tool) []providers.Tool {
	if len(in) == 0 {
		return nil
	}
	out := make([]providers.Tool, len(in))
	copy(out, in)
	strict := false
	for i := range out {
		out[i].Function.Strict = &strict
	}
	return out
}

type anthropicNoTopPProvider struct {
	inner *providers.AnthropicProvider
}

func (p *anthropicNoTopPProvider) Name() string    { return p.inner.Name() }
func (p *anthropicNoTopPProvider) Validate() error { return p.inner.Validate() }
func (p *anthropicNoTopPProvider) ListModels(ctx context.Context) ([]providers.ModelInfo, error) {
	return p.inner.ListModels(ctx)
}

func (p *anthropicNoTopPProvider) Chat(ctx context.Context, req *providers.Request) (*providers.Response, error) {
	return p.inner.Chat(ctx, anthropicRequestWithoutConflictingTopP(req))
}

func (p *anthropicNoTopPProvider) Stream(ctx context.Context, req *providers.Request) (providers.StreamReader, error) {
	return p.inner.Stream(ctx, anthropicRequestWithoutConflictingTopP(req))
}

func anthropicRequestWithoutConflictingTopP(req *providers.Request) *providers.Request {
	if req == nil || req.Temperature == nil || req.TopP == nil {
		return req
	}
	clone := *req
	clone.TopP = nil
	return &clone
}
