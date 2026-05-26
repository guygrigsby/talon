package agentcore_chat

import (
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
