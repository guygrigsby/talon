package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/guygrigsby/talon/internal/openclaw"
)

// Config is the subset of openclaw.json talon types directly. Only fields
// talon's own code reads need to appear here; everything else is preserved as
// raw JSON via the dot-path edit layer and never goes through this struct.
type Config struct {
	Gateway Gateway `json:"gateway"`
}

type Gateway struct {
	Mode string      `json:"mode"`
	Port int         `json:"port"`
	Bind string      `json:"bind"`
	Auth GatewayAuth `json:"auth"`
}

type GatewayAuth struct {
	Mode  string `json:"mode"`
	Token string `json:"token"`
}

// DefaultPaths returns the default openclaw.Paths.
func DefaultPaths() openclaw.Paths { return openclaw.DefaultPaths() }

// MergedBytes loads both layers and returns the deep-merged JSON
// (talon-priority). Either layer may be missing — only their absence-as-file
// is tolerated; if a layer exists but isn't valid JSON, that's an error.
func MergedBytes(p openclaw.Paths) ([]byte, error) {
	openclawRaw, err := readOptional(p.Openclaw.Config, p.SkipOpenclaw)
	if err != nil {
		return nil, err
	}
	talonRaw, err := readOptional(p.Talon.Config, false)
	if err != nil {
		return nil, err
	}
	if openclawRaw == nil && talonRaw == nil {
		return []byte("{}"), nil
	}
	if openclawRaw == nil {
		return canonicalize(talonRaw)
	}
	if talonRaw == nil {
		return canonicalize(openclawRaw)
	}
	return mergeJSON(openclawRaw, talonRaw)
}

// Load returns a typed Config built from the merged view. Use MergedBytes
// (or Get) to read fields outside the typed schema.
func Load(p openclaw.Paths) (*Config, error) {
	merged, err := MergedBytes(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(merged, &c); err != nil {
		return nil, fmt.Errorf("parse merged config: %w", err)
	}
	if c.Gateway.Port == 0 {
		c.Gateway.Port = 18789
	}
	return &c, nil
}

// GatewayURL returns the websocket URL talon clients should dial.
func (c *Config) GatewayURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d/", c.Gateway.Port)
}

// readOptional reads a file or returns (nil, nil) when it does not exist.
// When skip is true, returns (nil, nil) without touching the path.
func readOptional(path string, skip bool) ([]byte, error) {
	if skip || path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// canonicalize parses then re-marshals so callers always get a consistent
// shape (no trailing whitespace, valid JSON).
func canonicalize(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return json.Marshal(v)
}
