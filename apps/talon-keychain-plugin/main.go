// talon-keychain-plugin — resolves keychain:// references via the
// macOS `security` CLI. CLI shape (one-shot, exit on completion):
//
//	talon-keychain-plugin keychain://<service>[/<account>]
//	→ prints cleartext to stdout, exits 0
//	→ on failure, prints reason to stderr, non-zero exit
//
// Reference target is the keychain service name; an optional
// "/account" suffix narrows by account when multiple entries
// share a service. Reads from the user's login keychain.
//
// Why this is a plugin: same reason as talon-op-plugin — the
// user's principle is that integrations live in plugins, not in
// the gateway binary itself. Keychain access is also macOS-only,
// so isolating it here means the gateway stays portable.

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
		fmt.Fprintln(os.Stderr, "usage: talon-keychain-plugin keychain://<service>[/<account>]")
		os.Exit(2)
	}
	ref := os.Args[1]
	if !strings.HasPrefix(ref, "keychain://") {
		fmt.Fprintf(os.Stderr, "talon-keychain-plugin: expected keychain:// reference, got %q\n", ref)
		os.Exit(2)
	}
	target := strings.TrimPrefix(ref, "keychain://")
	service, account := splitTarget(target)
	if service == "" {
		fmt.Fprintln(os.Stderr, "talon-keychain-plugin: empty service name in reference")
		os.Exit(2)
	}

	if _, err := exec.LookPath("security"); err != nil {
		fmt.Fprintln(os.Stderr, "talon-keychain-plugin: macOS `security` CLI required")
		os.Exit(1)
	}

	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "talon-keychain-plugin: security find-generic-password -s %s: %v\n", service, err)
		os.Exit(1)
	}
	fmt.Print(strings.TrimRight(string(out), "\r\n"))
}

// splitTarget extracts (service, account) from "service" or
// "service/account". Trailing slash without an account is treated
// as an empty account.
func splitTarget(t string) (string, string) {
	if i := strings.LastIndex(t, "/"); i >= 0 {
		return t[:i], t[i+1:]
	}
	return t, ""
}
