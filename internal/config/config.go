package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
)

// Config is the subset of Talon's config used by CLI dialing. The runtime
// stores native TOML on disk and adapts it to this JSON-shaped view for the
// existing gateway handlers.
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

// DefaultPaths returns the default talonpath.Paths.
func DefaultPaths() talonpath.Paths { return talonpath.DefaultPaths() }

// MergedBytes loads Talon's native TOML config and returns the runtime JSON
// view used by the gateway handlers. The result is cached against the
// config file's size and mtime; a subsequent call with unchanged input returns
// the prior result without re-reading or re-adapting.
//
// The returned []byte is shared across callers and MUST NOT be
// mutated. Every internal caller treats it read-only (gjson.GetBytes
// or json.Unmarshal); add a defensive copy at the boundary if a
// future caller wants to write into it.
func MergedBytes(p talonpath.Paths) ([]byte, error) {
	key := buildMergedCacheKey(p)
	mergedCacheMu.RLock()
	if mergedCacheEntry.key == key && mergedCacheEntry.bytes != nil {
		out := mergedCacheEntry.bytes
		mergedCacheMu.RUnlock()
		return out, nil
	}
	mergedCacheMu.RUnlock()

	out, err := mergedBytesUncached(p)
	if err != nil {
		return nil, err
	}
	mergedCacheMu.Lock()
	mergedCacheEntry.key = key
	mergedCacheEntry.bytes = out
	mergedCacheMu.Unlock()
	return out, nil
}

func mergedBytesUncached(p talonpath.Paths) ([]byte, error) {
	raw, err := readOptional(p.Talon.Config, false)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return []byte("{}"), nil
	}
	nativeCfg, err := talonconfig.ReadTOMLBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse native config %s: %w", p.Talon.Config, err)
	}
	adapted, err := talonconfig.ToRuntimeJSON(nativeCfg, nil)
	if err != nil {
		return nil, fmt.Errorf("adapt native config %s: %w", p.Talon.Config, err)
	}
	return canonicalize(adapted)
}

// mergedCacheKey identifies the config file path, mtime, and size.
// Stat-based invalidation matches what every config write triggers.
type mergedCacheKey struct {
	path    string
	size    int64
	mtimeNs int64
}

var (
	mergedCacheMu    sync.RWMutex
	mergedCacheEntry struct {
		key   mergedCacheKey
		bytes []byte
	}
)

func buildMergedCacheKey(p talonpath.Paths) mergedCacheKey {
	k := mergedCacheKey{path: p.Talon.Config}
	if st, err := os.Stat(p.Talon.Config); err == nil {
		k.size = st.Size()
		k.mtimeNs = st.ModTime().UnixNano()
	}
	return k
}

// Load returns a typed Config built from the merged view. Use MergedBytes
// (or Get) to read fields outside the typed schema.
func Load(p talonpath.Paths) (*Config, error) {
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
