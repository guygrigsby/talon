//go:build darwin

package macopen

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runOpen invokes the macOS `open` command with the prepared argv.
// The argv is built upstream by buildOpenArgs; everything here is
// just process plumbing — no shell, no string concatenation, no
// injection surface.
//
// Five-second cap on the wait. `open` returns very quickly (it
// hands off to LaunchServices); the timeout is just insurance
// against pathological states where launchd is stuck.
func runOpen(ctx context.Context, argv []string) error {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "open", argv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open %s: %w (output: %s)", strings.Join(argv, " "), err, truncate(string(out), 256))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
