// Package bluebubbles implements the BlueBubbles iMessage channel as a talon plugin library.
// The subprocess entrypoint (apps/talon-bluebubbles-plugin/main.go) calls New()
// and pluginrun.Serve() to wire it up.
package bluebubbles

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

// defaultWebhookPath is what the BlueBubbles admin UI gets pointed at
// when the user doesn't override channels.bluebubbles.webhookPath.
// Matches the openclaw JS plugin's CLI flag default.
const defaultWebhookPath = "/webhook"

// defaultSendTimeout caps a single /api/v1/message/text round-trip.
// BlueBubbles can take a few seconds when iMessage's send-via-Apple
// path is slow; honor channels.bluebubbles.sendTimeoutMs override.
const defaultSendTimeout = 30 * time.Second

type bluebubblesPlugin struct {
	pb.UnimplementedPluginServer

	http *http.Client
}

func (s *bluebubblesPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:           "talon-bluebubbles",
			Version:        "0.1.0",
			Description:    "BlueBubbles iMessage channel (Go plugin)",
			OffersChannels: []string{"bluebubbles"},
			OffersTools: []*pb.ToolSpec{{
				Name:        "bluebubbles_send",
				Description: "Send an iMessage via the configured BlueBubbles server. chat_guid is optional — when omitted, the message goes to the default chat captured during channel setup (channels.bluebubbles.allowFrom[0] turned into iMessage;-;<address>). Pass an explicit chat_guid only when targeting a different chat captured in this conversation.",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"chat_guid": {"type": "string", "description": "Optional. BlueBubbles chat GUID, e.g. iMessage;-;+15551234567 for a DM or iMessage;+;<groupGUID> for a group. Omit to send to the default DM."},
						"text":      {"type": "string", "description": "Message body."}
					},
					"required": ["text"],
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

func (s *bluebubblesPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	switch req.GetToolName() {
	case "bluebubbles_send":
		return s.runSend(ctx, req)
	default:
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("bluebubbles plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
}

func (s *bluebubblesPlugin) runSend(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	var args struct {
		ChatGUID string `json:"chat_guid"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return &pb.RunToolResponse{Output: "bluebubbles_send: invalid arguments JSON: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(args.Text) == "" {
		return &pb.RunToolResponse{Output: "bluebubbles_send: text is required", IsError: true}, nil
	}
	if strings.TrimSpace(args.ChatGUID) == "" {
		args.ChatGUID = defaultChatGUIDFromCache()
		if args.ChatGUID == "" {
			return &pb.RunToolResponse{
				Output:  "bluebubbles_send: chat_guid is required (no default configured — set channels.bluebubbles.allowFrom)",
				IsError: true,
			}, nil
		}
	}
	if err := s.postMessage(ctx, args.ChatGUID, args.Text); err != nil {
		return &pb.RunToolResponse{Output: "bluebubbles_send: " + err.Error(), IsError: true}, nil
	}
	return &pb.RunToolResponse{Output: fmt.Sprintf("sent to chat %s", args.ChatGUID)}, nil
}

// channelConfig mirrors the load-bearing subset of the JS plugin's
// channels.bluebubbles.* schema (extensions/bluebubbles/openclaw.
// plugin.json). V1 only reads what it uses today; everything else is
// silently ignored. Adding a feature later means adding its config
// key here.
type channelConfig struct {
	ServerURL      string   `json:"serverUrl"`
	Password       string   `json:"password"`
	WebhookPort    int      `json:"webhookPort"`
	WebhookPath    string   `json:"webhookPath"`
	DMPolicy       string   `json:"dmPolicy"`
	AllowFrom      []string `json:"allowFrom"`
	GroupPolicy    string   `json:"groupPolicy"`
	GroupAllowFrom []string `json:"groupAllowFrom"`
	SendTimeoutMs  int      `json:"sendTimeoutMs"`
}

func (s *bluebubblesPlugin) StartChannel(req *pb.StartChannelRequest, stream pb.Plugin_StartChannelServer) error {
	if req.GetChannelName() != "bluebubbles" {
		return fmt.Errorf("bluebubbles plugin: unknown channel %q", req.GetChannelName())
	}
	var cfg channelConfig
	if len(req.GetChannelConfig()) > 0 {
		if err := json.Unmarshal(req.GetChannelConfig(), &cfg); err != nil {
			return fmt.Errorf("bluebubbles plugin: parse channel_config: %w", err)
		}
	}
	if cfg.ServerURL == "" {
		return fmt.Errorf("bluebubbles plugin: channels.bluebubbles.serverUrl is empty")
	}
	if cfg.Password == "" {
		return fmt.Errorf("bluebubbles plugin: channels.bluebubbles.password is empty")
	}
	if cfg.WebhookPort <= 0 {
		return fmt.Errorf("bluebubbles plugin: channels.bluebubbles.webhookPort is required (must be >0; the BlueBubbles server posts events here)")
	}
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = defaultWebhookPath
	}
	if !strings.HasPrefix(cfg.WebhookPath, "/") {
		cfg.WebhookPath = "/" + cfg.WebhookPath
	}

	// Stash cmd state for SendChannelMessage and the tool dispatcher.
	// Both run outside this RPC and need the same auth + base URL.
	setSendState(strings.TrimRight(cfg.ServerURL, "/"), cfg.Password, sendTimeout(cfg))
	if def := defaultChatGUIDFromAllow(cfg.AllowFrom); def != "" {
		setDefaultChatGUID(def)
	}

	allowDM := buildAllowSet(cfg.DMPolicy, cfg.AllowFrom)
	allowGroup := buildAllowSet(cfg.GroupPolicy, cfg.GroupAllowFrom)

	// Mailbox between the webhook listener and this RPC handler. Big
	// enough to absorb a typical iMessage burst (e.g. group reply
	// stream) without backpressuring the HTTP handler.
	msgs := make(chan *pb.IncomingChannelMessage, 64)
	srvErr := make(chan error, 1)

	listenCtx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	srv := s.startWebhookServer(listenCtx, cfg, allowDM, allowGroup, msgs, srvErr)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	}()

	for {
		select {
		case msg := <-msgs:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case err := <-srvErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case <-stream.Context().Done():
			return nil
		}
	}
}

// startWebhookServer launches the HTTP listener that BlueBubbles posts
// events to. Returns the *http.Server so StartChannel can Shutdown it
// on stream close. Errors land on srvErr (one-shot — buffered).
func (s *bluebubblesPlugin) startWebhookServer(
	ctx context.Context,
	cfg channelConfig,
	allowDM, allowGroup map[string]struct{},
	msgs chan<- *pb.IncomingChannelMessage,
	srvErr chan<- error,
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg := convertWebhookEvent(body, allowDM, allowGroup); msg != nil {
			select {
			case msgs <- msg:
			case <-ctx.Done():
			}
		}
		// BlueBubbles ignores response body but expects 2xx.
		w.WriteHeader(http.StatusNoContent)
	})

	addr := fmt.Sprintf(":%d", cfg.WebhookPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := srv.ListenAndServe()
		select {
		case srvErr <- err:
		default:
		}
	}()
	return srv
}

// SendChannelMessage POSTs to /api/v1/message/text with chatGuid =
// req.RoomID. Inbound roomID was the chat GUID we put on the
// IncomingChannelMessage, so it round-trips here.
func (s *bluebubblesPlugin) SendChannelMessage(ctx context.Context, req *pb.SendChannelMessageRequest) (*pb.SendChannelMessageResponse, error) {
	if req.GetChannel() != "bluebubbles" {
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("bluebubbles plugin: SendChannelMessage on unexpected channel %q", req.GetChannel())
	}
	if req.GetRoomId() == "" || req.GetText() == "" {
		return &pb.SendChannelMessageResponse{Ok: false}, fmt.Errorf("bluebubbles plugin: room_id and text are required")
	}
	if err := s.postMessage(ctx, req.GetRoomId(), req.GetText()); err != nil {
		return &pb.SendChannelMessageResponse{Ok: false}, err
	}
	return &pb.SendChannelMessageResponse{Ok: true}, nil
}

func (s *bluebubblesPlugin) Shutdown(_ context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	go func() { os.Exit(0) }()
	return &pb.ShutdownResponse{}, nil
}

// --- BlueBubbles HTTP send -----------------------------------------------

// postMessage POSTs a single text message to the configured
// BlueBubbles server. method:'private-api' selects the helper-app
// path which supports replies/effects; falls back to apple-script if
// the helper isn't installed (BlueBubbles handles that internally).
func (s *bluebubblesPlugin) postMessage(ctx context.Context, chatGUID, text string) error {
	server, password, timeout, err := getSendState()
	if err != nil {
		return err
	}
	tempGUID, err := newTempGUID()
	if err != nil {
		return fmt.Errorf("temp guid: %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"chatGuid": chatGUID,
		"message":  text,
		"tempGuid": tempGUID,
		"method":   "private-api",
	})
	endpoint := server + "/api/v1/message/text?password=" + url.QueryEscape(password)
	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("BlueBubbles send http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	return nil
}

// --- inbound webhook parsing ---------------------------------------------

// blueBubblesWebhook is the JSON envelope BlueBubbles posts. Only the
// fields V1 reads are typed; the rest are ignored for forward compat.
type blueBubblesWebhook struct {
	Type string             `json:"type"`
	Data blueBubblesMessage `json:"data"`
}

type blueBubblesMessage struct {
	GUID        string             `json:"guid"`
	Text        string             `json:"text"`
	IsFromMe    bool               `json:"isFromMe"`
	DateCreated int64              `json:"dateCreated"` // ms since epoch
	Handle      *blueBubblesHandle `json:"handle"`
	Chats       []blueBubblesChat  `json:"chats"`
	GroupTitle  string             `json:"groupTitle"`
}

type blueBubblesHandle struct {
	Address string `json:"address"`
	Service string `json:"service"`
}

type blueBubblesChat struct {
	GUID    string `json:"guid"`
	IsGroup bool   `json:"isGroup"`
	Style   int    `json:"style"`
}

// convertWebhookEvent decodes a BlueBubbles webhook payload and turns
// the message into a pb.IncomingChannelMessage when the event is an
// inbound new-message that passes the configured allowlists. Returns
// nil for events that should be ignored (echoes, edits in V1,
// non-DM/group sources, denied senders).
func convertWebhookEvent(body []byte, allowDM, allowGroup map[string]struct{}) *pb.IncomingChannelMessage {
	var ev blueBubblesWebhook
	if err := json.Unmarshal(body, &ev); err != nil {
		slog.Warn("bluebubbles webhook decode failed", "plugin", "bluebubbles", "err", err)
		return nil
	}
	if ev.Type != "new-message" {
		// V1 ignores updated-message / typing / read-receipt events;
		// they'd come in here with type values like "updated-message",
		// "chat-read-status-changed" etc.
		return nil
	}
	m := ev.Data
	// Skip echoes of messages the agent itself sent — BlueBubbles
	// also posts those back via webhook.
	if m.IsFromMe {
		return nil
	}
	if strings.TrimSpace(m.Text) == "" {
		return nil
	}
	if len(m.Chats) == 0 {
		return nil
	}
	chat := m.Chats[0]
	senderID := ""
	if m.Handle != nil {
		senderID = m.Handle.Address
	}
	// Allowlist gate. allowDM/allowGroup are derived from
	// dmPolicy/groupPolicy: empty map = open (accept-all),
	// non-empty = strict allowlist on the sender's handle.address.
	if chat.IsGroup {
		if allowGroup != nil {
			if _, ok := allowGroup[senderID]; !ok {
				return nil
			}
		}
	} else {
		if allowDM != nil {
			if _, ok := allowDM[senderID]; !ok {
				return nil
			}
		}
	}
	display := senderID
	if chat.IsGroup && m.GroupTitle != "" {
		display = m.GroupTitle
	}
	return &pb.IncomingChannelMessage{
		Channel:     "bluebubbles",
		SenderId:    senderID,
		DisplayName: display,
		RoomId:      chat.GUID,
		Text:        m.Text,
		TsMs:        m.DateCreated,
	}
}

// buildAllowSet builds the allowlist set used at inbound time. nil
// return = open policy (accept all); non-nil empty map = disabled
// policy (deny all). Matches the JS plugin's dmPolicy / groupPolicy
// semantics: 'allowlist' uses the corresponding allowFrom array;
// 'open' returns nil; 'disabled'/'pairing' returns an empty map so
// nothing slips through until pairing flips it to allowlist.
func buildAllowSet(policy string, list []string) map[string]struct{} {
	switch policy {
	case "open":
		return nil
	case "", "allowlist":
		// Default-when-blank: treat as allowlist. Empty list = no
		// senders allowed, which is the safe default before the
		// wizard captures one.
		set := map[string]struct{}{}
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s != "" {
				set[s] = struct{}{}
			}
		}
		return set
	case "disabled", "pairing":
		// 'pairing' is the JS plugin's "I haven't captured a sender
		// yet, deny all" state. 'disabled' is the explicit off
		// switch. Either way: empty map.
		return map[string]struct{}{}
	default:
		// Unknown policy: be conservative.
		return map[string]struct{}{}
	}
}

// defaultChatGUIDFromAllow turns allowFrom[0] (typically a phone or
// email) into a DM chatGuid for use as the bluebubbles_send default.
// Mirrors the JS plugin's pairing flow where the captured sender
// becomes the default conversation. No service prefix means iMessage
// (the dominant case); SMS-only contacts need the agent to specify
// chat_guid explicitly.
func defaultChatGUIDFromAllow(allow []string) string {
	for _, s := range allow {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		return "iMessage;-;" + s
	}
	return ""
}

func sendTimeout(cfg channelConfig) time.Duration {
	if cfg.SendTimeoutMs > 0 {
		return time.Duration(cfg.SendTimeoutMs) * time.Millisecond
	}
	return defaultSendTimeout
}

// --- per-process send state cache ---------------------------------------

// SendChannelMessage and the bluebubbles_send tool both run outside
// StartChannel and don't see the channel_config. Stash the auth state
// in package-level vars at StartChannel time and read it back here.
// Mirrors the telegram plugin's tokenForSendFromEnv pattern.
var (
	sendStateMu    sync.RWMutex
	sendServer     string
	sendPassword   string
	sendTimeoutVal time.Duration
	defaultChatGUID string
)

func setSendState(server, password string, timeout time.Duration) {
	sendStateMu.Lock()
	sendServer = server
	sendPassword = password
	sendTimeoutVal = timeout
	sendStateMu.Unlock()
}

func getSendState() (server, password string, timeout time.Duration, err error) {
	sendStateMu.RLock()
	defer sendStateMu.RUnlock()
	if sendServer == "" || sendPassword == "" {
		return "", "", 0, fmt.Errorf("send before StartChannel — no auth cached")
	}
	t := sendTimeoutVal
	if t == 0 {
		t = defaultSendTimeout
	}
	return sendServer, sendPassword, t, nil
}

func setDefaultChatGUID(g string) {
	sendStateMu.Lock()
	defaultChatGUID = g
	sendStateMu.Unlock()
}

func defaultChatGUIDFromCache() string {
	sendStateMu.RLock()
	defer sendStateMu.RUnlock()
	return defaultChatGUID
}

// --- helpers -------------------------------------------------------------

// newTempGUID is the optimistic id BlueBubbles uses to dedup. We mint
// a 16-byte hex string per send; collisions are practically zero.
func newTempGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "talon-" + hex.EncodeToString(b[:]), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// New returns a configured BlueBubbles plugin PluginServer.
func New() (pb.PluginServer, error) {
	return &bluebubblesPlugin{http: &http.Client{Timeout: defaultSendTimeout + 10*time.Second}}, nil
}
