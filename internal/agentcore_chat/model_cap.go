package agentcore_chat

import (
	"context"
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/tidwall/gjson"
)

// cappedChatModel wraps an agentcore.ChatModel and injects a default
// WithMaxTokens option into every Generate/GenerateStream call.
// Necessary because agentcore.llm's DefaultGenerationConfig.MaxTokens
// is hard-coded to 65536, which exceeds the per-model caps of
// every model except the 1M-window 4.x series — every other
// request 400s with "max_tokens is too large."
//
// Per the talon convention, the long-term fix is upstream (default
// to 0 / honor model caps). Until that lands, every model talon
// constructs goes through this wrapper with the per-model cap
// read from `models.providers.<provider>.models[id==<model>].maxTokens`.
type cappedChatModel struct {
	inner  agentcore.ChatModel
	maxTok int
}

// newCappedChatModel returns a ChatModel that injects WithMaxTokens
// as the first call option. User-supplied opts (later in the slice)
// can override since agentcore.CallOption is a setter on CallConfig.
func newCappedChatModel(inner agentcore.ChatModel, maxTokens int) agentcore.ChatModel {
	if maxTokens <= 0 {
		return inner
	}
	return &cappedChatModel{inner: inner, maxTok: maxTokens}
}

func (m *cappedChatModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	merged := append([]agentcore.CallOption{agentcore.WithMaxTokens(m.maxTok)}, opts...)
	return m.inner.Generate(ctx, messages, tools, merged...)
}

func (m *cappedChatModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	merged := append([]agentcore.CallOption{agentcore.WithMaxTokens(m.maxTok)}, opts...)
	return m.inner.GenerateStream(ctx, messages, tools, merged...)
}

func (m *cappedChatModel) SupportsTools() bool {
	return m.inner.SupportsTools()
}

// ProviderName satisfies the optional agentcore.ProviderNamer
// interface — the agent loop reads this for per-provider hooks.
// Delegates to the inner model so wrapped LiteLLMAdapters still
// surface their provider name.
func (m *cappedChatModel) ProviderName() string {
	if pn, ok := m.inner.(interface{ ProviderName() string }); ok {
		return pn.ProviderName()
	}
	return ""
}

// resolveModelMaxTokens reads the per-model `maxTokens` value from
// the user's config. The lookup path is:
//
//	models.providers.<provider>.models[id==<modelID>].maxTokens
//
// Returns 0 when no entry is found — caller leaves the model
// uncapped (or uses a sensible default).
func resolveModelMaxTokens(merged []byte, providerName, modelID string) int {
	path := fmt.Sprintf("models.providers.%s.models.#(id==%q).maxTokens", providerName, modelID)
	v := gjson.GetBytes(merged, path)
	if !v.Exists() {
		return 0
	}
	return int(v.Int())
}
