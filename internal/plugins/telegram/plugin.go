// Package telegram implements the Telegram bot channel as a talon plugin library.
// The subprocess entrypoint (apps/talon-telegram-plugin/main.go) calls New()
// and pluginrun.Serve() to wire it up.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// telegramAPIBase is the Bot API root. Per-bot URLs prepend
// "/bot<token>/<method>". Override only for tests against a fake
// server (none today).
const telegramAPIBase = "https://api.telegram.org"

// pollTimeout is the long-poll timeout sent to getUpdates. Telegram
// holds the request open for this many seconds when there's no new
// message; >0 means we get woken up immediately on inbound. 30s is
// the standard recommendation in the Bot API docs.
const pollTimeout = 30 * time.Second

type telegramPlugin struct {
	pb.UnimplementedPluginServer

	// http is the shared client all Telegram requests use. Default
	// stdlib client; tests can substitute via the unexported field.
	http *http.Client
}

func (s *telegramPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:           "talon-telegram",
			Version:        "0.1.0",
			Description:    "Telegram bot channel (Go plugin)",
			OffersChannels: []string{"telegram"},
			// telegram_send lets the model proactively message a
			// known chat — opposite direction from the dispatcher's
			// reply-to-sender flow. The plugin owns this tool: native
			// code stays out of channel-specific surfaces.
			OffersTools: []*pb.ToolSpec{{
				Name:        "telegram_send",
				Description: "Send a Telegram message to the user. chat_id is optional — when omitted, the message goes to the configured default chat (channels.telegram.allowFrom[0]), which is the user who set up the channel. Pass an explicit chat_id only when you've captured a different chat id during this conversation.",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"chat_id": {"type": "string", "description": "Optional. Numeric Telegram chat id (or sender id for DMs). Omit to send to the default chat configured during setup."},
						"text":    {"type": "string", "description": "Message body. Markdown supported."}
					},
					"required": ["text"],
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

// RunTool dispatches plugin-offered tools. Today only telegram_send;
// uses the same token cached at StartChannel so calls fail clearly
// when the channel isn't configured (the model sees a "not started"
// message instead of a silent no-op).
func (s *telegramPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	switch req.GetToolName() {
	case "telegram_send":
		return s.runTelegramSend(ctx, req)
	default:
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("telegram plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
}

func (s *telegramPlugin) runTelegramSend(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	var args struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return &pb.RunToolResponse{Output: "telegram_send: invalid arguments JSON: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Text) == "" {
		return &pb.RunToolResponse{Output: "telegram_send: text is required", IsError: true}, nil
	}
	// chat_id is optional: when omitted, fall back to the cached
	// default (channels.telegram.allowFrom[0] from setup). Lets the
	// agent reply to "the user" without remembering numeric ids.
	if strings.TrimSpace(args.ChatID) == "" {
		args.ChatID = defaultChatIDFromCache()
		if args.ChatID == "" {
			return &pb.RunToolResponse{
				Output:  "telegram_send: chat_id is required (no default configured — set channels.telegram.allowFrom)",
				IsError: true,
			}, nil
		}
	}
	token, err := tokenForSendFromEnv()
	if err != nil {
		return &pb.RunToolResponse{
			Output:  "telegram_send: channel not started yet — enable channels.telegram and restart the gateway",
			IsError: true,
		}, nil
	}
	body := url.Values{}
	body.Set("chat_id", args.ChatID)
	body.Set("text", args.Text)
	body.Set("parse_mode", "Markdown")
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return &pb.RunToolResponse{Output: "telegram_send: " + err.Error(), IsError: true}, nil
	}
	httpReq.URL.RawQuery = body.Encode()
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return &pb.RunToolResponse{Output: "telegram_send: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("telegram_send: sendMessage http %d: %s", resp.StatusCode, truncate(string(raw), 256)),
			IsError: true,
		}, nil
	}
	return &pb.RunToolResponse{Output: fmt.Sprintf("sent to chat %s", args.ChatID)}, nil
}

// channelConfig is the per-channel JSON the host passes in
// StartChannelRequest.channel_config. Mirrors the
// channels.telegram.* sub-tree of openclaw's config schema; we only
// pull the fields we use today.
//
// AllowFrom is the openclaw-style allowlist of numeric sender IDs.
// When set, the plugin drops inbound messages whose from.id isn't on
// the list. Empty list = accept-all (matches openclaw's "open"
// dmPolicy). Configured by the configure-wizard during setup.
type channelConfig struct {
	BotToken  string   `json:"botToken"`
	AllowFrom []string `json:"allowFrom"`
	DMPolicy  string   `json:"dmPolicy"` // "allowlist" or "" (open)
}

// StartChannel honors the "first start wins" assumption — the
// dispatcher only invokes one StartChannel per channel name per
// gateway lifetime. We block until ctx cancels (client closes the
// stream) or the long-poll goroutine errors out.
func (s *telegramPlugin) StartChannel(req *pb.StartChannelRequest, stream pb.Plugin_StartChannelServer) error {
	if req.GetChannelName() != "telegram" {
		return fmt.Errorf("telegram plugin: unknown channel %q", req.GetChannelName())
	}
	var cfg channelConfig
	if len(req.GetChannelConfig()) > 0 {
		if err := json.Unmarshal(req.GetChannelConfig(), &cfg); err != nil {
			return fmt.Errorf("telegram plugin: parse channel_config: %w", err)
		}
	}
	if cfg.BotToken == "" {
		return fmt.Errorf("telegram plugin: channels.telegram.botToken is empty — set it in your config before enabling the channel")
	}
	// Cache the token for SendChannelMessage; it runs outside this
	// RPC and needs the same auth.
	setSendToken(cfg.BotToken)
	// Cache allowFrom[0] as the default chat for telegram_send when
	// the agent omits chat_id. In the 1:1 DM the wizard sets up,
	// chat.id == from.id, so the configured sender id doubles as a
	// valid chat id.
	if len(cfg.AllowFrom) > 0 {
		setDefaultChatID(strings.TrimSpace(cfg.AllowFrom[0]))
	}
	// Build the allowlist set once. Empty = accept-all.
	allow := map[string]struct{}{}
	if cfg.DMPolicy == "allowlist" {
		for _, id := range cfg.AllowFrom {
			allow[strings.TrimSpace(id)] = struct{}{}
		}
	}

	// Mailbox between the poll goroutine and this RPC handler.
	// Buffered so a slow stream.Send doesn't backpressure the poll
	// loop into a deadlock when Telegram is bursty.
	msgs := make(chan *pb.IncomingChannelMessage, 32)
	pollErr := make(chan error, 1)

	// pollCtx is what we'll cancel when the gRPC stream context
	// dies — propagates a clean stop into the poll loop so the
	// final getUpdates request bails on close.
	pollCtx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	go func() {
		err := s.pollLoop(pollCtx, cfg.BotToken, allow, msgs)
		// Best-effort signal; the only consumer is the select below.
		select {
		case pollErr <- err:
		default:
		}
	}()

	for {
		select {
		case msg := <-msgs:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case err := <-pollErr:
			if err != nil && err != context.Canceled {
				return err
			}
			return nil
		case <-stream.Context().Done():
			return nil
		}
	}
}

// SendChannelMessage POSTs to /sendMessage with chat_id from
// req.room_id. The dispatcher round-trips room_id verbatim from the
// inbound IncomingChannelMessage, so any value we put there in the
// poll loop comes back in the right form here. We use parse_mode=
// "Markdown" since openclaw's tg extension does the same and the
// agent is markdown-aware by convention.
func (s *telegramPlugin) SendChannelMessage(ctx context.Context, req *pb.SendChannelMessageRequest) (*pb.SendChannelMessageResponse, error) {
	cfg, err := tokenForSendFromEnv()
	if err != nil {
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("telegram plugin: %w", err)
	}
	if req.GetChannel() != "telegram" {
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("telegram plugin: SendChannelMessage on unexpected channel %q", req.GetChannel())
	}
	if req.GetRoomId() == "" || req.GetText() == "" {
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("telegram plugin: room_id and text are required")
	}
	body := url.Values{}
	body.Set("chat_id", req.GetRoomId())
	body.Set("text", req.GetText())
	body.Set("parse_mode", "Markdown")
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, cfg)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return &pb.SendChannelMessageResponse{Ok: false}, err
	}
	httpReq.URL.RawQuery = body.Encode()
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return &pb.SendChannelMessageResponse{Ok: false}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("sendMessage http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	return &pb.SendChannelMessageResponse{Ok: true}, nil
}

func (s *telegramPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

// telegramUpdate is the Bot API "Update" envelope — only the fields
// we care about. We don't claim to handle every update type today
// (channel_post / inline / callback / etc); SCAFFOLD lands message
// + edited_message and ignores the rest. Adding a new type later is
// a new switch arm in convertUpdate.
type telegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *telegramMessage `json:"message"`
	EditedMessage *telegramMessage `json:"edited_message"`
}

type telegramMessage struct {
	MessageID int64         `json:"message_id"`
	Date      int64         `json:"date"`
	Chat      telegramChat  `json:"chat"`
	From      *telegramUser `json:"from"`
	Text      string        `json:"text"`
}

type telegramChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // "private", "group", "supergroup", "channel"
	Title string `json:"title"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type getUpdatesResp struct {
	OK     bool             `json:"ok"`
	Result []telegramUpdate `json:"result"`
}

// pollLoop runs Telegram long-polling until ctx is canceled. Each
// inbound message becomes one IncomingChannelMessage on the channel.
// allow is the optional sender-id allowlist; empty = accept-all
// (openclaw's "open" dmPolicy). Errors from a single getUpdates call
// are logged to stderr and retried with a small backoff — transient
// network blips shouldn't take down the channel.
func (s *telegramPlugin) pollLoop(ctx context.Context, token string, allow map[string]struct{}, out chan<- *pb.IncomingChannelMessage) error {
	// One identity probe up front so the user sees in the log
	// that the bot token actually works (vs. silently long-polling
	// against a 401). getMe is cheap (no offset / state) and
	// surfaces the bot username for clarity.
	if username, err := s.getMe(ctx, token); err != nil {
		slog.Warn("telegram getMe failed — token may be wrong or revoked",
			"plugin", "telegram", "err", err)
	} else {
		slog.Info("telegram polling started",
			"plugin", "telegram", "bot", "@"+username, "allowlist_size", len(allow))
	}

	var offset int64
	backoff := time.Second
	totalReceived := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := s.getUpdates(ctx, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 4xx auth errors (401 Unauthorized = bad/revoked
			// token; 404 Not Found = wrong token shape) won't fix
			// themselves during this process lifetime. Log once
			// with an actionable hint and stop polling rather
			// than spamming the gateway log every 30s forever.
			// The user fixes the token via `talon config set
			// channels.telegram.botToken …` and restarts.
			if isPermanentAuthErr(err) {
				slog.Error("telegram polling stopped — bot token is invalid; fix with `talon config set channels.telegram.botToken <token>` and restart the gateway",
					"plugin", "telegram", "err", err)
				return err
			}
			slog.Warn("telegram getUpdates error", "plugin", "telegram", "err", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			msg := convertUpdate(u)
			if msg == nil {
				continue
			}
			totalReceived++
			if totalReceived == 1 {
				slog.Info("telegram first inbound message",
					"plugin", "telegram", "sender", msg.GetSenderId(), "room", msg.GetRoomId(),
					"text_len", len(msg.GetText()))
			}
			if len(allow) > 0 {
				if _, ok := allow[msg.GetSenderId()]; !ok {
					slog.Info("telegram dropping non-allowlisted message",
						"plugin", "telegram", "sender", msg.GetSenderId())
					continue
				}
			}
			select {
			case out <- msg:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// isPermanentAuthErr reports whether err looks like a Telegram
// auth failure that won't recover by retrying. 401 means the
// token is bad; 404 means the URL is malformed (typically a
// completely wrong token shape). Neither resolves itself during
// this process lifetime — the user has to update config + restart.
func isPermanentAuthErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "http 401") || strings.Contains(msg, "http 404")
}

// getMe pings Telegram's bot-identity endpoint to confirm the
// token works. Used as a startup probe so a wrong/expired token
// surfaces as a clear log line instead of silent long-polling.
func (s *telegramPlugin) getMe(ctx context.Context, token string) (string, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getMe", telegramAPIBase, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("getMe http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("getMe ok=false")
	}
	return out.Result.Username, nil
}

