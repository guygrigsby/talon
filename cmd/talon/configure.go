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
//
// All HTTP is stdlib — no Bot API SDK dep, ~250 LOC.

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
	bot, err := telegramGetMe(ctx, token)
	if err != nil {
		return fmt.Errorf("verify token via getMe: %w", err)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "✓ Token verified. Bot: @%s (%s)\n", bot.Username, bot.FirstName)
	fmt.Fprintln(out)

	// 3. Capture the user's chat. Drain any prior unread updates so
	//    we don't pick up a stale message; then ask the user to
	//    DM the bot now and long-poll for the new one.
	startOffset, err := telegramDrainUpdates(ctx, token)
	if err != nil {
		return fmt.Errorf("drain prior updates: %w", err)
	}
	fmt.Fprintf(out, "Open Telegram and message @%s. Send /start (or any text).\n", bot.Username)
	fmt.Fprintln(out, "(Press Ctrl-C to abort.)")
	fmt.Fprint(out, "Waiting for your message...")

	chat, err := telegramWaitForMessage(ctx, token, startOffset, 90*time.Second)
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
	if err := telegramSendMessage(ctx, token, chat.ChatID, confirm); err != nil {
		// Non-fatal — the config write is still valuable. Log and continue.
		fmt.Fprintf(out, "warn: send confirmation: %v\n", err)
	}

	// 5. Persist. Order: channel config, then plugins.entries, so a
	//    half-applied wizard leaves talon refusing to spawn the
	//    plugin rather than running it without a token.
	paths := resolvePaths()
	senderIDStr := strconv.FormatInt(chat.SenderID, 10)
	chatIDStr := strconv.FormatInt(chat.ChatID, 10)
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
	_ = chatIDStr // reserved for future per-chat config

	fmt.Fprintln(out)
	fmt.Fprintln(out, "✓ Configuration written. Restart the gateway so the plugin loads:")
	fmt.Fprintln(out, "    make docker-stop && make docker-run")
	return nil
}

// --- Telegram Bot API helpers ---------------------------------------------

const telegramAPIBase = "https://api.telegram.org"

type telegramBotInfo struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type telegramSenderID struct {
	ChatID      int64
	SenderID    int64
	DisplayName string
}

type tgUpdate struct {
	UpdateID      int64    `json:"update_id"`
	Message       *tgMsg   `json:"message"`
	EditedMessage *tgMsg   `json:"edited_message"`
}

type tgMsg struct {
	MessageID int64   `json:"message_id"`
	Date      int64   `json:"date"`
	Chat      tgChat  `json:"chat"`
	From      *tgUser `json:"from"`
	Text      string  `json:"text"`
}

type tgChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type tgGetUpdatesResp struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

type tgGetMeResp struct {
	OK     bool             `json:"ok"`
	Result *telegramBotInfo `json:"result"`
}

func telegramGetMe(ctx context.Context, token string) (*telegramBotInfo, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getMe", telegramAPIBase, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getMe http %d: %s", resp.StatusCode, truncateForErr(string(raw), 256))
	}
	var out tgGetMeResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("getMe decode: %w", err)
	}
	if !out.OK || out.Result == nil {
		return nil, errors.New("getMe ok=false")
	}
	return out.Result, nil
}

// telegramDrainUpdates returns the next offset to use for getUpdates so
// any pre-existing pending updates (from earlier unrelated chats) are
// skipped. We pull once with timeout=0 (immediate return) and take
// the highest update_id + 1.
func telegramDrainUpdates(ctx context.Context, token string) (int64, error) {
	updates, err := telegramGetUpdates(ctx, token, 0, 0)
	if err != nil {
		return 0, err
	}
	var maxID int64
	for _, u := range updates {
		if u.UpdateID > maxID {
			maxID = u.UpdateID
		}
	}
	if maxID == 0 {
		return 0, nil
	}
	return maxID + 1, nil
}

// telegramWaitForMessage blocks until either a new (post-offset)
// message arrives or deadline expires. Uses successive long-poll
// calls with timeout=30s; deadline caps the total wait.
func telegramWaitForMessage(ctx context.Context, token string, offset int64, deadline time.Duration) (telegramSenderID, error) {
	end := time.Now().Add(deadline)
	for {
		if ctx.Err() != nil {
			return telegramSenderID{}, ctx.Err()
		}
		if time.Now().After(end) {
			return telegramSenderID{}, errors.New("timed out waiting for a Telegram message")
		}
		updates, err := telegramGetUpdates(ctx, token, offset, 30)
		if err != nil {
			return telegramSenderID{}, err
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			m := u.Message
			if m == nil {
				m = u.EditedMessage
			}
			if m == nil || m.From == nil {
				continue
			}
			display := m.From.FirstName
			if display == "" {
				display = m.From.Username
			}
			return telegramSenderID{
				ChatID:      m.Chat.ID,
				SenderID:    m.From.ID,
				DisplayName: display,
			}, nil
		}
	}
}

func telegramGetUpdates(ctx context.Context, token string, offset int64, timeoutSec int) ([]tgUpdate, error) {
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(timeoutSec))
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", telegramAPIBase, token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getUpdates http %d: %s", resp.StatusCode, truncateForErr(string(raw), 256))
	}
	var out tgGetUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("getUpdates decode: %w", err)
	}
	if !out.OK {
		return nil, errors.New("getUpdates ok=false")
	}
	return out.Result, nil
}

func telegramSendMessage(ctx context.Context, token string, chatID int64, text string) error {
	q := url.Values{}
	q.Set("chat_id", strconv.FormatInt(chatID, 10))
	q.Set("text", text)
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage?%s", telegramAPIBase, token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage http %d: %s", resp.StatusCode, truncateForErr(string(raw), 256))
	}
	return nil
}

func truncateForErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
