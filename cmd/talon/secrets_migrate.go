// Package main — `talon secrets migrate <path>` and
// `talon secrets keychain-bootstrap`.
//
// migrate is the workhorse: takes a dotted config path, reads the
// literal value, creates (or updates) a 1Password item with that
// value, replaces the literal in config with the corresponding
// op:// reference, and round-trips a read through the resolver to
// confirm the new reference works. Aborts (and leaves config
// unchanged) on any step that fails — never strands the user with
// a non-functional config.
//
// keychain-bootstrap is the one-time setup: stores a 1Password
// service-account token in the macOS keychain at the well-known
// service name talon-op-plugin pulls from. Lets the op CLI auth
// non-interactively from a fresh shell.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

// keychainServiceForOPToken matches the constant in
// apps/talon-op-plugin/main.go. Kept duplicated rather than
// imported because the plugin and the CLI live in different
// modules' main packages.
const keychainServiceForOPToken = "talon.opAccessToken"

func secretsMigrateCmd() *cobra.Command {
	var (
		vault       string
		field       string
		itemName    string
		dryRun      bool
		yes         bool
	)
	c := &cobra.Command{
		Use:   "migrate <path>",
		Short: "Move a plaintext secret at <path> into 1Password and replace it with an op:// reference",
		Long: `Migrates one config secret from disk into 1Password:

  1. Read the literal value at <path> from the merged config.
  2. Create (or update) a 1Password item named after the path.
  3. Replace the literal in the talon overlay with op://...
  4. Round-trip a read through the resolver to confirm.

<path> uses the same dotted syntax as 'talon config get' / 'config
set'. Run 'talon secrets ls' first to see candidates.

Examples:
  talon secrets migrate gateway.auth.token
  talon secrets migrate channels.telegram.botToken --vault Personal`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			segments, err := config.ParsePath(path)
			if err != nil {
				return fmt.Errorf("parse path: %w", err)
			}
			paths := resolvePaths()
			merged, err := config.MergedBytes(paths)
			if err != nil {
				return fmt.Errorf("read merged config: %w", err)
			}

			cur := gjson.GetBytes(merged, strings.Join(segments, "."))
			if !cur.Exists() {
				return fmt.Errorf("path not found: %s", path)
			}
			if cur.Type != gjson.String {
				return fmt.Errorf("path %s is not a string (got %s); migrate only supports string secrets", path, gjsonTypeName(cur.Type))
			}
			value := cur.Str
			if value == "" {
				return fmt.Errorf("path %s has empty value (nothing to migrate)", path)
			}
			if secrets.IsReference(value) {
				return fmt.Errorf("path %s is already a reference (%s); skip", path, value)
			}
			if !secrets.IsSensitivePath(path) {
				return fmt.Errorf("path %s doesn't look sensitive (no token/password/secret/key/auth segment); refusing to migrate", path)
			}

			if itemName == "" {
				itemName = itemNameForPath(path)
			}
			ref := fmt.Sprintf("op://%s/%s/%s", vault, itemName, field)

			cmd.Println("Migrating", path)
			cmd.Printf("  → 1Password: vault=%s item=%s field=%s\n", vault, itemName, field)
			cmd.Println("  → Replacing literal in", paths.Talon.Config)
			cmd.Println("  → New reference:", ref)
			if dryRun {
				cmd.Println("(dry-run; no changes made)")
				return nil
			}
			if !yes {
				cmd.Print("Proceed? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
					return fmt.Errorf("aborted")
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Step 1: write to 1Password. We do this BEFORE
			// touching config so a 1P failure can't strand the
			// user with a broken config and an unsaved secret.
			if err := upsertOpItem(ctx, vault, itemName, field, value); err != nil {
				return fmt.Errorf("write to 1Password: %w", err)
			}

			// Step 2: round-trip via the resolver to confirm
			// the new reference can actually be read back.
			roundTrip, err := secrets.NewResolver().Resolve(ctx, ref)
			if err != nil {
				return fmt.Errorf("verify new reference: %w (config NOT modified)", err)
			}
			if roundTrip != value {
				return fmt.Errorf("verify mismatch: 1Password returned %d bytes, expected %d (config NOT modified)", len(roundTrip), len(value))
			}

			// Step 3: replace the config value. SetReplaceSafe
			// preserves the rest of the file structure.
			if _, err := config.Set(paths, segments, ref, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
				return fmt.Errorf("rewrite config: %w (1Password item exists at %s — re-run to replace literal)", err, ref)
			}
			cmd.Println("✓ Migrated", path, "→", ref)
			return nil
		},
	}
	c.Flags().StringVar(&vault, "vault", "Personal", "1Password vault to write into")
	c.Flags().StringVar(&field, "field", "credential", "1Password item field name")
	c.Flags().StringVar(&itemName, "item", "", "1Password item name (default derived from path)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen without writing")
	c.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation")
	return c
}

// itemNameForPath turns a dotted config path into a 1Password
// item name that's valid + reverse-readable. We replace dots with
// dashes (1P item names allow most characters but dots feel
// pathy + confusing) and prefix with "talon-" so the items are
// grouped in the user's vault search.
//
//	gateway.auth.token            → talon-gateway-auth-token
//	channels.telegram.botToken    → talon-channels-telegram-botToken
func itemNameForPath(path string) string {
	cleaned := strings.NewReplacer(
		".", "-",
		"[", "-",
		"]", "",
		`"`, "",
		" ", "-",
	).Replace(path)
	// Collapse runs of dashes left over from adjacent
	// separators (e.g. ."key" → -"key" → --key after quote
	// removal). Keeps the item name tidy.
	for strings.Contains(cleaned, "--") {
		cleaned = strings.ReplaceAll(cleaned, "--", "-")
	}
	return "talon-" + strings.Trim(cleaned, "-")
}

// upsertOpItem creates a 1Password item with the given field, or
// updates the field if the item already exists. Uses two `op`
// invocations because `op item create` errors when the item
// exists and `op item edit` errors when it doesn't — easier to
// dispatch on existence than parse the error code.
func upsertOpItem(ctx context.Context, vault, item, field, value string) error {
	if _, err := exec.LookPath("op"); err != nil {
		return fmt.Errorf("1Password CLI not on $PATH (brew install --cask 1password-cli)")
	}
	exists, err := opItemExists(ctx, vault, item)
	if err != nil {
		return fmt.Errorf("check item exists: %w", err)
	}
	if exists {
		// Edit the existing item's field. `password` items use
		// the field name verbatim; we always write the field
		// the user chose (default "credential").
		c := exec.CommandContext(ctx, "op", "item", "edit", item, "--vault", vault, fmt.Sprintf("%s=%s", field, value))
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("op item edit %s: %w", item, err)
		}
		return nil
	}
	c := exec.CommandContext(ctx, "op", "item", "create",
		"--category=password",
		"--vault", vault,
		"--title", item,
		fmt.Sprintf("%s=%s", field, value),
	)
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("op item create %s: %w", item, err)
	}
	return nil
}

