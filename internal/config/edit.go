package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"
)

// canonicalPrettyOptions defines talon's on-disk overlay format. Output matches
// openclaw's writer (Node `JSON.stringify(v, null, 2) + "\n"`) byte-for-byte:
// 2-space indent, spaces after colons, no key sort, one element per line, and
// a trailing newline. This is verified in TestSetWriteFormatMatchesOpenclaw.
//
// Keep in sync with both writers' tests if the format ever changes.
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
	// StaleOpenclawPaths lists openclaw-layer paths that the prune logic
	// could not remove because openclaw is read-only. The merged view will
	// still report those values until the user clears them on the openclaw
	// side or the talon overlay tombstones them.
	StaleOpenclawPaths []string
	// Wrote is true when the talon overlay was written (false in DryRun).
	Wrote bool
}

// Get returns the merged value at the parsed segment path.
func Get(p openclaw.Paths, segments []string) (gjson.Result, error) {
	merged, err := MergedBytes(p)
	if err != nil {
		return gjson.Result{}, err
	}
	if len(segments) == 0 {
		return gjson.ParseBytes(merged), nil
	}
	return gjson.GetBytes(merged, ToSjsonPath(segments)), nil
}

// Set applies a typed value at the parsed segment path. Writes target the
// talon overlay (p.Talon.Config); the openclaw layer is read-only.
//
// The protected-path guard checks the merged view, not just the overlay, so a
// destructive replacement is refused even when the entries being dropped live
// only in the openclaw layer.
func Set(p openclaw.Paths, segments []string, value any, opts SetOpts) (SetResult, error) {
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
	overlay, err := readOverlayOrEmpty(p.Talon.Config)
	if err != nil {
		return SetResult{}, err
	}
	updated, err := applySet(overlay, segments, value, opts.Mode)
	if err != nil {
		return SetResult{}, err
	}
	pruned, stale, updated, err := pruneInactiveGatewayAuth(updated, p, segments)
	if err != nil {
		return SetResult{}, err
	}
	res := SetResult{
		Path:               SegPath(segments),
		PrunedPaths:        pruned,
		StaleOpenclawPaths: stale,
	}
	if opts.DryRun {
		return res, nil
	}
	updated = canonicalPretty(updated)
	// Idempotent short-circuit: if the post-pretty bytes already match what's
	// on disk, skip the write. Avoids rotating .bak files and appending a no-op
	// audit record when the user re-issues an unchanged set (talon-7vk).
	onDisk, _ := os.ReadFile(p.Talon.Config)
	if onDisk != nil && bytes.Equal(onDisk, updated) {
		return res, nil
	}
	if err := writeOverlay(p.Talon, overlay, updated, []string{SegPath(segments)}); err != nil {
		return res, err
	}
	res.Wrote = true
	return res, nil
}

// Unset removes a key/element from the talon overlay.
//
// Note: Unset only touches the talon overlay. If the same path is set in the
// openclaw layer, the merged view will continue to report the openclaw value
// after Unset returns. ErrNotInOverlay signals this case.
func Unset(p openclaw.Paths, segments []string) error {
	if len(segments) == 0 {
		return fmt.Errorf("path is empty")
	}
	overlay, err := readOverlayOrEmpty(p.Talon.Config)
	if err != nil {
		return err
	}
	sjPath := ToSjsonPath(segments)
	if !gjson.GetBytes(overlay, sjPath).Exists() {
		// Nothing to remove from talon's overlay. If openclaw has the path,
		// surface that to the caller so they can decide what to do.
		merged, mErr := MergedBytes(p)
		if mErr == nil && gjson.GetBytes(merged, sjPath).Exists() {
			return fmt.Errorf("%w: %s exists only in the openclaw layer (read-only)", ErrNotInOverlay, SegPath(segments))
		}
		return fmt.Errorf("path not found: %s", SegPath(segments))
	}
	updated, err := sjson.DeleteBytes(overlay, sjPath)
	if err != nil {
		return fmt.Errorf("unset %s: %w", SegPath(segments), err)
	}
	updated = canonicalPretty(updated)
	onDisk, _ := os.ReadFile(p.Talon.Config)
	if onDisk != nil && bytes.Equal(onDisk, updated) {
		return nil
	}
	return writeOverlay(p.Talon, overlay, updated, []string{SegPath(segments)})
}

// Validate parses the merged config and, on success, refreshes the talon
// overlay's "last-good" sidecar.
func Validate(p openclaw.Paths) error {
	merged, err := MergedBytes(p)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(merged, &v); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	overlay, err := readOverlayOrEmpty(p.Talon.Config)
	if err != nil {
		return err
	}
	// Only refresh last-good if the talon overlay exists on disk.
	if _, statErr := os.Stat(p.Talon.Config); statErr == nil {
		_ = writeFile(p.Talon.LastGoodPath(), overlay, 0o600)
	}
	return nil
}

// ErrNotInOverlay is returned by Unset when the requested path exists in the
// openclaw layer but not in the talon overlay (so Unset cannot remove it).
var ErrNotInOverlay = errors.New("config path exists only in the openclaw layer")

func readOverlayOrEmpty(path string) ([]byte, error) {
	if path == "" {
		return []byte("{}"), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte("{}"), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !gjson.ValidBytes(b) {
		return nil, fmt.Errorf("invalid JSON in %s", path)
	}
	return b, nil
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
			return fmt.Errorf("refusing to replace %s; the merged view contains entries your write would shadow or drop: %s. Use --merge to layer your changes additively, or --replace to override the guard. (Note: --replace cannot delete entries that come from the openclaw layer; tombstones are not yet implemented — see talon-9ic.)", pathLabel, formatRemoved(removed))
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

// pruneInactiveGatewayAuth prunes credentials from the talon overlay that no
// longer apply after a gateway.auth.mode change. If the openclaw layer still
// has stale credentials, those paths are returned in stalePaths so the caller
// can warn the user (we can't delete from openclaw).
func pruneInactiveGatewayAuth(overlay []byte, p openclaw.Paths, segments []string) (pruned, stalePaths []string, out []byte, err error) {
	if SegPath(segments) != "gateway.auth.mode" {
		return nil, nil, overlay, nil
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
		// Check whether the openclaw layer still has the now-inactive key.
		if !p.SkipOpenclaw {
			openclawRaw, oerr := os.ReadFile(p.Openclaw.Config)
			if oerr == nil && gjson.GetBytes(openclawRaw, path).Exists() {
				stalePaths = append(stalePaths, path)
			}
		}
		return nil
	}
	switch mode {
	case "token":
		if err = prune("password"); err != nil {
			return nil, nil, overlay, err
		}
	case "password":
		if err = prune("token"); err != nil {
			return nil, nil, overlay, err
		}
	case "trusted-proxy", "none", "":
		if err = prune("token"); err != nil {
			return nil, nil, overlay, err
		}
		if err = prune("password"); err != nil {
			return nil, nil, overlay, err
		}
	}
	return pruned, stalePaths, overlay, nil
}
