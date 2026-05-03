// talon-op-plugin — resolves op:// references via the 1Password
// CLI. CLI shape (one-shot, exit on completion):
//
//	talon-op-plugin op://<vault>/<item>/<field>
//	→ prints cleartext to stdout, exits 0
//	→ on failure, prints reason to stderr, non-zero exit
//
// Authentication: requires the 1Password `op` CLI on $PATH and
// either OP_SERVICE_ACCOUNT_TOKEN exported or an interactive
// `op signin` session active. The recommended bootstrap stores
// the service-account token in the macOS keychain and exports it
// via talon-keychain-plugin (see `talon secrets keychain-bootstrap`).
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "op", "read", ref)
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
