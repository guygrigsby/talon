package main

// Tailscale exposure helpers. Wraps the local `tailscale` CLI to
// publish the gateway's HTTP listener over the user's tailnet
// (--tailscale=serve) or the public internet via Funnel
// (--tailscale=funnel). Required prerequisites:
//
//   - tailscale binary on $PATH
//   - host already enrolled in a tailnet (tailscaled running)
//   - for funnel mode: ACL grants this node funnel access (a
//     separate per-tailnet step at login.tailscale.com)
//
// We spawn `tailscale serve [...]` as a one-shot — Tailscale's
// `serve` subcommand persists its config, so we don't need to
// keep a daemon running on talon's side. On shutdown, when
// --tailscale-reset-on-exit is set, we run the equivalent
// "off" command to clean up.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// tailscaleMode is one of "off" / "serve" / "funnel". The empty
// string is treated as "off" (the default behavior).
type tailscaleMode string

const (
	tailscaleOff    tailscaleMode = "off"
	tailscaleServe  tailscaleMode = "serve"
	tailscaleFunnel tailscaleMode = "funnel"
)

// parseTailscaleMode normalizes the --tailscale flag value and
// surfaces invalid inputs as an error rather than silently falling
// back. Empty string and "off" both disable; anything else must
// match exactly.
func parseTailscaleMode(s string) (tailscaleMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return tailscaleOff, nil
	case "serve":
		return tailscaleServe, nil
	case "funnel":
		return tailscaleFunnel, nil
	default:
		return "", fmt.Errorf("--tailscale: unknown mode %q (want off|serve|funnel)", s)
	}
}

// tailscaleServeArgs builds the argv for `tailscale serve` based
// on mode + the gateway's local port. Funnel uses 443 (the only
// HTTPS port Funnel allows); serve uses 443 too for consistency
// (Tailscale terminates TLS with a per-tailnet cert). The local
// port is handed off as an http://127.0.0.1:<port> upstream.
func tailscaleServeArgs(mode tailscaleMode, gatewayPort int) ([]string, error) {
	if mode == tailscaleOff {
		return nil, nil
	}
	upstream := "http://127.0.0.1:" + strconv.Itoa(gatewayPort)
	args := []string{"serve", "--bg", "--https=443"}
	if mode == tailscaleFunnel {
		args = append(args, "--funnel=on")
	}
	args = append(args, upstream)
	return args, nil
}

// tailscaleResetArgs returns the argv for tearing down the serve
// configuration on shutdown. Mirrors Tailscale's "off" syntax.
func tailscaleResetArgs() []string {
	return []string{"serve", "--https=443", "off"}
}

// applyTailscale runs `tailscale serve` according to mode. Returns
// nil on success (or no-op for "off"); error if the binary is
// missing or the command failed. Output is forwarded to stderr so
// the user sees Tailscale's diagnostics inline with talon's logs.
func applyTailscale(ctx context.Context, mode tailscaleMode, gatewayPort int) error {
	args, err := tailscaleServeArgs(mode, gatewayPort)
	if err != nil {
		return err
	}
	if args == nil {
		return nil
	}
	if _, err := exec.LookPath("tailscale"); err != nil {
		return errors.New("--tailscale=" + string(mode) + " requires the `tailscale` CLI on $PATH (install: https://tailscale.com/download)")
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "tailscale", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tailscale %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// resetTailscale runs the teardown command. Best-effort: if it
// fails the user has to clean up manually but talon shouldn't
// block shutdown over it. Errors print to stderr.
func resetTailscale(ctx context.Context) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "tailscale", tailscaleResetArgs()...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "talon: tailscale reset on exit failed: %v\n", err)
	}
}
