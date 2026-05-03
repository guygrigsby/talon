// talon-op-plugin — resolves op:// references via the 1Password
// CLI. CLI shape (one-shot, exit on completion):
//
//	talon-op-plugin op://<vault>/<item>/<field>
//	→ prints cleartext to stdout, exits 0
//	→ on failure, prints reason to stderr, non-zero exit
//
// Authentication: requires the 1Password `op` CLI on $PATH and
// EITHER:
//   - OP_SERVICE_ACCOUNT_TOKEN exported in the environment, OR
//   - the keychain entry "talon.opAccessToken" populated (set up
//     via `talon secrets keychain-bootstrap`). The plugin auto-
//     loads from the keychain when the env var is empty so a fresh
//     shell doesn't have to source anything.
//
// Why a separate Go plugin instead of inlining the shell-out in
// internal/secrets: the user's principle is that integrations
// should be plugins. New plugins go here in apps/, follow the
// talon-<name>-plugin convention, and ship as their own static
// binary. Keeps the gateway binary integration-free.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// keychainServiceForOPToken is the macOS keychain entry name where
// the 1Password service-account token lives. Configurable later;
// today the constant matches what `talon secrets keychain-bootstrap`
// writes.
const keychainServiceForOPToken = "talon.opAccessToken"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: talon-op-plugin op://<vault>/<item>/<field>")
		os.Exit(2)
	}
	ref := os.Args[1]
	if !strings.HasPrefix(ref, "op://") {
		fmt.Fprintf(os.Stderr, "talon-op-plugin: expected op:// reference, got %q\n", ref)
		os.Exit(2)
	}
	if _, err := exec.LookPath("op"); err != nil {
		fmt.Fprintln(os.Stderr, "talon-op-plugin: 1Password CLI required (brew install --cask 1password-cli)")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Bootstrap auth: if OP_SERVICE_ACCOUNT_TOKEN isn't already in
	// env, try to pull it from the macOS keychain. Lets a fresh
	// shell run `talon dashboard` (or any op:// resolution) without
	// needing the user to source a shell profile. Failures are
	// silent — `op` itself will then complain with its own
	// "service account token required" message.
	env := os.Environ()
	if os.Getenv("OP_SERVICE_ACCOUNT_TOKEN") == "" {
		if tok := readKeychainToken(ctx, keychainServiceForOPToken); tok != "" {
			env = append(env, "OP_SERVICE_ACCOUNT_TOKEN="+tok)
		}
	}

	cmd := exec.CommandContext(ctx, "op", "read", ref)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "talon-op-plugin: op read %s: %v\n", ref, err)
		os.Exit(1)
	}
	// Strip the trailing newline `op` always appends; downstream
	// consumers don't want to deal with it.
	fmt.Print(strings.TrimRight(string(out), "\r\n"))
}

// readKeychainToken returns the stored OP_SERVICE_ACCOUNT_TOKEN
// from the macOS keychain or "" when the lookup fails (no entry,
// non-mac, security CLI missing). Never panics, never logs —
// caller decides what to do when the result is empty.
func readKeychainToken(ctx context.Context, service string) string {
	if _, err := exec.LookPath("security"); err != nil {
		return ""
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "security", "find-generic-password", "-s", service, "-w")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}