func (s *telegramPlugin) getUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(int(pollTimeout/time.Second)))
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", telegramAPIBase, token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getUpdates http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out getUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("getUpdates ok=false")
	}
	return out.Result, nil
}

func convertUpdate(u telegramUpdate) *pb.IncomingChannelMessage {
	m := u.Message
	if m == nil {
		m = u.EditedMessage
	}
	if m == nil || m.Text == "" {
		return nil
	}
	display := ""
	senderID := ""
	if m.From != nil {
		display = m.From.FirstName
		if display == "" {
			display = m.From.Username
		}
		senderID = strconv.FormatInt(m.From.ID, 10)
	}
	roomID := strconv.FormatInt(m.Chat.ID, 10)
	return &pb.IncomingChannelMessage{
		Channel:     "telegram",
		SenderId:    senderID,
		DisplayName: display,
		RoomId:      roomID,
		Text:        m.Text,
		TsMs:        m.Date * 1000,
	}
}

// tokenForSendFromEnv is a v0 stop-gap: SendChannelMessage runs
// outside the StartChannel request, so it doesn't have direct
// access to the channel_config the host passed in. We stash the
// token in process state at StartChannel time and read it back
// here. Single-process plugin so a package-level var is fine; if
// we ever fork-exec per-call this needs revisiting.
//
// defaultChatID is allowFrom[0] from the channel config — used as a
// fallback when the agent invokes telegram_send without a chat_id.
// In a 1:1 DM (the only scenario the wizard configures), chat.id ==
// from.id, so the user's sender id is also a valid chat id. Empty
// when allowFrom isn't configured (open dmPolicy).
var (
	sendTokenMu   sync.RWMutex
	sendToken     string
	defaultChatID string
)

func tokenForSendFromEnv() (string, error) {
	sendTokenMu.RLock()
	tok := sendToken
	sendTokenMu.RUnlock()
	if tok == "" {
		return "", fmt.Errorf("send before StartChannel — no token cached")
	}
	return tok, nil
}

func setSendToken(tok string) {
	sendTokenMu.Lock()
	sendToken = tok
	sendTokenMu.Unlock()
}

// defaultChatIDFromCache returns the cached fallback chat id (set by
// StartChannel from channels.telegram.allowFrom[0]). Empty string if
// none was configured.
func defaultChatIDFromCache() string {
	sendTokenMu.RLock()
	id := defaultChatID
	sendTokenMu.RUnlock()
	return id
}

func setDefaultChatID(id string) {
	sendTokenMu.Lock()
	defaultChatID = id
	sendTokenMu.Unlock()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// New returns a configured Telegram plugin PluginServer.
func New() (pb.PluginServer, error) {
	return &telegramPlugin{http: &http.Client{Timeout: pollTimeout + 10*time.Second}}, nil
}
