package chatdriver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/provider/openai"
	"github.com/guygrigsby/talon/internal/secrets"
)

// ProviderAuth carries the resolved credentials needed to call one
// provider via agentcore/llm. BaseURL applies to OpenAI-compatible
// providers (every cloud and local LLM service except Anthropic's
// native Messages API).
type ProviderAuth struct {
	// Provider is the canonical name ("openai", "anthropic",
	// "deepseek", "mistral", "mlx", "lmstudio", "ollama", …).
	Provider string
	// BaseURL is the chat-completions endpoint root. Empty when
	// agentcore/llm should use its own default for the provider
	// (Anthropic's case; openai-compat needs it explicit).
	BaseURL string
	// APIKey is the literal credential. May be empty for loopback
	// servers (LM Studio without auth, MLX, Ollama).
	APIKey string
}

// secretsTimeout caps how long a single op:// or keychain://
// resolution may take. Keychain access is sub-ms; 1Password CLI
// can take a second or two cold.
const secretsTimeout = 15 * time.Second

// ResolveProviderAuth resolves every configured provider's credentials
// to literal values:
//
//  1. plugins.entries.openai-compat.config.providers.<name>.apiKey
//     (or plugins.entries.anthropic.config.apiKey for anthropic)
//  2. <NAME>_API_KEY env var
//  3. <NAME>_API_KEY_REF env var (a secret reference)
//  4. ~/.talon/agents/main/agent/auth-profiles.json profile
//     "<name>:default"
//
// Step 1 values, env REFs, and profile keys may be op:// /
// keychain:// references; those are passed through
// secrets.ResolveOrLiteral.
//
// Providers whose auth can't be resolved are omitted from the
// returned map — callers decide whether that's an error (chat
// requested against the missing provider) or fine (provider just
// won't appear in the picker).
func ResolveProviderAuth(merged []byte, paths talonpath.Paths) map[string]ProviderAuth {
	out := map[string]ProviderAuth{}
	authPath := filepath.Join(paths.Talon.Dir, "agents", "main", "agent", "auth-profiles.json")
	if paths.Talon.Dir == "" {
		// Tests typically pass an empty Paths; fall through with
		// a path that doesn't exist so the profile step no-ops.
		authPath = ""
	}

	// openai-compat tenants — multi-provider config block.
	gjson.GetBytes(merged, "plugins.entries.openai-compat.config.providers").ForEach(func(name, prov gjson.Result) bool {
		pname := name.Str
		if pname == "" {
			return true
		}
		entry := ProviderAuth{
			Provider: pname,
			BaseURL:  prov.Get("baseUrl").Str,
		}
		key := resolveOneKey(pname, prov.Get("apiKey").Str, authPath)
		if key == "" && !isLoopbackURL(entry.BaseURL) {
			// Skip — no auth for a non-loopback provider.
			return true
		}
		entry.APIKey = key
		out[pname] = entry
		return true
	})

	// Anthropic plugin entry — single-tenant.
	anthropicKey := gjson.GetBytes(merged, "plugins.entries.anthropic.config.apiKey").Str
	if k := resolveOneKey("anthropic", anthropicKey, authPath); k != "" {
		out["anthropic"] = ProviderAuth{
			Provider: "anthropic",
			APIKey:   k,
		}
	}

	return out
}

// resolveOneKey runs the auth chain for a single provider and
// returns the literal credential (or "" when none was found).
// Errors from the secrets resolver collapse to "" — callers can't
// distinguish "wrong ref" from "no ref configured" here, which is
// the right behavior: both mean "this provider is unavailable."
func resolveOneKey(provider, fromConfig, authPath string) string {
	if k := maybeResolveRef(fromConfig); k != "" {
		return k
	}

	envName := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	if ref := strings.TrimSpace(os.Getenv(envName + "_REF")); ref != "" {
		if k := maybeResolveRef(ref); k != "" {
			return k
		}
	}

	if authPath != "" {
		if k, err := openai.LoadProfileKeyOptional(authPath, provider+":default", provider); err == nil && k != "" {
			return maybeResolveRef(k)
		}
	}
	return ""
}

// maybeResolveRef passes literal values through unchanged and
// resolves op:// / keychain:// references to their cleartext.
// Returns "" on resolver error so the caller's "is this empty"
// check still works.
func maybeResolveRef(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !secrets.IsReference(v) {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsTimeout)
	defer cancel()
	resolved, err := secrets.ResolveOrLiteral(ctx, v)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(resolved)
}

// isLoopbackURL reports whether u points at the local machine.
// Local OpenAI-compatible servers (LM Studio, MLX, Ollama, llama.cpp)
// usually run without auth, so the auth chain treats a loopback
// URL as "no key needed" rather than skipping the provider.
func isLoopbackURL(u string) bool {
	low := strings.ToLower(u)
	return strings.Contains(low, "://localhost") ||
		strings.Contains(low, "://127.0.0.1") ||
		strings.Contains(low, "://[::1]") ||
		strings.Contains(low, "://0.0.0.0")
}

// AuthFingerprint returns a short, secret-safe identifier for an
// auth entry — useful for logs and tests that need to assert "the
// right key landed here" without surfacing the key itself. Uses the
// SHA256 prefix; empty key returns "".
//
// (Implemented because the secrets-dump incident burned us once:
// never let cleartext into logs even for verification.)
func AuthFingerprint(a ProviderAuth) string {
	if a.APIKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(a.APIKey))
	return fmt.Sprintf("%s:%x", a.Provider, sum[:4])
}