// opItemExists checks whether a 1Password item with the given
// title exists in the named vault. `op item get` returns non-zero
// when not found; we use stderr containing "doesn't exist" or a
// non-zero exit as the negative signal.
func opItemExists(ctx context.Context, vault, item string) (bool, error) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "op", "item", "get", item, "--vault", vault, "--format", "json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// "isn't an item in" / "doesn't exist" / exit code 1 →
		// item missing. Anything else (auth failure, network) is
		// a real error.
		s := stderr.String()
		if strings.Contains(s, "isn't an item") || strings.Contains(s, "doesn't exist") || strings.Contains(s, "no item") {
			return false, nil
		}
		// op exits 1 on any error including not-found; only
		// surface as error if we don't recognize the message.
		return false, fmt.Errorf("op item get: %s", strings.TrimSpace(s))
	}
	return true, nil
}

func gjsonTypeName(t gjson.Type) string {
	switch t {
	case gjson.Null:
		return "null"
	case gjson.False, gjson.True:
		return "boolean"
	case gjson.Number:
		return "number"
	case gjson.String:
		return "string"
	case gjson.JSON:
		return "object/array"
	}
	return "unknown"
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

			// Use -U so an existing entry with the same service
			// name is updated rather than rejected as duplicate.
			args = []string{
				"add-generic-password",
				"-U",
				"-s", keychainServiceForOPToken,
				"-a", account,
				"-w", token,
			}
			runCmd := exec.CommandContext(ctx, "security", args...)
			runCmd.Stderr = os.Stderr
			if err := runCmd.Run(); err != nil {
				return fmt.Errorf("security add-generic-password: %w", err)
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
