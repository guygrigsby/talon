// Package main implements `talon gateway diagnostics export`.
//
// Writes a payload-free .zip containing what a support reviewer
// realistically needs to debug a gateway issue:
//   - manifest.json:  timestamp, talon/server version, contents
//   - config.json:    merged config with secrets redacted
//   - paths.json:     resolved Talon paths
//   - health.json:    output of `health` RPC (when reachable)
//   - logs/config-audit.jsonl: tail of the config-write audit log
//
// Sanitization rules: redact anything that looks like a credential
// before it lands in the zip. The redactor walks the merged config
// recursively and replaces sensitive leaf values with the literal
// string "[REDACTED]" — preserving structure so reviewers can still
// see WHICH secrets are configured without exposing them.

package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
)

// sensitiveKeyParts are case-insensitive substrings that flag a leaf
// value as a credential. Generous matching: false positives become
// "[REDACTED]" placeholders, false negatives leak secrets — favor
// the former. Reviewers can ask the user for specific fields if a
// non-secret happens to match.
var sensitiveKeyParts = []string{
	"token",
	"password",
	"secret",
	"apikey",
	"api_key",
	"private",
	"credential",
	"auth",
	"botToken",
	"signing",
}

func diagnosticsExportRunE(opts diagnosticsExportOpts) error {
	paths := resolvePaths()
	now := time.Now()

	outPath := opts.output
	if outPath == "" {
		outPath = fmt.Sprintf("talon-diagnostics-%s.zip", now.Format("20060102-150405"))
	}
	abs, err := filepath.Abs(outPath)
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}

	f, err := os.Create(abs)
	if err != nil {
		return fmt.Errorf("create %s: %w", abs, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	included := []string{}

	// 1. Merged config (redacted). Reviewers need this to understand
	//    what's configured; the redactor strips credentials.
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return fmt.Errorf("read merged config: %w", err)
	}
	redacted, err := redactConfigJSON(merged)
	if err != nil {
		return fmt.Errorf("redact config: %w", err)
	}
	if err := writeZipFile(zw, "config.json", redacted); err != nil {
		return err
	}
	included = append(included, "config.json")

	// 2. Paths layout (where the config + state live).
	pathsJSON, _ := json.MarshalIndent(map[string]any{
		"talon": map[string]string{
			"dir":    paths.Talon.Dir,
			"config": paths.Talon.Config,
		},
	}, "", "  ")
	if err := writeZipFile(zw, "paths.json", pathsJSON); err != nil {
		return err
	}
	included = append(included, "paths.json")

	// 3. Health snapshot. Best-effort — when the gateway is
	//    unreachable we still emit the rest of the bundle. Skipped
	//    when --timeout is 0 or the user forces it via env.
	if opts.timeoutMs > 0 {
		if snap, healthErr := tryFetchHealth(opts.timeoutMs); healthErr == nil {
			snapJSON, _ := json.MarshalIndent(snap, "", "  ")
			if err := writeZipFile(zw, "health.json", snapJSON); err != nil {
				return err
			}
			included = append(included, "health.json")
		} else {
			// Record the failure so reviewers know the gateway was
			// down at export time (vs. the user just forgetting).
			errPayload, _ := json.MarshalIndent(map[string]any{
				"error":     healthErr.Error(),
				"timeoutMs": opts.timeoutMs,
			}, "", "  ")
			if err := writeZipFile(zw, "health.error.json", errPayload); err != nil {
				return err
			}
			included = append(included, "health.error.json")
		}
	}

	// 4. Tail of the config-audit log. Useful for tracing recent
	//    config writes during incident review. Best-effort: file
	//    may not exist on a fresh install.
	auditPath := filepath.Join(paths.Talon.Dir, "logs", "config-audit.jsonl")
	if tail, err := tailFile(auditPath, opts.logLines, opts.logBytes); err == nil && len(tail) > 0 {
		if err := writeZipFile(zw, "logs/config-audit.jsonl", tail); err != nil {
			return err
		}
		included = append(included, "logs/config-audit.jsonl")
	}

	// 5. Manifest last so it reflects the final included list.
	manifest, _ := json.MarshalIndent(map[string]any{
		"format":      "talon-diagnostics-v1",
		"createdAt":   now.UTC().Format(time.RFC3339),
		"talonClient": talonVersion,
		"contents":    included,
		"redaction": map[string]any{
			"appliedTo":   []string{"config.json"},
			"keyPatterns": sensitiveKeyParts,
			"placeholder": "[REDACTED]",
		},
	}, "", "  ")
	if err := writeZipFile(zw, "manifest.json", manifest); err != nil {
		return err
	}

	if opts.jsonOut {
		out, _ := json.Marshal(map[string]any{"output": abs, "contents": included})
		fmt.Fprintln(os.Stdout, string(out))
	} else {
		fmt.Fprintf(os.Stdout, "Wrote %s\n", abs)
		for _, name := range included {
			fmt.Fprintf(os.Stdout, "  + %s\n", name)
		}
	}
	return nil
}

