package openai

import (
	"encoding/json"
	"fmt"
	"os"
)

// authProfilesFile is the on-disk shape of openclaw's auth-profiles.json.
// We only decode the fields we use; everything else is ignored.
type authProfilesFile struct {
	Version  int                       `json:"version"`
	Profiles map[string]authProfileRow `json:"profiles"`
}

type authProfileRow struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"` // present for type=api_key
}

// LoadAPIKey extracts the OpenAI API key from an openclaw-style
// auth-profiles.json at path. profileID is typically "openai:default" — the
// canonical id for the OpenAI api_key profile in openclaw's auth store. The
// profile's type must be "api_key" and provider must be "openai", otherwise
// LoadAPIKey returns a descriptive error rather than a key that would fail
// downstream.
func LoadAPIKey(path, profileID string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("openai: read auth profiles %s: %w", path, err)
	}
	var f authProfilesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("openai: parse auth profiles: %w", err)
	}
	row, ok := f.Profiles[profileID]
	if !ok {
		return "", fmt.Errorf("openai: profile %q not found in %s", profileID, path)
	}
	if row.Type != "api_key" {
		return "", fmt.Errorf("openai: profile %q has type %q, want api_key", profileID, row.Type)
	}
	if row.Provider != "openai" {
		return "", fmt.Errorf("openai: profile %q has provider %q, want openai", profileID, row.Provider)
	}
	if row.Key == "" {
		return "", fmt.Errorf("openai: profile %q has empty key", profileID)
	}
	return row.Key, nil
}
