// Package main — `talon configure tailscale`. Provisions a Tailscale
// VIPService and binds the gateway to it (ADR 0008).
//
// Flow:
//  1. Prompt for the OAuth client id + secret (TS_OAUTH_CLIENT_ID /
//     TS_OAUTH_CLIENT_SECRET env vars also accepted).
//  2. Exchange them for an access token and read the tailnet's MagicDNS
//     name — verifies the credentials.
//  3. Prompt for a service label (default "talon"; normalized to
//     "svc:talon") and a port (default 443).
//  4. Create the VIPService via the API (idempotent).
//  5. Print the ACL grant snippet the tag needs to advertise the service
//     and offer to... print it (default). We never silently edit the
//     user's policy file.
//  6. Store the OAuth secret as a keychain ref (never plaintext) and write:
//       gateway.tailscale.oauth_client_id  = <id>           (plaintext, not a secret)
//       gateway.tailscale.oauth_client_ref = keychain://…   (ref to the secret)
//       gateway.tailscale.service          = "svc:talon"
//       gateway.tailscale.tailnet          = <tailnet>.ts.net
//       gateway.port                       = <port>
//       gateway.bind                       = "tailnet"
//  7. Print the resulting URL (https://talon.<tailnet>.ts.net) + a restart
//     hint (gateway.bind is restart-class per talon-5zx).

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/guygrigsby/talon/internal/tailscale"
	"github.com/spf13/cobra"
)

// tailscaleProvisioner is the provision-time API surface the wizard needs.
// *tailscale.Client satisfies it; tests inject a fake.
type tailscaleProvisioner interface {
	TailnetName(ctx context.Context) (string, error)
	CreateService(ctx context.Context, svcName string, ports []string) error
}

// Injectable seams for tests.
var (
	newTailscaleClient = func(ctx context.Context, id, secret string) (tailscaleProvisioner, error) {
		return tailscale.NewFromOAuth(ctx, id, secret)
	}
	storeTailscaleSecret = secrets.StoreKeychainSecret
)