type diagnosticsExportOpts struct {
	output    string
	logLines  int
	logBytes  int
	timeoutMs int
	jsonOut   bool
}

// writeZipFile writes name+body into zw. Errors include the
// filename so caller messages are self-explanatory.
func writeZipFile(zw *zip.Writer, name string, body []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("zip create %s: %w", name, err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("zip write %s: %w", name, err)
	}
	return nil
}

// redactConfigJSON parses raw JSON config bytes, walks the tree, and
// replaces any leaf value whose key matches sensitiveKeyParts with
// "[REDACTED]". Returns pretty-printed JSON for diff-friendly
// reading. Non-object inputs (rare but possible if the config is
// malformed) are passed through unredacted.
func redactConfigJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Don't fail the whole export over a malformed config —
		// emit the raw bytes with a comment header so reviewers
		// can spot the parse failure.
		return raw, nil
	}
	redactWalk(v, "")
	return json.MarshalIndent(v, "", "  ")
}

// redactWalk recursively redacts in-place. parentKey carries the
// key under which v was found; the leaf check applies only when v
// is a string/number/bool whose parentKey matches a pattern. Maps
// and slices recurse without redacting their structural shape.
func redactWalk(v any, parentKey string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if shouldRedact(k) && isLeafSecret(child) {
				x[k] = "[REDACTED]"
				continue
			}
			redactWalk(child, k)
		}
	case []any:
		for _, child := range x {
			redactWalk(child, parentKey)
		}
	}
}

// shouldRedact returns true when key contains any sensitiveKeyParts
// substring (case-insensitive). Substring match catches camelCase
// ("botToken") and snake_case ("api_key") variants alike.
func shouldRedact(key string) bool {
	if key == "" {
		return false
	}
	lower := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, strings.ToLower(part)) {
			return true
		}
	}
	return false
}

// isLeafSecret returns true when v is the kind of value worth
// redacting — non-empty strings, numeric ids. Empty values stay
// empty (signal: "configured but blank") and structural values
// (maps, slices) recurse instead of redacting wholesale.
func isLeafSecret(v any) bool {
	switch x := v.(type) {
	case string:
		return x != ""
	case float64:
		return true
	case bool:
		// e.g. "tokenEnabled": true — preserve structural booleans.
		return false
	}
	return false
}

// tailFile reads the last min(limit, lines-in-file) lines of path,
// up to maxBytes total. Returns nil bytes (no error) when path is
// missing — caller treats that as "no audit log to include."
func tailFile(path string, limit, maxBytes int) ([]byte, error) {
	if limit <= 0 {
		limit = 5000
	}
	if maxBytes <= 0 {
		maxBytes = 1000000
	}
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read at most maxBytes from the tail of the file. For short
	// files we just read the whole thing.
	size := st.Size()
	start := int64(0)
	if size > int64(maxBytes) {
		start = size - int64(maxBytes)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// When we started mid-file, drop the partial first line.
	if start > 0 {
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	// Cap to last `limit` lines.
	lines := strings.Split(string(buf), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// tryFetchHealth issues the `health` RPC. Returns the raw payload
// on success; caller decides how to fold the result into the zip.
// Wraps the dial in a timeout so a hung gateway doesn't stall the
// export forever.
func tryFetchHealth(timeoutMs int) (any, error) {
	if timeoutMs <= 0 {
		return nil, fmt.Errorf("timeout disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	cli, _, err := dialFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial gateway: %w", err)
	}
	defer cli.Close()
	raw, err := cli.Request(ctx, "health", nil)
	if err != nil {
		return nil, fmt.Errorf("health rpc: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("health decode: %w", err)
	}
	return v, nil
}
