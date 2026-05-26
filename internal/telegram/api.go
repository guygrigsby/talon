// Package telegram is a minimal Bot API client used by the CLI configure
// wizard and the channels.telegram.* RPCs. Just enough surface to verify
// a token (getMe), drain pending updates, long-poll for the first user
// message, and send a confirmation back. No third-party Bot API SDK
// dependency — stdlib net/http is fine for this footprint.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIBase is the base URL for Bot API requests. var (not const) so
// tests can swap in an httptest.Server URL via SetAPIBase. Production
// callers should not mutate it.
var APIBase = "https://api.telegram.org"

// SetAPIBase overrides APIBase for the duration of the returned
// restore func. Intended for tests:
//
//	defer telegram.SetAPIBase(server.URL)()
func SetAPIBase(u string) func() {
	prev := APIBase
	APIBase = u
	return func() { APIBase = prev }
}

// BotInfo is the subset of getMe.result the wizard cares about.
type BotInfo struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// SenderID identifies the Telegram user (and chat) that DM'd the bot.
// SenderID is the user.id (used for allowFrom); ChatID is the chat the
// reply must be sent to (typically the same number for a 1:1 DM but
// nominally distinct).
type SenderID struct {
	ChatID      int64
	SenderID    int64
	DisplayName string
}

type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message"`
	EditedMessage *Message `json:"edited_message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from"`
	Text      string `json:"text"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type getUpdatesResp struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type getMeResp struct {
	OK     bool     `json:"ok"`
	Result *BotInfo `json:"result"`
}

// GetMe verifies the token and returns the bot's identity.
func GetMe(ctx context.Context, token string) (*BotInfo, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getMe", APIBase, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, sanitizeBotAPIError(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, sanitizeBotAPIError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getMe http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out getMeResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("getMe decode: %w", err)
	}
	if !out.OK || out.Result == nil {
		return nil, errors.New("getMe ok=false")
	}
	return out.Result, nil
}

// DrainUpdates returns the next offset to use for getUpdates so any
// pre-existing pending updates (from earlier unrelated chats) are
// skipped. Pulls once with timeout=0 (immediate return) and takes the
// highest update_id + 1.
func DrainUpdates(ctx context.Context, token string) (int64, error) {
	updates, err := GetUpdates(ctx, token, 0, 0)
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

// WaitForMessage blocks until either a new (post-offset) message
// arrives or deadline expires. Uses successive long-poll calls with
// timeout=30s; deadline caps the total wait.
func WaitForMessage(ctx context.Context, token string, offset int64, deadline time.Duration) (SenderID, error) {
	end := time.Now().Add(deadline)
	for {
		if ctx.Err() != nil {
			return SenderID{}, ctx.Err()
		}
		if time.Now().After(end) {
			return SenderID{}, errors.New("timed out waiting for a Telegram message")
		}
		updates, err := GetUpdates(ctx, token, offset, 30)
		if err != nil {
			return SenderID{}, err
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
			return SenderID{
				ChatID:      m.Chat.ID,
				SenderID:    m.From.ID,
				DisplayName: display,
			}, nil
		}
	}
}

func GetUpdates(ctx context.Context, token string, offset int64, timeoutSec int) ([]Update, error) {
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(timeoutSec))
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", APIBase, token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, sanitizeBotAPIError(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, sanitizeBotAPIError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getUpdates http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out getUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("getUpdates decode: %w", err)
	}
	if !out.OK {
		return nil, errors.New("getUpdates ok=false")
	}
	return out.Result, nil
}

// SendMessage POSTs sendMessage with the given chat_id and text. No
// parse_mode is set by default — callers that need Markdown should use
// SendMarkdown.
func SendMessage(ctx context.Context, token string, chatID int64, text string) error {
	return sendMessage(ctx, token, chatID, text, "")
}

// SendMarkdown sends with parse_mode=Markdown. Useful for the
// confirmation message ("✓ talon configured for @bot").
func SendMarkdown(ctx context.Context, token string, chatID int64, text string) error {
	return sendMessage(ctx, token, chatID, text, "Markdown")
}

func sendMessage(ctx context.Context, token string, chatID int64, text, parseMode string) error {
	q := url.Values{}
	q.Set("chat_id", strconv.FormatInt(chatID, 10))
	q.Set("text", text)
	if parseMode != "" {
		q.Set("parse_mode", parseMode)
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage?%s", APIBase, token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return sanitizeBotAPIError(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return sanitizeBotAPIError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sendMessage http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type safeBotAPIError struct {
	err error
	msg string
}

func (e *safeBotAPIError) Error() string { return e.msg }
func (e *safeBotAPIError) Unwrap() error { return e.err }

func sanitizeBotAPIError(err error) error {
	if err == nil {
		return nil
	}
	return &safeBotAPIError{
		err: err,
		msg: redactBotToken(err.Error()),
	}
}

func redactBotToken(s string) string {
	const marker = "/bot"
	var out strings.Builder
	for {
		idx := strings.Index(s, marker)
		if idx < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:idx+len(marker)])
		rest := s[idx+len(marker):]
		if rest == "" {
			return out.String()
		}
		end := strings.IndexAny(rest, `/?"& `)
		if end < 0 {
			out.WriteString("<redacted>")
			return out.String()
		}
		if end > 0 {
			out.WriteString("<redacted>")
		}
		s = rest[end:]
	}
}