// configureTailscale is the interactive Tailscale tailnet-service wizard.
func configureTailscale(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, "Tailscale (tailnet service) setup")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "This provisions a Tailscale VIPService and binds the gateway to it,")
	fmt.Fprintln(out, "exposing talon at a stable https://talon.<tailnet>.ts.net URL.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "You'll need a Tailscale OAuth client (Admin console → Settings → OAuth")
	fmt.Fprintln(out, "clients) with the auth_keys + services scopes and the tag:talon owner.")
	fmt.Fprintln(out)

	// 1. OAuth client id.
	id := strings.TrimSpace(os.Getenv("TS_OAUTH_CLIENT_ID"))
	if id != "" {
		fmt.Fprint(out, "Found TS_OAUTH_CLIENT_ID in environment. Use it? [Y/n] ")
		if line, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n") {
			id = ""
		}
	}
	if id == "" {
		fmt.Fprint(out, "OAuth client id: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read client id: %w", err)
		}
		id = strings.TrimSpace(line)
	}
	if id == "" {
		return errors.New("OAuth client id is required")
	}

	// OAuth client secret.
	secret := strings.TrimSpace(os.Getenv("TS_OAUTH_CLIENT_SECRET"))
	if secret != "" {
		fmt.Fprint(out, "Found TS_OAUTH_CLIENT_SECRET in environment. Use it? [Y/n] ")
		if line, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n") {
			secret = ""
		}
	}
	if secret == "" {
		fmt.Fprint(out, "OAuth client secret: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read client secret: %w", err)
		}
		secret = strings.TrimSpace(line)
	}
	if secret == "" {
		return errors.New("OAuth client secret is required")
	}

	// 2. Verify the credentials by reading the tailnet name.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := newTailscaleClient(ctx, id, secret)
	if err != nil {
		return fmt.Errorf("authenticate OAuth client: %w", err)
	}
	tailnetName, err := client.TailnetName(ctx)
	if err != nil {
		return fmt.Errorf("read tailnet name (check OAuth scopes): %w", err)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "✓ Connected to tailnet %s\n", tailnetName)
	fmt.Fprintln(out)

	// 3. Service label + port.
	fmt.Fprint(out, "Service label [talon]: ")
	line, _ := reader.ReadString('\n')
	label := strings.TrimSpace(line)
	if label == "" {
		label = "talon"
	}
	label = strings.TrimPrefix(label, "svc:")
	svcName := "svc:" + label

	fmt.Fprint(out, "Service port [443]: ")
	line, _ = reader.ReadString('\n')
	port := 443
	if s := strings.TrimSpace(line); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("invalid port %q (want 1..65535)", s)
		}
		port = n
	}

	// 4. Create the VIPService (idempotent).
	if err := client.CreateService(ctx, svcName, []string{strconv.Itoa(port)}); err != nil {
		return fmt.Errorf("create service %s: %w", svcName, err)
	}
	fmt.Fprintf(out, "✓ Service %s ready\n", svcName)
	fmt.Fprintln(out)

	// 5. ACL grant — print, never silently apply.
	fmt.Fprintln(out, "Add the following to your tailnet policy (Admin console → Access controls)")
	fmt.Fprintln(out, "so the tag can advertise + reach the service:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, aclGrantSnippet(svcName, port))
	fmt.Fprintln(out)
	fmt.Fprint(out, "Apply this grant to your tailnet policy now? [y/N] ")
	line, _ = reader.ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
		// Auto-apply via the policy API is not part of v1 (ADR 0008:
		// print-and-paste). Tell the user to paste it manually.
		fmt.Fprintln(out, "Automatic policy edits aren't supported yet — paste the snippet above")
		fmt.Fprintln(out, "into your tailnet policy file in the admin console.")
	} else {
		fmt.Fprintln(out, "Paste the snippet above into your tailnet policy file when ready.")
	}
	fmt.Fprintln(out)

	// 6. Store the secret as a ref and write config.
	storeCtx, storeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer storeCancel()
	ref, err := storeTailscaleSecret(storeCtx, "talon.gateway.tailscale.oauthSecret", secret)
	if err != nil {
		return fmt.Errorf("store OAuth secret: %w", err)
	}

	paths := resolvePaths()
	writes := []struct {
		path  []string
		value any
	}{
		{[]string{"gateway", "tailscale", "oauth_client_id"}, id},
		{[]string{"gateway", "tailscale", "oauth_client_ref"}, ref},
		{[]string{"gateway", "tailscale", "service"}, svcName},
		{[]string{"gateway", "tailscale", "tailnet"}, tailnetName},
		{[]string{"gateway", "port"}, port},
		{[]string{"gateway", "bind"}, "tailnet"},
	}
	for _, w := range writes {
		if _, err := config.Set(paths, w.path, w.value, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
			return fmt.Errorf("config set %s: %w", strings.Join(w.path, "."), err)
		}
	}

	// 7. Final URL + restart hint.
	host := strings.TrimSuffix(tailnetName, ".")
	fmt.Fprintln(out, "✓ Configuration written.")
	fmt.Fprintf(out, "  URL: https://%s.%s\n", label, host)
	fmt.Fprintln(out)
	fmt.Fprintln(out, config.ReloadRestart.Hint("gateway.bind"))
	return nil
}

// aclGrantSnippet returns the policy grant + autoApprovers the tag needs to
// advertise + reach the service (ADR 0008 Findings).
func aclGrantSnippet(svcName string, port int) string {
	return fmt.Sprintf(`  // grant members access to the service
  { "src": ["autogroup:member"], "dst": [%q], "ip": [%q] }

  // let the tag advertise the service
  "autoApprovers": { "services": { %q: ["tag:talon"] } }`,
		svcName, strconv.Itoa(port), svcName)
}

// configureTailscaleCmd exposes the wizard as `talon configure tailscale`.
func configureTailscaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tailscale",
		Short: "Provision a Tailscale VIPService and bind the gateway to it",
		Long: `Provision a Tailscale VIPService and bind the gateway to your tailnet.
Walks through OAuth client setup, service creation, the required ACL grant,
and writes gateway.bind=tailnet so the gateway serves at a stable
https://talon.<tailnet>.ts.net URL.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return configureTailscale(os.Stdin, os.Stdout)
		},
	}
}
