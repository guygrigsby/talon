package talonconfig

import (
	"strings"
	"testing"
)

func TestFromLegacyJSON_MapsCoreConfig(t *testing.T) {
	cfg, report, err := FromLegacyJSON([]byte(`{
		"gateway": {"mode": "local", "bind": "loopback", "port": 18789, "auth": {"mode": "token", "token": "literal-token"}},
		"agents": {
			"defaults": {
				"model": {"primary": "openai/gpt-5.4-mini", "fallbacks": ["anthropic/claude-opus-4-7"]},
				"workspace": "/tmp/main",
				"models": {"openai/gpt-5.4-mini": {"alias": "mini"}}
			},
			"list": [
				{"id": "main", "model": "openai/gpt-4o-mini", "workspace": "/tmp/main-agent"},
				{"id": "coding", "name": "Coding", "model": "deepseek/deepseek-chat", "workspace": "/tmp/should-not-copy"}
			]
		},
		"models": {"providers": {"openai": {"api": "responses", "baseUrl": "https://api.openai.com/v1", "models": [
			{"id": "gpt-4o-mini", "name": "GPT-4o mini", "contextWindow": 128000, "maxTokens": 16384, "input": ["text"], "reasoning": false, "cost": {"input": 0.15, "output": 0.6}}
		]}}},
		"plugins": {"entries": {"openai-compat": {"enabled": true, "config": {"providers": {"openai": {"apiKey": "op://vault/openai/key"}}}}}},
		"channels": {"telegram": {"enabled": true, "botToken": "123:secret", "allowFrom": ["42"], "groups": {"*": {"requireMention": true}}}},
		"memory": {"enabled": true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Model != "openai/gpt-4o-mini" {
		t.Fatalf("agent model = %q", cfg.Agent.Model)
	}
	if cfg.Agent.Workspace != "/tmp/main-agent" {
		t.Fatalf("main workspace = %q", cfg.Agent.Workspace)
	}
	if len(cfg.Subagents) != 1 || cfg.Subagents[0].ID != "coding" || cfg.Subagents[0].Model != "deepseek/deepseek-chat" {
		t.Fatalf("subagents = %+v", cfg.Subagents)
	}
	if cfg.Subagents[0].Name != "Coding" {
		t.Fatalf("subagent name = %q", cfg.Subagents[0].Name)
	}
	if len(cfg.Models.Providers) != 1 || cfg.Models.Providers["openai"].APIKey != "op://vault/openai/key" {
		t.Fatalf("providers = %+v", cfg.Models.Providers)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.BotToken == "" || len(cfg.Telegram.AllowFrom) != 1 {
		t.Fatalf("telegram = %+v", cfg.Telegram)
	}
	if report.SecretCounts["literal"] != 2 || report.SecretCounts["op-ref"] != 1 {
		t.Fatalf("secret counts = %+v", report.SecretCounts)
	}
}

func TestMarshalTOML_RedactsLiteralSecretsAndDropsSubagentWorkspace(t *testing.T) {
	cfg, _, err := FromLegacyJSON([]byte(`{
		"gateway": {"auth": {"mode": "token", "token": "literal-token"}},
		"agents": {"defaults": {"model": {"primary": "openai/gpt-5.4-mini"}}, "list": [
			{"id": "coding", "model": "deepseek/deepseek-chat", "workspace": "/tmp/legacy-workspace"}
		]},
		"plugins": {"entries": {
			"brave": {"config": {"webSearch": {"apiKey": "literal-brave"}}},
			"openai-compat": {"config": {"providers": {"openai": {"apiKey": "op://vault/openai/key"}}}}
		}},
		"channels": {"telegram": {"botToken": "123:secret"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out := string(MarshalTOML(cfg, MarshalOptions{RedactSecrets: true}))
	for _, want := range []string{
		`auth_token_ref = "<redacted:literal>"`,
		`bot_token_ref = "<redacted:literal>"`,
		`api_key_ref = "op://vault/openai/key"`,
		`web_search_api_key_ref = "<redacted:literal>"`,
		`[[subagents]]`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("TOML missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/tmp/legacy-workspace") {
		t.Fatalf("subagent workspace leaked into native config:\n%s", out)
	}
	parsed, err := ReadTOMLBytes([]byte(out))
	if err != nil {
		t.Fatalf("viper should parse generated TOML: %v\n%s", err, out)
	}
	if parsed.Gateway.AuthToken != "<redacted:literal>" {
		t.Fatalf("parsed auth token = %q", parsed.Gateway.AuthToken)
	}
	if parsed.Telegram.BotToken != "<redacted:literal>" {
		t.Fatalf("parsed telegram token = %q", parsed.Telegram.BotToken)
	}
}

func TestToLegacyJSON_UsesFallbackForRedactedSecretsAndDropsSubagentWorkspace(t *testing.T) {
	cfg, err := ReadTOMLBytes([]byte(`
[gateway]
mode = "local"
bind = "loopback"
port = 19000
auth_mode = "token"
auth_token_ref = "<redacted:literal>"

[agent]
model = "openai/gpt-5.4-mini"
workspace = "/tmp/main"
tools_profile = "full"

[[subagents]]
id = "coding"
name = "Coding"
model = "anthropic/claude-opus-4-7"
tools_profile = "full"

[tools]
profile = "full"
web_search_enabled = true
web_search_provider = "brave"

[models.providers.openai]
api = "responses"
base_url = "https://api.openai.com/v1"
api_key_ref = "op://vault/openai/key"

[[models.providers.openai.models]]
id = "gpt-4o-mini"
name = "GPT-4o mini"
context_window = 128000
max_tokens = 16384
input = ["text"]
reasoning = false
cost_input = 0.15
cost_output = 0.6

[models.providers.anthropic]
api_key_ref = "op://vault/anthropic/key"

[channels.telegram]
enabled = true
bot_token_ref = "<redacted:literal>"
allow_from = ["42"]
agent_id = "main"
require_mention = true

[plugins]
enabled = ["anthropic", "brave", "openai-compat"]
deny = ["bonjour"]
load_paths = ["/plugins"]
legacy_openclaw_shim = true
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ToLegacyJSON(cfg, []byte(`{
		"gateway": {"auth": {"token": "gateway-secret"}},
		"agents": {"list": [{"id": "coding", "workspace": "/tmp/should-not-survive"}]},
		"plugins": {"entries": {
			"brave": {"config": {"webSearch": {"apiKey": "brave-secret"}}},
			"openai-compat": {"config": {"providers": {"openai": {"apiKey": "old-openai"}}}}
		}},
		"channels": {"telegram": {"botToken": "telegram-secret"}},
		"skills": {"entries": {"openai-whisper-api": {"apiKey": "whisper-secret"}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, bad := range []string{redactedLiteral, "/tmp/should-not-survive"} {
		if strings.Contains(text, bad) {
			t.Fatalf("legacy output contains %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{
		`"gateway-secret"`,
		`"telegram-secret"`,
		`"brave-secret"`,
		`"whisper-secret"`,
		`"op://vault/openai/key"`,
		`"op://vault/anthropic/key"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("legacy output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"workspace":"/tmp/should-not-survive"`) {
		t.Fatalf("subagent workspace survived:\n%s", text)
	}
	if strings.Contains(text, `"workspace":"/tmp/main"`) && !strings.Contains(text, `"id":"main"`) {
		t.Fatalf("main workspace should be tied to main agent:\n%s", text)
	}
}
