// Package main — `talon secrets migrate` and
// `talon secrets keychain-bootstrap`.
//
// migrate is the bulk sweep: walks the merged config and per-agent
// JSON files, identifies every literal sensitive value, and moves
// each into the macOS keychain as a keychain://talon.<dotted-path>
// reference. Dry-run by default; --apply commits the writes.
//
// The job here is "get plaintext off disk" — no path arg, no
// vault/item flags, no per-secret interaction. Users with existing
// op:// or keychain:// refs are left alone (already migrated);
// only Kind=="literal" rows from `secrets audit` are touched.
//
// keychain-bootstrap stays in this file because it's the sibling
// "store this one credential" command: it puts the 1Password
// service-account token in the macOS keychain so talon-op-plugin
// can resolve op:// references non-interactively. Lives next to
// migrate because they share the macOS-only + `security` CLI
// preconditions.

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// keychainServiceForOPToken matches the constant in
// apps/talon-op-plugin/main.go. Kept duplicated rather than
// imported because the plugin and the CLI live in different
// modules' main packages.
const keychainServiceForOPToken = "talon.opAccessToken"

func secretsMigrateCmd() *cobra.Command {
	var (
		apply   bool
		account string
		filter  string
	)
	c := &cobra.Command{
		Use:   "migrate",
		Short: "Move every plaintext secret on disk into the macOS keychain",
		Long: `Walks the merged config and per-agent auth files, identifies
every literal sensitive value, and migrates each into the macOS
keychain as keychain://talon.<dotted-path>.

DEFAULT IS DRY-RUN — pass --apply to actually write to the keychain
and rewrite config. The dry-run plan prints one row per literal so
you can review naming and scope before committing.

Already-migrated refs (op://, keychain://) are skipped — this command
only converts literal plaintext.

Examples:
  talon secrets migrate                  # show plan
  talon secrets migrate --apply          # do it
  talon secrets migrate --filter gateway # only paths matching "gateway"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("secrets migrate is macOS-only (current: %s) — the keychain:// resolver shells out to `security`", runtime.GOOS)
			}
			if apply {
				if _, err := exec.LookPath("security"); err != nil {
					return fmt.Errorf("`security` CLI not found (should be on every macOS)")
				}
			}
			if account == "" {
				if u := os.Getenv("USER"); u != "" {
					account = u
				} else {
					account = "talon"
				}
			}

			paths := resolvePaths()
			plan, err := buildMigratePlan(paths, filter)
			if err != nil {
				return err
			}
			if len(plan) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No plaintext secrets found. Nothing to migrate.")
				return nil
			}

			printMigratePlan(cmd, plan, account, apply)
			if !apply {
				fmt.Fprintln(cmd.OutOrStdout(), "")
				fmt.Fprintln(cmd.OutOrStdout(), "Dry-run only. Re-run with --apply to migrate.")
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			return applyMigratePlan(ctx, cmd, paths, plan, account)
		},
	}
	c.Flags().BoolVar(&apply, "apply", false, "actually write to keychain and rewrite config (default: dry-run)")
	c.Flags().StringVar(&account, "account", "", "keychain account name (default: $USER)")
	c.Flags().StringVar(&filter, "filter", "", "only migrate paths containing this substring")
	return c
}

// migrateItem is one row in the plan: a discovered literal secret
// and where it will end up.
type migrateItem struct {
	// Path is the source location — dotted merged-config path
	// ("gateway.auth.token") or file:// reference
	// ("file://agents/main/agent/auth-profiles.json:profiles.openai:default.key").
	Path string
	// Value is the plaintext to write into the keychain. Kept in
	// memory only — never logged, never echoed to stdout, never
	// written to disk outside the keychain.
	Value string
	// Service is the keychain service name we'll create. Format:
	// talon.<dotted-path> with file:// rel-paths flattened in.
	Service string
}

// buildMigratePlan walks both audit sources and returns one item
// per literal secret. Empty values and existing refs are skipped —
// migrate is the "convert literals" command; refs are already
// migrated and empties have nothing to move.
func buildMigratePlan(paths openclaw.Paths, filter string) ([]migrateItem, error) {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return nil, fmt.Errorf("read merged config: %w", err)
	}
	configEntries := auditSecrets(merged)
	fileEntries := auditFileSecrets(paths.Openclaw.Dir)

	items := []migrateItem{}
	for _, e := range configEntries {
		if e.Kind != "literal" {
			continue
		}
		if filter != "" && !strings.Contains(e.Path, filter) {
			continue
		}
		value, err := readConfigLiteral(merged, e.Path)
		if err != nil || value == "" {
			continue
		}
		items = append(items, migrateItem{
			Path:    e.Path,
			Value:   value,
			Service: KeychainServiceForPath(e.Path),
		})
	}
	for _, e := range fileEntries {
		if e.Kind != "literal" {
			continue
		}
		if filter != "" && !strings.Contains(e.Path, filter) {
			continue
		}
		value, err := readFileLiteral(paths, e.Path)
		if err != nil || value == "" {
			continue
		}
		items = append(items, migrateItem{
			Path:    e.Path,
			Value:   value,
			Service: KeychainServiceForPath(e.Path),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, nil
}

func readConfigLiteral(merged []byte, path string) (string, error) {
	segments, err := config.ParsePath(path)
	if err != nil {
		return "", err
	}
	cur := gjson.GetBytes(merged, strings.Join(segments, "."))
	if !cur.Exists() || cur.Type != gjson.String {
		return "", fmt.Errorf("%s: not a string leaf", path)
	}
	return cur.Str, nil
}

func readFileLiteral(paths openclaw.Paths, fullPath string) (string, error) {
	rel, key, err := parseFileRef(fullPath)
	if err != nil {
		return "", err
	}
	// Talon overlay wins, fall back to openclaw layer — same
	// precedence as the runtime reader.
	overlayPath := filepath.Join(paths.Talon.Dir, rel)
	openclawPath := filepath.Join(paths.Openclaw.Dir, rel)
	src := overlayPath
	if _, err := os.Stat(src); err != nil {
		src = openclawPath
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	cur := gjson.GetBytes(raw, key)
	if !cur.Exists() || cur.Type != gjson.String {
		return "", fmt.Errorf("%s: not a string leaf", key)
	}
	return cur.Str, nil
}

func printMigratePlan(cmd *cobra.Command, items []migrateItem, account string, apply bool) {
	mode := "PLAN (dry-run)"
	if apply {
		mode = "APPLYING"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s — %d secret(s), keychain account=%s\n\n", mode, len(items), account)
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tKEYCHAIN SERVICE\tNEW REFERENCE")
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%s\tkeychain://%s\n", it.Path, it.Service, it.Service)
	}
	tw.Flush()
}

// applyMigratePlan executes each migration. Per-item failures are
// reported but don't abort — partial progress is fine because each
// keychain write is independently verified before its config gets
// rewritten.
func applyMigratePlan(ctx context.Context, cmd *cobra.Command, paths openclaw.Paths, items []migrateItem, account string) error {
	var failures int
	for _, it := range items {
		ref := "keychain://" + it.Service
		if err := writeKeychainEntry(ctx, it.Service, account, it.Value); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s — keychain write failed: %v\n", it.Path, err)
			failures++
			continue
		}
		// Verify via the canonical resolver (inlined keychain
		// reader) so the same code path the gateway uses at
		// runtime validates the entry.
		roundTrip, err := secrets.NewResolver().Resolve(ctx, ref)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s — verify failed: %v (keychain entry exists; config NOT modified)\n", it.Path, err)
			failures++
			continue
		}
		if roundTrip != it.Value {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s — verify mismatch (keychain returned different bytes; config NOT modified)\n", it.Path)
			failures++
			continue
		}
		if err := writeRefForPath(paths, it.Path, ref); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s — config rewrite failed: %v (keychain entry at %s is good; re-run to finish)\n", it.Path, err, ref)
			failures++
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s → %s\n", it.Path, ref)
	}
	if failures > 0 {
		return fmt.Errorf("secrets migrate: %d of %d item(s) failed", failures, len(items))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "All literals migrated. The gateway picks up new refs as it re-reads them — usually transparently per-request. Restart the gateway only if you've rotated the underlying values (rare; migration writes the same bytes you had on disk).")
	return nil
}

// writeKeychainEntry creates or updates a generic-password entry in
// the macOS keychain. -U makes the upsert atomic: if the service+
// account combo already exists, it's overwritten rather than
// failing as a duplicate.
func writeKeychainEntry(ctx context.Context, service, account, value string) error {
	args := []string{
		"add-generic-password",
		"-U",
		"-s", service,
		"-a", account,
		"-w", value,
	}
	runCmd := exec.CommandContext(ctx, "security", args...)
	runCmd.Stderr = os.Stderr
	if err := runCmd.Run(); err != nil {
		return fmt.Errorf("security add-generic-password: %w", err)
	}
	return nil
}

// writeRefForPath dispatches on path shape: dotted merged-config
// path → config.Set on the talon overlay; file:// path → JSON
// rewrite of the file in the talon overlay.
func writeRefForPath(paths openclaw.Paths, path, ref string) error {
	if strings.HasPrefix(path, "file://") {
		return writeFileRef(paths, path, ref)
	}
	segments, err := config.ParsePath(path)
	if err != nil {
		return err
	}
	if _, err := config.Set(paths, segments, ref, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
		return err
	}
	return nil
}

// writeFileRef rewrites a file:// secret in the talon overlay,
// leaving the openclaw layer file alone. Same overlay-only write
// policy the rest of talon uses for layered state.
func writeFileRef(paths openclaw.Paths, fullPath, ref string) error {
	rel, key, err := parseFileRef(fullPath)
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(paths.Talon.Dir, rel)
	openclawPath := filepath.Join(paths.Openclaw.Dir, rel)
	src := overlayPath
	if _, err := os.Stat(src); err != nil {
		src = openclawPath
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	updated, err := sjsonSetString(raw, key, ref)
	if err != nil {
		return fmt.Errorf("rewrite JSON: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(overlayPath), 0o700); err != nil {
		return fmt.Errorf("mkdir overlay: %w", err)
	}
	if err := os.WriteFile(overlayPath, updated, 0o600); err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}
	return nil
}

// parseFileRef splits "file://<rel>:<key>" into (rel, key).
// Returns an error when the shape doesn't match — `:` is required
// to delimit file from key, and rel must be non-empty.
func parseFileRef(fullPath string) (rel, key string, err error) {
	body := strings.TrimPrefix(fullPath, "file://")
	idx := strings.Index(body, ":")
	if idx <= 0 || idx == len(body)-1 {
		return "", "", fmt.Errorf("file:// path must be file://<rel>:<key>, got %q", fullPath)
	}
	return body[:idx], body[idx+1:], nil
}

// sjsonSetString writes a string value at the given dotted key
// in raw JSON. Wraps github.com/tidwall/sjson; isolated as a
// function so the import lives in one place and tests can mock
// if needed.
func sjsonSetString(raw []byte, key, value string) ([]byte, error) {
	return sjson.SetBytes(raw, key, value)
}

// KeychainServiceForPath derives the keychain service name from a
// secret's source path. Format: talon.<dotted-path> with any non-
// dot separator (/, :, ", [, ], etc.) flattened into a dot, and
// runs of dots collapsed to a single dot.
//
//	gateway.auth.token
//	  → talon.gateway.auth.token
//	channels.telegram.botToken
//	  → talon.channels.telegram.botToken
//	file://agents/main/agent/auth-profiles.json:profiles.openai:default.key
//	  → talon.agents.main.agent.auth-profiles.json.profiles.openai.default.key
//
// Exported because the smoke target's test filter matches on this
// name and dry-run output exercises it without writing to keychain.
func KeychainServiceForPath(path string) string {
	cleaned := strings.NewReplacer(
		"file://", "",
		"/", ".",
		":", ".",
		`"`, "",
		"[", ".",
		"]", "",
		" ", ".",
	).Replace(path)
	for strings.Contains(cleaned, "..") {
		cleaned = strings.ReplaceAll(cleaned, "..", ".")
	}
	cleaned = strings.Trim(cleaned, ".")
	return "talon." + cleaned
}

