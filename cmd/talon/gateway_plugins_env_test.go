package main

import (
	"reflect"
	"sort"
	"testing"
)

// TestParsePluginSpecs_BraveAutoTranslatesAPIKey covers the migration
// path where the brave key lives at
// plugins.entries.brave.config.webSearch.apiKey. parsePluginSpecs should
// auto-translate that path into the BRAVE_API_KEY env var the Go plugin
// reads. Literal key -> BRAVE_API_KEY; reference -> BRAVE_API_KEY_REF.
func TestParsePluginSpecs_BraveAutoTranslatesAPIKey(t *testing.T) {
	body := []byte(`{
		"plugins": {
			"entries": {
				"brave": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-brave-plugin"],
					"config": {"webSearch": {"apiKey": "BSA_literal_key"}}
				}
			}
		}
	}`)
	got := parsePluginSpecs(body, pluginParseDefaults{})
	if len(got) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(got))
	}
	want := []string{"BRAVE_API_KEY=BSA_literal_key"}
	if !reflect.DeepEqual(got[0].env, want) {
		t.Errorf("brave env: got %v, want %v", got[0].env, want)
	}
}

func TestParsePluginSpecs_BraveAPIKeyAsReference(t *testing.T) {
	body := []byte(`{
		"plugins": {
			"entries": {
				"brave": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-brave-plugin"],
					"config": {"webSearch": {"apiKey": "op://Personal/talon-brave/credential"}}
				}
			}
		}
	}`)
	got := parsePluginSpecs(body, pluginParseDefaults{})
	if len(got) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(got))
	}
	want := []string{"BRAVE_API_KEY_REF=op://Personal/talon-brave/credential"}
	if !reflect.DeepEqual(got[0].env, want) {
		t.Errorf("brave env: got %v, want %v", got[0].env, want)
	}
}

func TestParsePluginSpecs_WhisperAutoTranslatesAPIKey(t *testing.T) {
	body := []byte(`{
		"plugins": {
			"entries": {
				"whisper": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-whisper-plugin"],
					"config": {"apiKey": "sk-test"}
				}
			}
		}
	}`)
	got := parsePluginSpecs(body, pluginParseDefaults{})
	if len(got) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(got))
	}
	want := []string{"OPENAI_API_KEY=sk-test"}
	if !reflect.DeepEqual(got[0].env, want) {
		t.Errorf("whisper env: got %v, want %v", got[0].env, want)
	}
}

func TestParsePluginSpecs_ExplicitEnvBlockMerges(t *testing.T) {
	// Both the auto-translation AND explicit env should appear,
	// in deterministic order (alpha by key for the explicit block,
	// auto-translated entries first).
	body := []byte(`{
		"plugins": {
			"entries": {
				"brave": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-brave-plugin"],
					"config": {"webSearch": {"apiKey": "k"}},
					"env": {"DEBUG": "1", "BRAVE_REGION": "us"}
				}
			}
		}
	}`)
	got := parsePluginSpecs(body, pluginParseDefaults{})
	if len(got) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(got))
	}
	// Just check both auto and explicit entries are present;
	// ordering is internal.
	sort.Strings(got[0].env)
	want := []string{"BRAVE_API_KEY=k", "BRAVE_REGION=us", "DEBUG=1"}
	sort.Strings(want)
	if !reflect.DeepEqual(got[0].env, want) {
		t.Errorf("merged env: got %v, want %v", got[0].env, want)
	}
}

func TestParsePluginSpecs_UnknownPluginNoAutoEnv(t *testing.T) {
	// Plugins we don't know about don't get auto-translation;
	// only their explicit env block (if any) lands in env.
	body := []byte(`{
		"plugins": {
			"entries": {
				"random": {
					"enabled": true,
					"cmd": ["/usr/local/bin/talon-random-plugin"],
					"config": {"apiKey": "wat"}
				}
			}
		}
	}`)
	got := parsePluginSpecs(body, pluginParseDefaults{})
	if len(got) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(got))
	}
	if len(got[0].env) != 0 {
		t.Errorf("unknown plugin should have no auto-env: got %v", got[0].env)
	}
}
