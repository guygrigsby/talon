package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
)

// canonicalPrettyOptions defines the internal JSON view used before converting
// back to native TOML.
var canonicalPrettyOptions = &pretty.Options{Indent: "  ", SortKeys: false, Width: 0}

// canonicalPretty returns the post-pretty form of raw with talon's canonical
// options. The result always ends with a trailing newline (tidwall/pretty
// always emits one as of v1.2.x; the append is defensive).
func canonicalPretty(raw []byte) []byte {
	out := pretty.PrettyOptions(raw, canonicalPrettyOptions)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out
}

// SetMode controls how Set handles existing values at the target path.
type SetMode int

const (
	// SetReplaceSafe replaces the value, but refuses to silently drop
	// entries from protected map/list paths in the merged view (use
	// SetForceReplace to override).
	SetReplaceSafe SetMode = iota
	// SetMerge deep-merges objects and id-keyed arrays at the target path.
	SetMerge
	// SetForceReplace replaces unconditionally, even at protected paths.
	SetForceReplace
)

// SetOpts is the option struct for Set.
type SetOpts struct {
	Mode   SetMode
	DryRun bool
}

// SetResult describes what Set did.
type SetResult struct {
	// Path is the dot-rendered segments of the requested set.
	Path string
	// PrunedPaths lists sibling keys removed from the talon overlay (e.g.
	// gateway.auth.password when gateway.auth.mode changed).
	PrunedPaths []string
	// Wrote is true when the talon overlay was written (false in DryRun).
	Wrote bool
}

// Get returns the merged value at the parsed segment path.
func Get(p talonpath.Paths, segments []string) (gjson.Result, error) {
	merged, err := MergedBytes(p)
	if err != nil {
		return gjson.Result{}, err
	}
	if len(segments) == 0 {
		return gjson.ParseBytes(merged), nil
	}
	return gjson.GetBytes(merged, ToSjsonPath(segments)), nil
}

// Set applies a typed value at the parsed segment path and writes the native
// TOML config.
func Set(p talonpath.Paths, segments []string, value any, opts SetOpts) (SetResult, error) {
	if len(segments) == 0 {
		return SetResult{}, fmt.Errorf("path is empty")
	}
	merged, err := MergedBytes(p)
	if err != nil {
		return SetResult{}, err
	}
	if opts.Mode == SetReplaceSafe {
		if err := assertNonDestructive(merged, segments, value); err != nil {
			return SetResult{}, err
		}
	}
	if err := assertNoPlaintextSecretWrite(segments, value); err != nil {
		return SetResult{}, err
	}
	updated, err := applySet(merged, segments, value, opts.Mode)
	if err != nil {
		return SetResult{}, err
	}
	pruned, updated, err := pruneInactiveGatewayAuth(updated, segments)
	if err != nil {
		return SetResult{}, err
	}
	res := SetResult{
		Path:        SegPath(segments),
		PrunedPaths: pruned,
	}
	if opts.DryRun {
		return res, nil
	}
	updated = canonicalPretty(updated)
	if bytes.Equal(canonicalPretty(merged), updated) {
		return res, nil
	}
	if err := writeNativeFromRuntimeJSON(p.Talon, merged, updated, []string{SegPath(segments)}); err != nil {
		return res, err
	}
	res.Wrote = true
	return res, nil
}

// Unset removes a key/element from the native config.
func Unset(p talonpath.Paths, segments []string) error {
	if len(segments) == 0 {
		return fmt.Errorf("path is empty")
	}
	merged, err := MergedBytes(p)
	if err != nil {
		return err
	}
	sjPath := ToSjsonPath(segments)
	if !gjson.GetBytes(merged, sjPath).Exists() {
		return fmt.Errorf("path not found: %s", SegPath(segments))
	}
	updated, err := sjson.DeleteBytes(merged, sjPath)
	if err != nil {
		return fmt.Errorf("unset %s: %w", SegPath(segments), err)
	}
	updated = canonicalPretty(updated)
	if bytes.Equal(canonicalPretty(merged), updated) {
		return nil
	}
	return writeNativeFromRuntimeJSON(p.Talon, merged, updated, []string{SegPath(segments)})
}

