// Package main — `talon configure channel <name>`. Interactive wizards
// per channel. Today only telegram is wired; the structure mirrors
// openclaw's setup-surface so additional channels (slack, discord)
// drop into the same shape.
//
// Telegram flow (mirrors openclaw's telegramSetupWizard):
//   1. Prompt for the bot token (TELEGRAM_BOT_TOKEN env var also accepted)
//   2. Verify via /getMe — print the bot's @username so the user knows
//      which bot to message
//   3. Prompt: "DM @<bot> with /start (or anything), then press Enter"
//   4. Long-poll /getUpdates until a message lands; capture from.id
//   5. Send a confirmation message back via /sendMessage
//   6. Persist:
//        channels.telegram.botToken   = <token>
//        channels.telegram.allowFrom  = ["<from.id>"]
//        channels.telegram.dmPolicy   = "allowlist"
//        plugins.entries.telegram     = {enabled, cmd}
//   7. Tell the user to restart the gateway so the plugin spawns

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
	"github.com/guygrigsby/talon/internal/telegram"
	"github.com/spf13/cobra"
)

func configureCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "configure",
		Short: "Interactive setup wizards (channels, providers, etc.)",
	}
	c.AddCommand(configureChannelCmd())
	return c
}

func configureChannelCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "channel <name>",
		Short: "Configure a channel by name (telegram supported today)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch strings.ToLower(args[0]) {
			case "telegram":
				return configureTelegram(os.Stdin, os.Stdout)
			default:
				return fmt.Errorf("configure channel: %q is not yet supported (try: telegram)", args[0])
			}
		},
	}
	return c
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

	// 1. Token. Prefer env, fall back to prompt. Mirrors openclaw's
	//    "TELEGRAM_BOT_TOKEN detected" behavior.
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
		return fmt.Errorf("wait for first message: %w", err)
	}
	fmt.Fprintln(out, " got it.")
	fmt.Fprintf(out, "✓ Captured sender id %d (%s)\n", chat.SenderID, chat.DisplayName)

	// 4. Send the confirmation message — the "openclaw sends me a
	//    telegram" moment. Establishes that the round-trip works
	//    AND closes the loop the user expects.
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
	writes := []struct {
		path  []string
		value any
	}{
		{[]string{"channels", "telegram", "botToken"}, token},
		{[]string{"channels", "telegram", "allowFrom"}, []any{senderIDStr}},
		{[]string{"channels", "telegram", "dmPolicy"}, "allowlist"},
		{[]string{"channels", "telegram", "agentId"}, "main"},
		{[]string{"plugins", "entries", "telegram"}, map[string]any{
			"enabled": true,
			"cmd":     []any{"/usr/local/bin/talon-telegram-plugin"},
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
