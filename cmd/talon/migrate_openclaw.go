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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
		dryRun bool
		force  bool
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
				"\nDone. copied=%d skipped=%d existed-no-clobber=%d bytes=%s\n",
				stats.copied, stats.skipped, stats.wouldClobber, humanBytes(stats.bytes),
			)
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
	return c
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
