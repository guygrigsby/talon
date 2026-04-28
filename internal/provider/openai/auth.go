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
// auth-profiles.json. Sugar over LoadProfileKey with profileID
// "openai:default" and expectedProvider "openai".
func LoadAPIKey(path, profileID string) (string, error) {
	return LoadProfileKey(path, profileID, "openai")
}

// LoadProfileKeyOptional behaves like LoadProfileKey except a missing
// auth-profiles.json file OR a missing profileID inside it returns
// ("", nil) instead of an error. Use for providers where auth is
// optional (e.g. local LM Studio servers) so a fresh install Just
// Works without forcing the user to create the file. Malformed
// profiles (wrong type, mismatched provider, empty key) still error
// because those represent intent the user got wrong.
func LoadProfileKeyOptional(path, profileID, expectedProvider string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("%s: read auth profiles %s: %w", expectedProvider, path, err)
	}
	var f authProfilesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("%s: parse auth profiles: %w", expectedProvider, err)
	}
	row, ok := f.Profiles[profileID]
	if !ok {
		return "", nil // profile not present — caller decides what to do
	}
	if row.Type != "api_key" {
		return "", fmt.Errorf("%s: profile %q has type %q, want api_key", expectedProvider, profileID, row.Type)
	}
	if row.Provider != expectedProvider {
		return "", fmt.Errorf("%s: profile %q has provider %q, want %s", expectedProvider, profileID, row.Provider, expectedProvider)
	}
	if row.Key == "" {
		return "", fmt.Errorf("%s: profile %q has empty key", expectedProvider, profileID)
	}
	return row.Key, nil
}

// LoadProfileKey is the generalized auth-profile reader other
// OpenAI-compatible providers (DeepSeek, etc.) reuse. The profile must
// have type="api_key", a non-empty key, and provider matching
// expectedProvider — caller is responsible for the right pairing of
// profileID + expectedProvider.
func LoadProfileKey(path, profileID, expectedProvider string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: read auth profiles %s: %w", expectedProvider, path, err)
	}
	var f authProfilesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("%s: parse auth profiles: %w", expectedProvider, err)
	}
	row, ok := f.Profiles[profileID]
	if !ok {
		return "", fmt.Errorf("%s: profile %q not found in %s", expectedProvider, profileID, path)
	}
	if row.Type != "api_key" {
		return "", fmt.Errorf("%s: profile %q has type %q, want api_key", expectedProvider, profileID, row.Type)
	}
	if row.Provider != expectedProvider {
		return "", fmt.Errorf("%s: profile %q has provider %q, want %s", expectedProvider, profileID, row.Provider, expectedProvider)
	}
	if row.Key == "" {
		return "", fmt.Errorf("%s: profile %q has empty key", expectedProvider, profileID)
	}
	return row.Key, nil
}
