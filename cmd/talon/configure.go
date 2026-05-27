// Package main — `talon configure channel <name>`. Interactive wizards
// per channel. Additional channels drop into the same shape.
//
// Telegram flow:
//   1. Prompt for the bot token (TELEGRAM_BOT_TOKEN env var also accepted)
//   2. Verify via /getMe — print the bot's @username so the user knows
//      which bot to message
//   3. Prompt: "DM @<bot> with /start (or anything), then press Enter"
//   4. Long-poll /getUpdates until a message lands; capture from.id
//   5. Send a confirmation message back via /sendMessage
//   6. Persist:
//        channels.telegram.botToken   = keychain://talon.channels.telegram.botToken/talon
//        channels.telegram.allowFrom  = ["<from.id>"]
//        channels.telegram.dmPolicy   = "allowlist"
//        plugins.entries.telegram     = {enabled, cmd}
//   7. Tell the user to restart the gateway so the plugin spawns
//
// BlueBubbles flow (V1, no auto-capture — we ask for the user's
// handle directly to avoid a temporary HTTP listener + back-and-forth
// admin config dance):
//   1. Prompt for serverUrl + password (BLUEBUBBLES_SERVER_URL /
//      BLUEBUBBLES_PASSWORD env vars also accepted)
//   2. Verify via GET /api/v1/server/info — confirms the server is
//      reachable and the password is right
//   3. Prompt for webhookPort (default 18792) and the user's iMessage
//      handle (phone or email)
//   4. Print the webhook URL the user must paste into the BlueBubbles
//      admin UI
//   5. Persist:
//        channels.bluebubbles.serverUrl, password ref, webhookPort,
//                              allowFrom, dmPolicy=allowlist, agentId
//        plugins.entries.bluebubbles = {enabled, cmd}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/guygrigsby/talon/internal/telegram"
	"github.com/spf13/cobra"
)

func configureCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "configure",
		Short: "Interactive setup wizards (channels, providers, etc.)",
		Long: `Interactive setup wizards. Run with no arguments for a menu-driven flow
that lists every wizard talon ships, or jump straight to one with a
subcommand (e.g. 'talon configure channel telegram').`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigureMenu(os.Stdin, os.Stdout)
		},
	}
	c.AddCommand(configureChannelCmd())
	c.AddCommand(configureTailscaleCmd())
	return c
}

// configureWizard is one entry on the configure menu. Adding a new
// wizard: append one entry to configureWizardsForTest. Existing
// subcommands ('configure channel <name>') route to it via Kind +
// Name without per-channel switch arms.
type configureWizard struct {
	// Kind groups wizards on the menu and routes subcommand args
	// to the right subset (e.g. 'configure channel <name>' looks
	// up Kind=="channel"). Use a stable lowercase token.
	Kind string
	// Name is the canonical short name a user types after the
	// subcommand (e.g. "telegram", "bluebubbles"). Aliases are
	// accepted via Aliases.
	Name string
	// Aliases are alternate names the subcommand accepts (e.g.
	// "bb" → "bluebubbles"). Empty for wizards without aliases.
	Aliases []string
	// Label is what the menu prints (e.g. "Telegram"). The Kind
	// is added as a prefix in the top-level menu so the same Label
	// can stay short in a per-Kind sub-menu.
	Label string
	// Run is the wizard body. Receives the same in/out the
	// subcommand uses, so menu and direct invocation share UX.
	Run func(in io.Reader, out io.Writer) error
}

// configureWizardsForTest is the canonical wizard list. Channel
// wizards listed first because that's what most users come here for;
// provider/agent wizards land below as they're added.
//
// var (not const/func) so tests can patch it via patchWizards.
var configureWizardsForTest = []configureWizard{
	{Kind: "channel", Name: "telegram", Label: "Telegram", Run: configureTelegram},
	{Kind: "channel", Name: "bluebubbles", Aliases: []string{"bb"}, Label: "BlueBubbles (iMessage)", Run: configureBluebubbles},
	{Kind: "tailscale", Name: "tailscale", Aliases: []string{"ts"}, Label: "Tailscale (tailnet service)", Run: configureTailscale},
}

// wizardsByKind returns the subset of registered wizards whose Kind
// matches. Pure filter — order preserved from configureWizardsForTest.
func wizardsByKind(kind string) []configureWizard {
	out := make([]configureWizard, 0, len(configureWizardsForTest))
	for _, w := range configureWizardsForTest {
		if w.Kind == kind {
			out = append(out, w)
		}
	}
	return out
}