// Validate parses the merged config and, on success, refreshes the talon
// overlay's "last-good" sidecar.
func Validate(p talonpath.Paths) error {
	merged, err := MergedBytes(p)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(merged, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	raw, err := os.ReadFile(p.Talon.Config)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", p.Talon.Config, err)
	}
	_ = writeFile(p.Talon.LastGoodPath(), raw, 0o600)
	return nil
}

func applySet(raw []byte, segments []string, value any, mode SetMode) ([]byte, error) {
	switch mode {
	case SetForceReplace, SetReplaceSafe:
		out, err := sjson.SetBytes(raw, ToSjsonPath(segments), value)
		if err != nil {
			return nil, fmt.Errorf("set %s: %w", SegPath(segments), err)
		}
		return out, nil
	case SetMerge:
		return mergeAtPath(raw, segments, value)
	default:
		return nil, fmt.Errorf("unknown SetMode: %v", mode)
	}
}

// --- secret-write guard ----------------------------------------------------

func assertNoPlaintextSecretWrite(segments []string, value any) error {
	if len(segments) == 0 {
		return nil
	}
	label := SegPath(segments)
	key := segments[len(segments)-1]
	return walkSecretWrite(label, key, value)
}

func walkSecretWrite(label, key string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			if err := walkSecretWrite(joinLabel(label, k), k, v[k]); err != nil {
				return err
			}
		}
	case map[string]string:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			if err := walkSecretWrite(joinLabel(label, k), k, v[k]); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := walkSecretWrite(label+"["+strconv.Itoa(i)+"]", key, item); err != nil {
				return err
			}
		}
	case []string:
		for i, item := range v {
			if err := walkSecretWrite(label+"["+strconv.Itoa(i)+"]", key, item); err != nil {
				return err
			}
		}
	case string:
		if strings.TrimSpace(v) == "" || !secrets.IsSensitiveKey(key) {
			return nil
		}
		if secrets.IsReference(v) {
			return nil
		}
		return fmt.Errorf("refusing to write plaintext secret to %s; store it in 1Password or the OS keychain and set an op:// or keychain:// reference", label)
	}
	return nil
}

func joinLabel(prefix, child string) string {
	if prefix == "" {
		return child
	}
	return prefix + "." + child
}

// --- protected-path guard ---------------------------------------------------

func assertNonDestructive(merged []byte, segments []string, value any) error {
	existing := gjson.GetBytes(merged, ToSjsonPath(segments))
	if !existing.Exists() {
		return nil
	}
	pathLabel := SegPath(segments)
	if isProtectedMapPath(segments) && existing.IsObject() {
		patchMap, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		removed := mapKeysMissingFrom(existing, patchMap)
		if len(removed) > 0 {
			return fmt.Errorf("refusing to replace %s; the merged view contains entries your write would shadow or drop: %s. Use --merge to layer your changes additively, or --replace to override the guard.", pathLabel, formatRemoved(removed))
		}
	}
	if isProtectedArrayByIDPath(segments) && existing.IsArray() {
		nextIDs, ok := arrayIDs(value)
		if !ok {
			return nil
		}
		var existingIDs []string
		existing.ForEach(func(_, item gjson.Result) bool {
			id := item.Get("id")
			if id.Type == gjson.String && id.Str != "" {
				existingIDs = append(existingIDs, id.Str)
			}
			return true
		})
		removed := stringsDiff(existingIDs, nextIDs)
		if len(removed) > 0 {
			return fmt.Errorf("refusing to replace %s; the merged view contains ids your write would shadow or drop: %s. Use --merge to merge by id, or --replace to override the guard.", pathLabel, formatRemoved(removed))
		}
	}
	return nil
}

func isProtectedMapPath(segments []string) bool {
	switch SegPath(segments) {
	case "agents.defaults.models",
		"models.providers",
		"plugins.entries",
		"auth.profiles":
		return true
	}
	if len(segments) == 3 && segments[0] == "models" && segments[1] == "providers" {
		return true
	}
	return false
}

func isProtectedArrayByIDPath(segments []string) bool {
	if SegPath(segments) == "agents.list" {
		return true
	}
	if len(segments) == 4 && segments[0] == "models" && segments[1] == "providers" && segments[3] == "models" {
		return true
	}
	return false
}

func mapKeysMissingFrom(existing gjson.Result, next map[string]any) []string {
	var removed []string
	existing.ForEach(func(k, _ gjson.Result) bool {
		key := k.String()
		if _, ok := next[key]; !ok {
			removed = append(removed, key)
		}
		return true
	})
	return removed
}

