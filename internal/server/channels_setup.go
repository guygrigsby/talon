// Package server — channels.telegram.* setup RPCs.
//
// Mirrors the CLI `talon configure channel telegram` wizard so the UI
// can drive an identical flow without shelling out. Three plain
// (non-streaming) RPCs map to the wizard's three blocking steps:
//
//   1. channels.telegram.verify({token})
//        → calls getMe; returns {ok, bot:{id, username, firstName}}.
//   2. channels.telegram.captureSender({token, deadlineSec?})
//        → drains pending updates, then long-polls until the user DMs
//          the bot or deadline expires. Returns {chatId, senderId,
//          displayName}. Default deadline is 90s; max 300s.
//   3. channels.telegram.persist({token, senderId, agentId?})
//        → writes channels.telegram.* + plugins.entries.telegram into
//          the talon overlay; sends the confirmation DM. Returns {ok,
//          restartHint}.
//
// Splitting into three RPCs (vs. one streaming RPC) keeps the UI
// simple — each call has a tidy response and the UI renders progress
// between steps. captureSender is the only long-running call (≤ 5
// min); WS handles long requests fine.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/guygrigsby/talon/internal/telegram"
)

// ChannelsSetupHandler wires the channels.telegram.* RPCs.
//
// pluginCmd is the absolute path the wizard writes into
// plugins.entries.telegram.cmd. Defaults to the bundled binary path
// inside the docker image. Override via WithPluginCmd in tests.
type ChannelsSetupHandler struct {
	paths     openclaw.Paths
	pluginCmd []string
}

func NewChannelsSetupHandler(paths openclaw.Paths) *ChannelsSetupHandler {
	return &ChannelsSetupHandler{
		paths:     paths,
		pluginCmd: []string{"/usr/local/bin/talon-telegram-plugin"},
	}
}

// WithPluginCmd overrides the cmd[] argv written to plugins.entries.telegram.
// Used by tests to avoid leaving production paths in fixtures.
func (h *ChannelsSetupHandler) WithPluginCmd(cmd []string) *ChannelsSetupHandler {
	h.pluginCmd = append([]string(nil), cmd...)
	return h
}

func (h *ChannelsSetupHandler) Register(r *Registry) {
	r.Register("channels.telegram.verify", h.handleVerify)
	r.Register("channels.telegram.captureSender", h.handleCaptureSender)
	r.Register("channels.telegram.persist", h.handlePersist)
}

// --- channels.telegram.verify ---------------------------------------------

type telegramVerifyParams struct {
	Token string `json:"token"`
}

type telegramBotResp struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"firstName"`
}

func (h *ChannelsSetupHandler) handleVerify(ctx context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p telegramVerifyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.verify: " + err.Error()}
	}
	if strings.TrimSpace(p.Token) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.verify: token is required"}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	bot, err := telegram.GetMe(verifyCtx, p.Token)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.verify: " + err.Error()}
	}
	return map[string]any{
		"ok": true,
		"bot": telegramBotResp{
			ID:        bot.ID,
			Username:  bot.Username,
			FirstName: bot.FirstName,
		},
	}, nil
}

// --- channels.telegram.captureSender --------------------------------------

type telegramCaptureParams struct {
	Token       string `json:"token"`
	DeadlineSec int    `json:"deadlineSec,omitempty"`
}

func (h *ChannelsSetupHandler) handleCaptureSender(ctx context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p telegramCaptureParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.captureSender: " + err.Error()}
	}
	if strings.TrimSpace(p.Token) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.captureSender: token is required"}
	}
	deadline := time.Duration(p.DeadlineSec) * time.Second
	if deadline <= 0 {
		deadline = 90 * time.Second
	}
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}

	// Drain any pre-existing updates so we don't pick up a stale
	// message from an earlier wizard run.
	drainCtx, drainCancel := context.WithTimeout(ctx, 10*time.Second)
	defer drainCancel()
	startOffset, err := telegram.DrainUpdates(drainCtx, p.Token)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "channels.telegram.captureSender: drain: " + err.Error()}
	}

	// Add a small buffer so the inner deadline (the wait loop's clock)
	// expires before the parent ctx — the loop returns its own
	// "timed out waiting" error rather than ctx canceled, which is
	// nicer to surface.
	waitCtx, waitCancel := context.WithTimeout(ctx, deadline+5*time.Second)
	defer waitCancel()
	sender, err := telegram.WaitForMessage(waitCtx, p.Token, startOffset, deadline)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.captureSender: " + err.Error()}
	}
	return map[string]any{
		"chatId":      sender.ChatID,
		"senderId":    sender.SenderID,
		"displayName": sender.DisplayName,
	}, nil
}

// --- channels.telegram.persist --------------------------------------------

type telegramPersistParams struct {
	Token    string `json:"token"`
	SenderID int64  `json:"senderId"`
	ChatID   int64  `json:"chatId,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
}

func (h *ChannelsSetupHandler) handlePersist(ctx context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p telegramPersistParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.persist: " + err.Error()}
	}
	if strings.TrimSpace(p.Token) == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.persist: token is required"}
	}
	if p.SenderID == 0 {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "channels.telegram.persist: senderId is required"}
	}
	agentID := strings.TrimSpace(p.AgentID)
	if agentID == "" {
		agentID = "main"
	}

	senderIDStr := strconv.FormatInt(p.SenderID, 10)
	writes := []struct {
		path  []string
		value any
	}{
		{[]string{"channels", "telegram", "botToken"}, p.Token},
		{[]string{"channels", "telegram", "allowFrom"}, []any{senderIDStr}},
		{[]string{"channels", "telegram", "dmPolicy"}, "allowlist"},
		{[]string{"channels", "telegram", "agentId"}, agentID},
		{[]string{"plugins", "entries", "telegram"}, map[string]any{
			"enabled": true,
			"cmd":     toAny(h.pluginCmd),
		}},
	}
	for _, w := range writes {
		if _, err := config.Set(h.paths, w.path, w.value, config.SetOpts{Mode: config.SetReplaceSafe}); err != nil {
			return nil, &FrameError{
				Code:    ErrCodeInternal,
				Message: fmt.Sprintf("channels.telegram.persist: set %s: %v", strings.Join(w.path, "."), err),
			}
		}
	}

	// Best-effort confirmation DM. Send via the captured chatId when
	// supplied (preferred — guarantees delivery to the right chat for
	// non-DM cases) and otherwise to the sender id (1:1 DMs have
	// chat.id == from.id by Telegram's contract).
	chatID := p.ChatID
	if chatID == 0 {
		chatID = p.SenderID
	}
	confirmCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	confirmErr := telegram.SendMessage(confirmCtx, p.Token,
		chatID,
		"✓ talon configured. Future replies in this chat are routed through your agent.")

	resp := map[string]any{
		"ok":          true,
		"restartHint": "Restart the gateway so the plugin loads (make docker-stop && make docker-run, or send SIGHUP if running outside docker).",
	}
	if confirmErr != nil {
		resp["confirmWarning"] = confirmErr.Error()
	}
	return resp, nil
}

// toAny widens a []string into []any so config.Set's JSON-friendly value
// argument doesn't reject it as a typed slice.
func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
