package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

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
//
// The result is cached against (path, size, mtime) for both layers; a
// subsequent call with unchanged inputs returns the prior result
// without re-reading or re-merging. Every read RPC the gateway serves
// (agents.list, models.list, agent.identity.get, config.get,
// images.list, plugins.deps.status, ...) calls this, so the cache
// matters: a UI panel render fires several reads in quick succession
// and they all see the same on-disk state.
//
// The returned []byte is shared across callers and MUST NOT be
// mutated. Every internal caller treats it read-only (gjson.GetBytes
// or json.Unmarshal); add a defensive copy at the boundary if a
// future caller wants to write into it.
func MergedBytes(p openclaw.Paths) ([]byte, error) {
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

func mergedBytesUncached(p openclaw.Paths) ([]byte, error) {
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

// mergedCacheKey identifies one (path, mtime, size) tuple per layer
// plus the SkipOpenclaw flag. Stat-based invalidation matches what
// every config write triggers: Set always touches the talon overlay,
// which bumps mtime+size, which invalidates the cache on the next
// read. Sub-second writes are fine — modern filesystems give
// nanosecond mtime resolution.
type mergedCacheKey struct {
	talonPath       string
	talonSize       int64
	talonMtimeNs    int64
	openclawPath    string
	openclawSize    int64
	openclawMtimeNs int64
	skipOpenclaw    bool
}

var (
	mergedCacheMu    sync.RWMutex
	mergedCacheEntry struct {
		key   mergedCacheKey
		bytes []byte
	}
)

func buildMergedCacheKey(p openclaw.Paths) mergedCacheKey {
	k := mergedCacheKey{
		talonPath:    p.Talon.Config,
		openclawPath: p.Openclaw.Config,
		skipOpenclaw: p.SkipOpenclaw,
	}
	if st, err := os.Stat(p.Talon.Config); err == nil {
		k.talonSize = st.Size()
		k.talonMtimeNs = st.ModTime().UnixNano()
	}
	if !p.SkipOpenclaw {
		if st, err := os.Stat(p.Openclaw.Config); err == nil {
			k.openclawSize = st.Size()
			k.openclawMtimeNs = st.ModTime().UnixNano()
		}
	}
	return k
}

// invalidateMergedCacheForTest drops the cached entry. Tests that
// manipulate config files between calls and don't go through Set
// (which already invalidates on write) call this to force a re-read.
// Production code shouldn't need this — stat changes do the work.
func invalidateMergedCacheForTest() {
	mergedCacheMu.Lock()
	mergedCacheEntry.key = mergedCacheKey{}
	mergedCacheEntry.bytes = nil
	mergedCacheMu.Unlock()
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
