// keychain:// resolver — inlined from the former talon-keychain-plugin.
//
// The plugin was a 65-line wrapper around `security find-generic-password`
// that lived as a separate binary in apps/. Distribution sucked: any
// install path that didn't co-install the helper (`go install ./cmd/talon`,
// most release tarballs people would build) left users with config refs
// that couldn't resolve. Inlining makes one-binary installs Just Work.
//
// Principled exception to "integrations live in plugins": this is a
// system-API call against the macOS Security framework's CLI front-end,
// not an integration with an outside service. The op:// resolver still
// goes through talon-op-plugin — it has bootstrap state worth isolating.

package secrets

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// resolveKeychainRef reads the cleartext value behind keychain://<target>.
// Target shape is "<service>" or "<service>/<account>"; trailing slash
// without an account is treated as empty account (matches the original
// plugin's splitTarget).
//
// macOS-only — every other OS errors with a clear message rather than
// silently returning an empty string. Five-second timeout caps the
// shell-out.
func resolveKeychainRef(ctx context.Context, target string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("secrets: keychain:// is macOS-only (current: %s)", runtime.GOOS)
	}
	service, account := splitKeychainTarget(target)
	if service == "" {
		return "", fmt.Errorf("secrets: keychain:// has empty service name")
	}
	if _, err := exec.LookPath("security"); err != nil {
		return "", fmt.Errorf("secrets: macOS `security` CLI required for keychain:// refs")
	}

	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "security", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("secrets: security find-generic-password -s %s: %w", service, err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// splitKeychainTarget extracts (service, account) from "service" or
// "service/account". Last slash wins so service names that happen to
// contain slashes survive — only the trailing segment is treated as
// the account selector.
func splitKeychainTarget(t string) (service, account string) {
	if i := strings.LastIndex(t, "/"); i >= 0 {
		return t[:i], t[i+1:]
	}
	return t, ""
}
