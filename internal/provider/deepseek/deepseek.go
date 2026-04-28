// Package deepseek wires the DeepSeek API into provider.Provider. DeepSeek
// implements OpenAI's chat-completions wire format, so this package is a
// thin constructor over internal/provider/openai with three configuration
// shifts:
//
//   - BaseURL points at https://api.deepseek.com/v1
//   - Provider name reports as "deepseek" (matches the user's openclaw
//     auth profile and the ModelID provider segment in agents.list)
//   - ProviderKey gates ModelID validation so a "deepseek/..." model is
//     accepted but an "openai/..." or "anthropic/..." model is rejected
//
// The openclaw config marks DeepSeek's api as "openai-completions" and
// supplies the same baseUrl: nothing in DeepSeek's API needs separate
// handling for tool calls, streaming, or usage accounting.
package deepseek

import (
	"github.com/guygrigsby/talon/internal/provider/openai"
)

// DefaultBaseURL is DeepSeek's API root. Override via Options.BaseURL on
// New for staging or proxies.
const DefaultBaseURL = "https://api.deepseek.com/v1"

// Options configures a DeepSeek provider. APIKey is required; BaseURL
// defaults to DefaultBaseURL.
type Options struct {
	APIKey  string
	BaseURL string
}

// New returns an *openai.Provider configured for DeepSeek (Name="deepseek",
// ProviderKey="deepseek", BaseURL set). The same Provider type satisfies
// provider.Provider — the gateway's chat handler routes by ModelID
// provider segment, so this just slots into agentProviderFactory.
func New(opts Options) *openai.Provider {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return openai.New(openai.Options{
		APIKey:      opts.APIKey,
		BaseURL:     baseURL,
		Name:        "deepseek",
		ProviderKey: "deepseek",
	})
}

// LoadAPIKey reads the DeepSeek api_key from an openclaw-style
// auth-profiles.json. Convention: profileID = "deepseek:default" with
// type=api_key and provider=deepseek.
func LoadAPIKey(authProfilesPath string) (string, error) {
	return openai.LoadProfileKey(authProfilesPath, "deepseek:default", "deepseek")
}
