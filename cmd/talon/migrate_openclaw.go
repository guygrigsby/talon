// `talon migrate-from-openclaw` copies ~/.openclaw state that talon
// reads — agent config, workspace files, the openclaw.json overlay —
// into ~/.talon. Talon's path-resolution will then find everything
// under the talon layer; the openclaw layer can be removed
// (separately, by the user) once they've verified the migration.
//
// Scope, intentional:
//   include: agents/*/agent/, workspace*/ (sans gigantic carve-outs),
//            openclaw.json itself (if no talon overlay exists)
//   skip:    plugin-runtime-deps/ (Node modules, talon doesn't use),
//            agents/*/sessions/ (openclaw runtime state talon doesn't
//                                read), *.bak* files, *.tgz, *.deleted
//                                trajectory dumps, node_modules anywhere
//
// Safety:
//   - destination files that already exist are left alone (no
//     clobber) so a re-run is idempotent and a partial migration
//     stays useful
//   - --dry-run prints what would be copied without touching the FS
//   - summary at the end: copied / skipped / would-clobber counts

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/spf13/cobra"
)

type migrateStats struct {
	copied        int
	skipped       int
	wouldClobber  int
	bytes         int64
}

func migrateOpenclawCmd() *cobra.Command {
	var (
		dryRun        bool
		force         bool
		noMergeConfig bool
	)
	c := &cobra.Command{
		Use:   "migrate-from-openclaw",
		Short: "Copy talon-readable state out of ~/.openclaw into ~/.talon",
		Long: `Walks the openclaw state directory and copies the files talon reads
(agents/*/agent/, workspace*/, openclaw.json) into the talon state directory.
Skips runtime state talon doesn't use (sessions, plugin-runtime-deps,
node_modules, *.bak*, *.tgz, *.deleted).

By default destination files that already exist are left untouched. Pass
--force to overwrite. --dry-run prints what would happen without touching
anything.

After the migration, verify the chat surface works (talon dashboard, send
a message). When you're satisfied, ~/.openclaw can be removed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := resolvePaths()
			src := paths.Openclaw.Dir
			dst := paths.Talon.Dir
			if src == "" {
				return errors.New("openclaw state dir not resolved (OPENCLAW_STATE_DIR or ~/.openclaw)")
			}
			if dst == "" {
				return errors.New("talon state dir not resolved (TALON_STATE_DIR or ~/.talon)")
			}
			if _, err := os.Stat(src); err != nil {
				return fmt.Errorf("openclaw state dir %q not readable: %w", src, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Migrating %s -> %s (dry-run=%v force=%v)\n", src, dst, dryRun, force)

			stats := migrateStats{}
			if err := migrateWalk(src, dst, &stats, dryRun, force, cmd.OutOrStdout()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"\nFile tree: copied=%d skipped=%d existed-no-clobber=%d bytes=%s\n",
				stats.copied, stats.skipped, stats.wouldClobber, humanBytes(stats.bytes),
			)

			// Config merge: agents.list / agents.defaults / gateway /
			// providers / channels live in openclaw.json itself, not
			// under the file tree. Talon's MergedBytes already
			// implements the layer merge (talon wins on conflict);
			// write that snapshot back into ~/.talon/openclaw.json so
			// when SkipOpenclaw goes true the merged view persists.
			if !noMergeConfig {
				wroteConfig, err := mergeConfigInto(paths, dryRun, force, cmd.OutOrStdout())
				if err != nil {
					return fmt.Errorf("merge openclaw.json: %w", err)
				}
				if dryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "Config merge: would-write=%v\n", wroteConfig)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "Config merge: wrote=%v target=%s\n", wroteConfig, paths.Talon.Config)
				}
			}

			if !dryRun && stats.copied > 0 {
				fmt.Fprintln(cmd.OutOrStdout(),
					"\nNext: restart any running talon-gateway, run `talon dashboard`,",
					"\nsend a test message. Once happy, ~/.openclaw can be removed.")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without touching the filesystem")
	c.Flags().BoolVar(&force, "force", false, "overwrite destination files that already exist (default: leave them alone)")
	c.Flags().BoolVar(&noMergeConfig, "no-merge-config", false, "skip merging openclaw.json into the talon overlay")
	return c
}

// mergeConfigInto reads both layers' openclaw.json (via the
// existing merge helper, talon overlay wins on conflicts) and
// writes the resulting bytes to the talon overlay path. The
// resolvePaths() default has SkipOpenclaw=true so callers must
// pass paths with SkipOpenclaw cleared — we override here.
func mergeConfigInto(paths openclaw.Paths, dryRun, force bool, out io.Writer) (bool, error) {
	probe := paths
	probe.SkipOpenclaw = false
	merged, err := config.MergedBytes(probe)
	if err != nil {
		return false, err
	}
	if len(merged) == 0 || string(merged) == "{}" {
		return false, nil
	}
	dst := paths.Talon.Config
	if dst == "" {
		return false, errors.New("talon config path empty")
	}
	// MergedBytes already merges talon-over-openclaw with id-keyed
	// array semantics (agents.list, models.aliases, etc.) so writing
	// its output to the talon overlay folds openclaw in without
	// dropping talon-side overrides. We pretty-print the result so
	// it stays human-editable. Backup the pre-existing overlay if
	// any, so a botched run is recoverable.
	//
	// Then rewrite workspace paths: agents.defaults.workspace and
	// agents.list[].workspace fields that point at the openclaw
	// state dir get retargeted at the matching talon location.
	// Otherwise the agent reads its system prompt + memory from
	// ~/.openclaw/ even though the migration just copied the
	// content to ~/.talon/.
	merged = rewriteWorkspacePaths(merged, paths.Openclaw.Dir, paths.Talon.Dir)
	pretty, err := jsonPretty(merged)
	if err != nil {
		return false, err
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if existing, err := os.ReadFile(dst); err == nil && len(existing) > 0 {
		backup := dst + ".bak." + nowStamp()
		if err := atomicWriteFile(backup, existing, 0o644); err != nil {
			fmt.Fprintf(out, "warning: could not write backup %s: %v\n", backup, err)
		} else {
			fmt.Fprintf(out, "Backed up existing talon overlay to %s\n", backup)
		}
	}
	return true, atomicWriteFile(dst, pretty, 0o644)
}

// shouldSkip applies the carve-outs that keep the migration from
// dragging in non-talon state. relPath is the source path relative
// to the openclaw root. Returns (skip, reason) — reason is logged
// in dry-run so the user can audit what's being left behind.
func shouldSkip(relPath string, info fs.FileInfo) (bool, string) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return false, ""
	}
	top := parts[0]

	// Whole top-level dirs talon never reads. Listed explicitly
	// (rather than an allow-list) so a future openclaw addition
	// gets surfaced in the dry-run rather than silently dropped.
	switch top {
	case "plugin-runtime-deps", "node_modules", "logs", "bin",
		"completions", "canvas", "tasks", "media", "msteams-pending-uploads.json",
		"flows", "locks", "devices", "exec-approvals.json":
		return true, "openclaw-runtime"
	}

	// agents/<id>/sessions/ — openclaw's chat history files,
	// talon keeps its own in-memory store; copying these in just
	// inflates the talon dir for no functional gain.
	if len(parts) >= 3 && parts[0] == "agents" && parts[2] == "sessions" {
		return true, "openclaw sessions"
	}

	// Anywhere in the tree: dirs we never want to drag into
	// ~/.talon. The user's workspace sometimes has a Python build
	// tree (Python-3.9.7/) and .git histories; talon doesn't read
	// either and they bloat the migration to ~88M without value.
	for _, p := range parts {
		switch p {
		case "node_modules", ".git", "__pycache__":
			return true, p
		}
		if strings.HasPrefix(p, "Python-") {
			return true, "python source tree"
		}
	}

	if info != nil && !info.IsDir() {
		name := info.Name()
		switch {
		case strings.Contains(name, ".bak"):
			return true, "backup"
		case strings.HasSuffix(name, ".tgz"):
			return true, "tarball"
		case strings.HasSuffix(name, ".deleted"):
			return true, "tombstone"
		case strings.Contains(name, ".trajectory.jsonl.deleted."):
			return true, "trajectory tombstone"
		}
	}
	return false, ""
}

func migrateWalk(src, dst string, stats *migrateStats, dryRun, force bool, out io.Writer) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if skip, reason := shouldSkip(rel, info); skip {
			stats.skipped++
			if dryRun {
				fmt.Fprintf(out, "SKIP  %s  [%s]\n", rel, reason)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		if d.IsDir() {
			if !dryRun {
				if err := os.MkdirAll(targetPath, 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", targetPath, err)
				}
			}
			return nil
		}
		// Regular file: copy unless already present.
		if _, err := os.Stat(targetPath); err == nil && !force {
			stats.wouldClobber++
			if dryRun {
				fmt.Fprintf(out, "KEEP  %s  [exists; pass --force to overwrite]\n", rel)
			}
			return nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", targetPath, err)
		}
		if dryRun {
			fmt.Fprintf(out, "COPY  %s  [%s]\n", rel, humanBytes(info.Size()))
			stats.copied++
			stats.bytes += info.Size()
			return nil
		}
		if err := copyFile(path, targetPath, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", path, targetPath, err)
		}
		stats.copied++
		stats.bytes += info.Size()
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// rewriteWorkspacePaths walks the agent-shaped config keys and
// retargets any workspace value that lives under the openclaw
// state dir onto the matching talon location. Specifically:
//
//	agents.defaults.workspace
//	agents.list[].workspace
//
// Other paths (model files, plugin binaries, ...) are left alone —
// they're typically absolute paths that don't shadow openclaw or
// they live somewhere stable like /usr/local/bin. No-op when src
// or dst is empty, or when no replaceable path is present.
func rewriteWorkspacePaths(raw []byte, srcDir, dstDir string) []byte {
	if srcDir == "" || dstDir == "" || srcDir == dstDir || len(raw) == 0 {
		return raw
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	rewrite := func(s string) (string, bool) {
		// Only rewrite when the value is rooted at srcDir/. The
		// trailing slash check avoids matching a srcDir-prefixed
		// directory that happens to share a name prefix.
		if strings.HasPrefix(s, srcDir+"/") {
			return dstDir + strings.TrimPrefix(s, srcDir), true
		}
		if s == srcDir {
			return dstDir, true
		}
		return s, false
	}
	agents, _ := doc["agents"].(map[string]any)
	if agents == nil {
		return raw
	}
	changed := false
	if defaults, ok := agents["defaults"].(map[string]any); ok {
		if v, ok := defaults["workspace"].(string); ok {
			if next, hit := rewrite(v); hit {
				defaults["workspace"] = next
				changed = true
			}
		}
	}
	if list, ok := agents["list"].([]any); ok {
		for _, entry := range list {
			obj, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := obj["workspace"].(string); ok {
				if next, hit := rewrite(v); hit {
					obj["workspace"] = next
					changed = true
				}
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// jsonPretty re-marshals JSON bytes with 2-space indent. The merged
// output from config.MergedBytes is compact; humans read the overlay
// after a migration so the indented form is the right default.
func jsonPretty(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// nowStamp returns an RFC3339-like timestamp without colons so it's
// safe in filenames on every common filesystem.
func nowStamp() string {
	t := time.Now().UTC()
	return fmt.Sprintf("%04d%02d%02dT%02d%02d%02dZ",
		t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// overlayMissing fills in keys from src that aren't already present
// in existing. Returns the new bytes + a changed flag. Recurses into
// nested objects so a partial talon overlay (e.g. {gateway:{port:18789}})
// gains the rest of gateway's keys from openclaw without losing the
// port override. Arrays are NOT deep-merged: if existing has the
// array at all (even empty), it wins — matches the rest of talon's
// "talon-wins" merge semantics.
func overlayMissing(existing, src []byte) ([]byte, bool, error) {
	var ex, srcMap map[string]any
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &ex); err != nil {
			return nil, false, fmt.Errorf("parse talon overlay: %w", err)
		}
	}
	if ex == nil {
		ex = map[string]any{}
	}
	if len(src) > 0 {
		if err := json.Unmarshal(src, &srcMap); err != nil {
			return nil, false, fmt.Errorf("parse merged source: %w", err)
		}
	}
	changed := overlayMissingObj(ex, srcMap)
	if !changed {
		return existing, false, nil
	}
	out, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func overlayMissingObj(dst, src map[string]any) bool {
	changed := false
	for k, v := range src {
		cur, present := dst[k]
		if !present {
			dst[k] = v
			changed = true
			continue
		}
		// Recurse only when both sides are JSON objects. Anything
		// else (string, number, bool, array) — existing wins.
		curObj, curOK := cur.(map[string]any)
		srcObj, srcOK := v.(map[string]any)
		if curOK && srcOK {
			if overlayMissingObj(curObj, srcObj) {
				changed = true
			}
		}
	}
	return changed
}

// atomicWriteFile writes data to path via a temp file in the same
// directory, then renames into place. Avoids leaving a half-written
// openclaw.json if the migration is interrupted.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func humanBytes(n int64) string {
	const (
		k = 1 << 10
		m = 1 << 20
		g = 1 << 30
	)
	switch {
	case n >= g:
		return fmt.Sprintf("%.1fG", float64(n)/g)
	case n >= m:
		return fmt.Sprintf("%.1fM", float64(n)/m)
	case n >= k:
		return fmt.Sprintf("%.1fK", float64(n)/k)
	}
	return fmt.Sprintf("%dB", n)
}
