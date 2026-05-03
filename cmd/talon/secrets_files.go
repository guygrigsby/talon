// Package main — auditing secrets that live in OPENCLAW JSON
// files outside the merged config (auth-profiles.json,
// device-auth.json, paired.json, exec-approvals.json). The
// merged-config audit in secrets.go misses these because they're
// loaded directly by the openclaw extension shim, not via
// config.MergedBytes.
//
// Path syntax for these is:
//
//	file://<rel-path-under-openclaw>:<dotted-key-into-json>
//
// e.g. file://agents/main/agent/auth-profiles.json:profiles."openai:default".key
//
// `talon secrets migrate` already speaks dotted paths through
// gjson — extending it to these files just means a different
// reader/writer.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/tidwall/gjson"
)

// fileSecretSources is the inventory of openclaw-layer files we
// know hold credentials. Glob patterns rooted under ~/.openclaw.
// New callers (e.g. a future agent-scoped key) can extend this
// list without touching the audit logic.
var fileSecretSources = []string{
	"agents/*/agent/auth-profiles.json",
	"identity/device-auth.json",
	"devices/paired.json",
	"exec-approvals.json",
}

// auditFileSecrets walks fileSecretSources rooted under
// openclawDir and returns one secretsAuditEntry per sensitive
// leaf. Same shape as auditSecrets (which handles the merged
// config) so the two outputs concatenate cleanly.
//
// Path format for these entries: "file://<rel>:<key>" so callers
// can route them to the right reader/writer (vs. the dotted
// config syntax for merged-config entries).
func auditFileSecrets(openclawDir string) []secretsAuditEntry {
	out := []secretsAuditEntry{}
	if openclawDir == "" {
		return out
	}
	for _, pattern := range fileSecretSources {
		matches, err := filepath.Glob(filepath.Join(openclawDir, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			rel, err := filepath.Rel(openclawDir, m)
			if err != nil {
				rel = m
			}
			out = append(out, scanFileForSecrets(m, rel)...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// scanFileForSecrets reads path as JSON, walks it for sensitive
// leaves, and returns audit entries with the file:// path form.
// Unparseable / unreadable files emit a single error-shaped entry
// rather than silently disappearing — under-reporting is the
// failure mode we explicitly do not want.
func scanFileForSecrets(absPath, relPath string) []secretsAuditEntry {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return []secretsAuditEntry{{
			Path:  "file://" + relPath,
			Kind:  "error",
			Value: err.Error(),
		}}
	}
	if !json.Valid(raw) {
		return []secretsAuditEntry{{
			Path:  "file://" + relPath,
			Kind:  "error",
			Value: "invalid JSON",
		}}
	}
	out := []secretsAuditEntry{}
	walkSecretLeavesFile(gjson.ParseBytes(raw), "", &out, "file://"+relPath+":")
	return out
}

// walkSecretLeavesFile is a sibling of secrets.go's walkSecretLeaves
// that prepends a fixed prefix (the file:// scheme + filename) to
// every emitted path. Same in-place classifier; just the path
// rewriting differs.
func walkSecretLeavesFile(v gjson.Result, prefix string, out *[]secretsAuditEntry, basePrefix string) {
	if v.IsObject() {
		v.ForEach(func(key, val gjson.Result) bool {
			child := key.Str
			path := child
			if prefix != "" {
				path = prefix + "." + child
			}
			if secrets.IsSensitiveKey(child) && val.Type == gjson.String {
				e := classifyLeaf(path, val)
				e.Path = basePrefix + e.Path
				*out = append(*out, e)
				return true
			}
			walkSecretLeavesFile(val, path, out, basePrefix)
			return true
		})
		return
	}
	if v.IsArray() {
		i := 0
		v.ForEach(func(_, val gjson.Result) bool {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			i++
			walkSecretLeavesFile(val, path, out, basePrefix)
			return true
		})
	}
}
