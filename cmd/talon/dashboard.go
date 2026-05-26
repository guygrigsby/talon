// Package main — `talon dashboard`. Drop-in for openclaw's
// `dashboard` command: prints the gateway URL, copies it to the
// clipboard, and opens the browser. The token is resolved through
// the secrets resolver so a `gateway.auth.token: op://...` config
// works the same as a literal token.
//
// Defaults that mirror openclaw's behavior:
//   - port:       gateway.port (config) || 18789
//   - bind:       gateway.bind (config) || "loopback"
//   - LAN coerce: lan → loopback for the URL host (browsers refuse
//                 secure-context features on raw LAN IPs anyway)
//   - --no-open:  skip the browser launch (still prints + copies)

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
)

func dashboardCmd() *cobra.Command {
	var (
		noOpen    bool
		uiHost    string
		session   string
		tokenFlag string
	)
	c := &cobra.Command{
		Use:   "dashboard",
		Short: "Print the dashboard URL, copy to clipboard, and open in your browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := resolvePaths()
			merged, err := config.MergedBytes(paths)
			if err != nil {
				return fmt.Errorf("read merged config: %w", err)
			}
			port := int(gjson.GetBytes(merged, "gateway.port").Int())
			if port == 0 {
				port = 18789
			}
			// Token resolution order:
			//   1. --token flag (explicit override; covers gateways
			//      running with a CLI-only --token that isn't in
			//      config)
			//   2. gateway.auth.token from merged config, routed
			//      through the secrets resolver so op://...,
			//      keychain://..., and literal values all work
			//   3. empty — URL goes out without #token=, the FE
			//      surfaces a friendly auth-required message
			token := tokenFlag
			if token == "" {
				tokenRef := gjson.GetBytes(merged, "gateway.auth.token").Str
				if tokenRef != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					resolved, err := secrets.NewResolver().Resolve(ctx, tokenRef)
					if err != nil {
						fmt.Fprintf(os.Stderr, "talon: dashboard: token resolve failed (%v); URL will not include auto-auth\n", err)
					} else {
						token = resolved
					}
				}
			}
			// Default path is "/" — the SvelteKit chat lives at the
			// root route. The legacy openclaw UI used "/chat";
			// pass --ui-host together with a route override if you
			// need to target it.
			u := buildUIURL(uiHost, "localhost", port, token, session, "/")
			fmt.Fprintln(cmd.OutOrStdout(), "Dashboard URL:", u)
			if token != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Token auto-auth included in URL.")
			}
			if err := copyToClipboard(u); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Copy to clipboard unavailable.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Copied to clipboard.")
			}
			if noOpen {
				return nil
			}
			if err := openInBrowser(u); err != nil {
				fmt.Fprintf(os.Stderr, "talon: could not auto-open (%v); use the URL above\n", err)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Opened in your browser.")
			return nil
		},
	}
	c.Flags().BoolVar(&noOpen, "no-open", false, "skip browser launch (still prints + copies the URL)")
	c.Flags().StringVar(&uiHost, "ui-host", defaultUIHost, "UI base URL (empty = gateway's own URL)")
	c.Flags().StringVar(&session, "session", "", "session key to put in the URL (empty = omit; the SPA currently ignores this param)")
	c.Flags().StringVar(&tokenFlag, "token", "", "explicit token override (use when the gateway runs with --token but no gateway.auth.token in config)")
	return c
}

// copyToClipboard puts s into the system clipboard. macOS uses
// pbcopy; Linux tries wl-copy then xclip; Windows uses clip.exe.
// Best-effort — error means "not available," not a fatal issue.
func copyToClipboard(s string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		}
	case "windows":
		cmd = exec.Command("clip.exe")
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stdin.Write([]byte(s))
	stdin.Close()
	return cmd.Wait()
}