// findWizard looks a wizard up by Kind + (Name|alias). Used by the
// 'configure <kind> <name>' subcommands to skip the menu when the
// user already knows what they want.
func findWizard(kind, name string) (configureWizard, bool) {
	name = strings.ToLower(name)
	for _, w := range configureWizardsForTest {
		if w.Kind != kind {
			continue
		}
		if strings.EqualFold(w.Name, name) {
			return w, true
		}
		for _, a := range w.Aliases {
			if strings.EqualFold(a, name) {
				return w, true
			}
		}
	}
	return configureWizard{}, false
}

// runConfigureMenu drives the top-level interactive picker: numbered list,
// single line to pick, q to quit. Loops after a wizard finishes so users can
// do channel-then-provider in one sitting.
func runConfigureMenu(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	wizards := configureWizardsForTest
	for {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "talon configure")
		fmt.Fprintln(out, "───────────────")
		for i, w := range wizards {
			label := w.Label
			if w.Kind != "" {
				// "Channel: Telegram" rather than just "Telegram" so
				// the top-level menu reads sensibly across kinds.
				// strings.Title-equivalent without the dependency:
				// uppercase the first letter of Kind.
				k := w.Kind
				if k != "" {
					k = strings.ToUpper(k[:1]) + k[1:]
				}
				label = k + ": " + w.Label
			}
			fmt.Fprintf(out, "  %d) %s\n", i+1, label)
		}
		fmt.Fprintln(out, "  q) Quit")
		fmt.Fprintln(out)
		fmt.Fprint(out, "Pick a wizard: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Stdin closed (piped input ran out). Treat as
				// quit so non-interactive callers don't loop.
				fmt.Fprintln(out)
				return nil
			}
			return fmt.Errorf("read selection: %w", err)
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			continue
		}
		if strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") || strings.EqualFold(choice, "exit") {
			return nil
		}
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(wizards) {
			fmt.Fprintf(out, "  invalid selection %q — pick 1..%d or q\n", choice, len(wizards))
			continue
		}
		fmt.Fprintln(out)
		if err := wizards[idx-1].Run(in, out); err != nil {
			// Surface the wizard's error to the menu and continue
			// rather than nuke the whole session — the user can
			// retry or pick a different wizard.
			fmt.Fprintf(out, "\nwizard failed: %v\n", err)
			continue
		}
	}
}

func configureChannelCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "channel [name]",
		Short: "Configure a channel (interactive picker when run without a name)",
		Long: `Configure a channel. With a name, runs that channel's wizard directly
(e.g. 'talon configure channel telegram'). With no name, lists every
channel wizard talon ships and prompts you to pick — same behavior as
the top-level 'talon configure' menu, scoped to channels.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			channels := wizardsByKind("channel")
			if len(args) == 0 {
				return runWizardSubmenu(os.Stdin, os.Stdout, "channel", channels)
			}
			w, ok := findWizard("channel", args[0])
			if !ok {
				names := make([]string, 0, len(channels))
				for _, ch := range channels {
					names = append(names, ch.Name)
				}
				return fmt.Errorf("configure channel: %q is not yet supported (try: %s)", args[0], strings.Join(names, ", "))
			}
			return w.Run(os.Stdin, os.Stdout)
		},
	}
	return c
}

// runWizardSubmenu is the per-Kind picker. Same UX as the top-level
// menu but scoped to one category so 'configure channel' doesn't show
// providers, agents, etc. Header reflects the kind so users know
// which subset they're picking from.
func runWizardSubmenu(in io.Reader, out io.Writer, kind string, wizards []configureWizard) error {
	if len(wizards) == 0 {
		return fmt.Errorf("no %s wizards registered", kind)
	}
	reader := bufio.NewReader(in)
	for {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "talon configure %s\n", kind)
		fmt.Fprintln(out, strings.Repeat("─", len("talon configure ")+len(kind)))
		for i, w := range wizards {
			fmt.Fprintf(out, "  %d) %s\n", i+1, w.Label)
		}
		fmt.Fprintln(out, "  q) Quit")
		fmt.Fprintln(out)
		fmt.Fprint(out, "Pick: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(out)
				return nil
			}
			return fmt.Errorf("read selection: %w", err)
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			continue
		}
		if strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") || strings.EqualFold(choice, "exit") {
			return nil
		}
		idx, err := strconv.Atoi(choice)
		if err != nil || idx < 1 || idx > len(wizards) {
			fmt.Fprintf(out, "  invalid selection %q — pick 1..%d or q\n", choice, len(wizards))
			continue
		}
		fmt.Fprintln(out)
		if err := wizards[idx-1].Run(in, out); err != nil {
			fmt.Fprintf(out, "\nwizard failed: %v\n", err)
			continue
		}
	}
}

// configureTelegram is the interactive wizard. in/out are injectable
// so a future test can drive it; production passes os.Stdin /
// os.Stdout. Errors surfaced from the function abort the wizard and
// land in the user's terminal verbatim — no half-written config (we
// only call config.Set after every step succeeds).
func configureTelegram(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, "Telegram setup")
	fmt.Fprintln(out)

	// 1. Token. Prefer env, fall back to prompt.
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token != "" {
		fmt.Fprintf(out, "Found TELEGRAM_BOT_TOKEN in environment. Use it? [Y/n] ")
		if line, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n") {
			token = ""
		}
	}
	if token == "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "How to get a token:")
		fmt.Fprintln(out, "  1) Open Telegram and chat with @BotFather")
		fmt.Fprintln(out, "  2) Run /newbot (or /mybots)")
		fmt.Fprintln(out, "  3) Copy the token (looks like 123456:ABC...)")
		fmt.Fprintln(out)
		fmt.Fprint(out, "Bot token: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return errors.New("bot token is required")
	}

	// 2. Verify token by calling /getMe. Print the bot identity so the
	//    user can tell which @handle to message in step 3.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bot, err := telegram.GetMe(ctx, token)
	if err != nil {
		return fmt.Errorf("verify token via getMe: %w", err)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "✓ Token verified. Bot: @%s (%s)\n", bot.Username, bot.FirstName)
	fmt.Fprintln(out)

	// 3. Capture the user's chat. Drain any prior unread updates so
	//    we don't pick up a stale message; then ask the user to
	//    DM the bot now and long-poll for the new one.
	startOffset, err := telegram.DrainUpdates(ctx, token)
	if err != nil {
		return fmt.Errorf("drain prior updates: %w", err)
	}
	fmt.Fprintf(out, "Open Telegram and message @%s. Send /start (or any text).\n", bot.Username)
	fmt.Fprintln(out, "(Press Ctrl-C to abort.)")
	fmt.Fprint(out, "Waiting for your message...")

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 95*time.Second)
	defer waitCancel()
	chat, err := telegram.WaitForMessage(waitCtx, token, startOffset, 90*time.Second)
	if err != nil {
		fmt.Fprintln(out)
		// 409 Conflict means another process (typically the
		// running gateway's telegram plugin) is already long-
		// polling this bot — Telegram only allows one. Surface
		// the actionable fix instead of the raw HTTP error.
		if strings.Contains(err.Error(), "http 409") {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Another process is already polling this bot. Telegram allows only one")
			fmt.Fprintln(out, "long-poll consumer per token, so the running gateway is blocking the")
			fmt.Fprintln(out, "wizard. Options:")
			fmt.Fprintln(out, "  1. Stop the gateway (Ctrl-C), re-run this wizard, then restart.")
			fmt.Fprintln(out, "  2. Set the sender id manually:")
			fmt.Fprintln(out, "       talon config set channels.telegram.allowFrom '[\"<your-numeric-id>\"]'")
			fmt.Fprintln(out, "     Find your id by DMing @userinfobot on Telegram, or by checking the")
			fmt.Fprintln(out, "     gateway log for `telegram first inbound message sender=…` when you")
			fmt.Fprintln(out, "     message the bot.")
			return errors.New("telegram capture aborted: bot is being polled by another process")
		}
		return fmt.Errorf("wait for first message: %w", err)
	}
	fmt.Fprintln(out, " got it.")
	fmt.Fprintf(out, "✓ Captured sender id %d (%s)\n", chat.SenderID, chat.DisplayName)

	// 4. Send the confirmation message. Establishes that the round-trip
	//    works and closes the loop the user expects.
	confirm := fmt.Sprintf("✓ talon configured for @%s.\nFuture replies in this chat are routed through your agent.", bot.Username)
	if err := telegram.SendMessage(ctx, token, chat.ChatID, confirm); err != nil {
		// Non-fatal — the config write is still valuable. Log and continue.
		fmt.Fprintf(out, "warn: send confirmation: %v\n", err)
	}

	// 5. Persist. Order: channel config, then plugins.entries, so a
	//    half-applied wizard leaves talon refusing to spawn the
	//    plugin rather than running it without a token.
	paths := resolvePaths()
	senderIDStr := strconv.FormatInt(chat.SenderID, 10)
	storeCtx, storeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer storeCancel()
	tokenRef, err := secrets.StoreKeychainSecret(storeCtx, "talon.channels.telegram.botToken", token)
	if err != nil {
		return fmt.Errorf("store Telegram token in keychain: %w", err)
	}
	writes := []struct {
		path  []string
		value any
	}{
		{[]string{"channels", "telegram", "botToken"}, tokenRef},
		{[]string{"channels", "telegram", "allowFrom"}, []any{senderIDStr}},
		{[]string{"channels", "telegram", "dmPolicy"}, "allowlist"},
		{[]string{"channels", "telegram", "agentId"}, "main"},
		{[]string{"plugins", "entries", "telegram"}, map[string]any{
			"enabled": true,
		}},
	}
	for _, w := range writes {
		if _, err := config.Set(paths, w.path, w.value, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
			return fmt.Errorf("config set %s: %w", strings.Join(w.path, "."), err)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "✓ Configuration written. Restart the gateway so the plugin loads:")
	fmt.Fprintln(out, "    make docker-stop && make docker-run")
	return nil
}

// defaultBluebubblesWebhookPort is the listener port the wizard writes
// when the user accepts the suggested default. Picked one above the
// gateway port (18789/18790) to stay in the same neighborhood and out
// of common service ranges.
const defaultBluebubblesWebhookPort = 18792

// configureBluebubbles is the V1 BlueBubbles wizard. Asks for the
// server URL + password + webhook port + the user's iMessage handle,
// verifies the server, then writes channels.bluebubbles.* and enables
// the plugin. No auto-capture: the user knows their own number, and
// driving the BlueBubbles webhook end-to-end during setup would
// require a temporary HTTP listener plus an out-of-band admin
// reconfig — saved for V2.
func configureBluebubbles(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, "BlueBubbles setup")
	fmt.Fprintln(out)

	// 1. Server URL + password. Env first, prompt as fallback.
	serverURL := strings.TrimSpace(os.Getenv("BLUEBUBBLES_SERVER_URL"))
	if serverURL != "" {
		fmt.Fprintf(out, "Found BLUEBUBBLES_SERVER_URL=%s. Use it? [Y/n] ", serverURL)
		if line, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n") {
			serverURL = ""
		}
	}
	if serverURL == "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "BlueBubbles server URL (the address of the BlueBubbles Mac app's API).")
		fmt.Fprintln(out, "Examples: http://192.168.1.10:1234   https://my-mac.tailnet:1234")
		fmt.Fprint(out, "Server URL: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read server URL: %w", err)
		}
		serverURL = strings.TrimSpace(line)
	}
	if serverURL == "" {
		return errors.New("server URL is required")
	}
	serverURL = strings.TrimRight(serverURL, "/")

	password := strings.TrimSpace(os.Getenv("BLUEBUBBLES_PASSWORD"))
	if password != "" {
		fmt.Fprint(out, "Found BLUEBUBBLES_PASSWORD in environment. Use it? [Y/n] ")
		if line, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "n") {
			password = ""
		}
	}
	if password == "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Server password (set in the BlueBubbles app under Server > API).")
		fmt.Fprint(out, "Password: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimSpace(line)
	}
	if password == "" {
		return errors.New("password is required")
	}

	// 2. Verify by hitting /api/v1/server/info?password=…
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := bluebubblesServerInfo(ctx, serverURL, password)
	if err != nil {
		return fmt.Errorf("verify BlueBubbles server: %w", err)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "✓ Server reachable. BlueBubbles %s on %s\n", info.ServerVersion, info.OSVersion)
	if info.LocalIPv4 != "" {
		fmt.Fprintf(out, "  (server reports its LAN address as %s)\n", info.LocalIPv4)
	}

	// 3. Webhook port. BlueBubbles posts events to talon at this port,
	//    so it has to be reachable from the BlueBubbles host. Default
	//    to 18792; user can override.
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Webhook port (talon will listen here for BlueBubbles events) [%d]: ", defaultBluebubblesWebhookPort)
	line, _ := reader.ReadString('\n')
	webhookPort := defaultBluebubblesWebhookPort
	if s := strings.TrimSpace(line); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("invalid port %q (want 1..65535)", s)
		}
		webhookPort = n
	}

	// 4. Capture the user's iMessage handle. Ask directly — no auto-
	//    capture in V1. Strip leading '@' or '+' decorations only when
	//    the value looks like a phone (don't touch emails).
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Your iMessage handle — the phone number or Apple ID you want talon to talk to.")
	fmt.Fprintln(out, "Examples: +15551234567   user@icloud.com")
	fmt.Fprint(out, "Handle: ")
	line, err = reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read handle: %w", err)
	}
	handle := strings.TrimSpace(line)
	if handle == "" {
		return errors.New("handle is required")
	}

	// 5. Print the webhook URL the user has to paste into the
	//    BlueBubbles admin UI. Without this step nothing inbound works.
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Open BlueBubbles → Server → API & Webhooks and add a webhook:")
	fmt.Fprintf(out, "    http://<this-machine-on-your-LAN>:%d/webhook\n", webhookPort)
	fmt.Fprintln(out, "Tick all event types, save. (You can do this after setup; messages won't reach talon until then.)")

	// 6. Persist. Channel config first so a half-applied wizard
	//    leaves the plugin entry disabled — same shape as telegram.
	paths := resolvePaths()
	storeCtx, storeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer storeCancel()
	passwordRef, err := secrets.StoreKeychainSecret(storeCtx, "talon.channels.bluebubbles.password", password)
	if err != nil {
		return fmt.Errorf("store BlueBubbles password in keychain: %w", err)
	}
	writes := []struct {
		path  []string
		value any
	}{
		{[]string{"channels", "bluebubbles", "serverUrl"}, serverURL},
		{[]string{"channels", "bluebubbles", "password"}, passwordRef},
		{[]string{"channels", "bluebubbles", "webhookPort"}, webhookPort},
		{[]string{"channels", "bluebubbles", "allowFrom"}, []any{handle}},
		{[]string{"channels", "bluebubbles", "dmPolicy"}, "allowlist"},
		{[]string{"channels", "bluebubbles", "agentId"}, "main"},
		{[]string{"plugins", "entries", "bluebubbles"}, map[string]any{
			"enabled": true,
		}},
	}
	for _, w := range writes {
		if _, err := config.Set(paths, w.path, w.value, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
			return fmt.Errorf("config set %s: %w", strings.Join(w.path, "."), err)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "✓ Configuration written. Restart the gateway so the plugin loads:")
	fmt.Fprintln(out, "    make docker-stop && make docker-run")
	return nil
}

// bluebubblesServerInfo is the V1 wizard's connectivity probe. Hits
// GET /api/v1/server/info with the password as a query string; surfaces
// the version + OS so the wizard can echo something the user
// recognizes. Inline (no internal/bluebubbles package yet) because
// it's the only HTTP touchpoint the CLI side needs today; promotes to
// a package when V2 adds verify/captureSender RPCs.
type bluebubblesServerInfoResp struct {
	Status int `json:"status"`
	Data   struct {
		ServerVersion string `json:"server_version"`
		OSVersion     string `json:"os_version"`
		LocalIPv4     string `json:"local_ipv4"`
	} `json:"data"`
	Message string `json:"message"`
}

type bluebubblesInfo struct {
	ServerVersion string
	OSVersion     string
	LocalIPv4     string
}

func bluebubblesServerInfo(ctx context.Context, serverURL, password string) (*bluebubblesInfo, error) {
	endpoint := serverURL + "/api/v1/server/info?password=" + url.QueryEscape(password)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateString(string(body), 200))
	}
	var parsed bluebubblesServerInfoResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Status != 200 && parsed.Status != 0 {
		// BlueBubbles returns {status: 401, message: "..."} on auth failure.
		msg := parsed.Message
		if msg == "" {
			msg = fmt.Sprintf("status=%d", parsed.Status)
		}
		return nil, errors.New(msg)
	}
	return &bluebubblesInfo{
		ServerVersion: parsed.Data.ServerVersion,
		OSVersion:     parsed.Data.OSVersion,
		LocalIPv4:     parsed.Data.LocalIPv4,
	}, nil
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