func arrayIDs(value any) ([]string, bool) {
	arr, ok := value.([]any)
	if !ok {
		return nil, false
	}
	ids := make([]string, 0, len(arr))
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, false
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func stringsDiff(a, b []string) []string {
	bset := make(map[string]struct{}, len(b))
	for _, s := range b {
		bset[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := bset[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

func formatRemoved(entries []string) string {
	const max = 6
	if len(entries) <= max {
		return strings.Join(entries, ", ")
	}
	return strings.Join(entries[:max], ", ") + fmt.Sprintf(", ... %d more", len(entries)-max)
}

// --- merge-at-path (used by SetMerge) ---------------------------------------

func mergeAtPath(raw []byte, segments []string, value any) ([]byte, error) {
	return mergeRecursive(raw, segments, value)
}

func mergeRecursive(raw []byte, prefix []string, patch any) ([]byte, error) {
	switch v := patch.(type) {
	case map[string]any:
		existing := gjson.GetBytes(raw, ToSjsonPath(prefix))
		if existing.Exists() && !existing.IsObject() {
			return nil, fmt.Errorf("cannot merge %s: existing value is not an object (use --replace)", SegPath(prefix))
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)
		var err error
		for _, k := range keys {
			next := append(append([]string{}, prefix...), k)
			raw, err = mergeRecursive(raw, next, v[k])
			if err != nil {
				return nil, err
			}
		}
		return raw, nil
	case []any:
		if isProtectedArrayByIDPath(prefix) {
			return mergeArrayByID(raw, prefix, v)
		}
		return sjson.SetBytes(raw, ToSjsonPath(prefix), v)
	default:
		return sjson.SetBytes(raw, ToSjsonPath(prefix), v)
	}
}

func mergeArrayByID(raw []byte, prefix []string, patch []any) ([]byte, error) {
	existing := gjson.GetBytes(raw, ToSjsonPath(prefix))
	var existingArr []any
	if existing.Exists() {
		if err := json.Unmarshal([]byte(existing.Raw), &existingArr); err != nil {
			return nil, fmt.Errorf("merge %s: existing value is not valid JSON: %w", SegPath(prefix), err)
		}
	}
	indexByID := make(map[string]int, len(existingArr))
	for i, entry := range existingArr {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			continue
		}
		indexByID[id] = i
	}
	merged := append([]any{}, existingArr...)
	for _, entry := range patch {
		m, ok := entry.(map[string]any)
		if !ok {
			merged = append(merged, entry)
			continue
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			merged = append(merged, entry)
			continue
		}
		if idx, present := indexByID[id]; present {
			existingEntry, _ := merged[idx].(map[string]any)
			if existingEntry == nil {
				existingEntry = map[string]any{}
			}
			maps.Copy(existingEntry, m)
			merged[idx] = existingEntry
			continue
		}
		indexByID[id] = len(merged)
		merged = append(merged, entry)
	}
	return sjson.SetBytes(raw, ToSjsonPath(prefix), merged)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// --- gateway.auth pruning ---------------------------------------------------

// pruneInactiveGatewayAuth prunes credentials that no longer apply after a
// gateway.auth.mode change.
func pruneInactiveGatewayAuth(overlay []byte, segments []string) (pruned []string, out []byte, err error) {
	if SegPath(segments) != "gateway.auth.mode" {
		return nil, overlay, nil
	}
	mode := gjson.GetBytes(overlay, "gateway.auth.mode").String()
	prune := func(key string) error {
		path := "gateway.auth." + key
		if gjson.GetBytes(overlay, path).Exists() {
			next, derr := sjson.DeleteBytes(overlay, path)
			if derr != nil {
				return fmt.Errorf("prune %s: %w", path, derr)
			}
			overlay = next
			pruned = append(pruned, path)
		}
		return nil
	}
	switch mode {
	case "token":
		if err = prune("password"); err != nil {
			return nil, overlay, err
		}
	case "password":
		if err = prune("token"); err != nil {
			return nil, overlay, err
		}
	case "trusted-proxy", "none", "":
		if err = prune("token"); err != nil {
			return nil, overlay, err
		}
		if err = prune("password"); err != nil {
			return nil, overlay, err
		}
	}
	return pruned, overlay, nil
}