// --- keychain-bootstrap ---------------------------------------------------

func secretsKeychainBootstrapCmd() *cobra.Command {
	var (
		token   string
		account string
	)
	c := &cobra.Command{
		Use:   "keychain-bootstrap",
		Short: "Store the 1Password service-account token in the macOS keychain (one-time setup)",
		Long: `Stores a 1Password service-account token in the macOS login
keychain at service "` + keychainServiceForOPToken + `". After this,
talon-op-plugin can resolve op:// references from a fresh shell
without OP_SERVICE_ACCOUNT_TOKEN being exported.

Get a service-account token at:
  https://my.1password.com → Developer Tools → Service accounts

Examples:
  talon secrets keychain-bootstrap                  # prompts for token
  talon secrets keychain-bootstrap --token ops_...  # non-interactive`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("keychain-bootstrap is macOS-only (current: %s)", runtime.GOOS)
			}
			if _, err := exec.LookPath("security"); err != nil {
				return fmt.Errorf("`security` CLI not found (should be on every macOS)")
			}
			if account == "" {
				if u := os.Getenv("USER"); u != "" {
					account = u
				} else {
					account = "talon"
				}
			}
			if token == "" {
				cmd.Print("Paste 1Password service-account token: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("read token: %w", err)
				}
				token = strings.TrimSpace(line)
			}
			if token == "" {
				return fmt.Errorf("token is required")
			}
			if !strings.HasPrefix(token, "ops_") {
				cmd.Println("warn: token doesn't start with 'ops_' — make sure this is a service-account token, not an account password")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := writeKeychainEntry(ctx, keychainServiceForOPToken, account, token); err != nil {
				return err
			}

			// Round-trip read so the user knows it's actually
			// retrievable (the writes succeed even when the
			// keychain is locked, but reads then prompt — better
			// to surface that here).
			readCmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainServiceForOPToken, "-a", account, "-w")
			readBack, err := readCmd.Output()
			if err != nil {
				return fmt.Errorf("verify keychain entry: %w (entry was written but the read failed — check Keychain Access)", err)
			}
			if strings.TrimRight(string(readBack), "\r\n") != token {
				return fmt.Errorf("verify mismatch: keychain returned a different value than written")
			}
			cmd.Printf("✓ Stored 1Password token in keychain at service=%s account=%s\n", keychainServiceForOPToken, account)
			cmd.Println("  talon-op-plugin will now auto-load this when OP_SERVICE_ACCOUNT_TOKEN isn't set.")
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "1Password service-account token (omit to prompt)")
	c.Flags().StringVar(&account, "account", "", "keychain account name (default: $USER)")
	return c
}
