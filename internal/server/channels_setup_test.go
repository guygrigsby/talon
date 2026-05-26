package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/telegram"
	"github.com/tidwall/gjson"
)

// fakeTelegramServer is a minimal Bot API stub used by every test in
// this file. It records the most recent sendMessage call so tests can
// assert on the confirmation DM path.
type fakeTelegramServer struct {
	httpsrv *httptest.Server
	// Per-endpoint behavior: nil means "200 with sane defaults". Set
	// to override for error-path tests.
	getMeResp        []byte
	getMeStatus      int
	updates          []byte // pre-canned getUpdates response (success path)
	sendMessageError int    // non-zero means /sendMessage replies with this status

	lastSend url.Values
}

func newFakeTelegramServer(t *testing.T) *fakeTelegramServer {
	t.Helper()
	srv := &fakeTelegramServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Path is /bot<token>/<method>. Match on method suffix.
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			if srv.getMeStatus != 0 {
				w.WriteHeader(srv.getMeStatus)
				return
			}
			body := srv.getMeResp
			if body == nil {
				body = []byte(`{"ok":true,"result":{"id":1234,"username":"talon_test_bot","first_name":"TalonTest"}}`)
			}
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			body := srv.updates
			if body == nil {
				// Default: zero updates (drain returns empty; capture
				// blocks until deadline).
				body = []byte(`{"ok":true,"result":[]}`)
			}
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if err := r.ParseForm(); err == nil {
				srv.lastSend = r.Form
			}
			if srv.sendMessageError != 0 {
				w.WriteHeader(srv.sendMessageError)
				_, _ = w.Write([]byte(`{"ok":false,"description":"forced"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv.httpsrv = httptest.NewServer(mux)
	t.Cleanup(srv.httpsrv.Close)
	t.Cleanup(telegram.SetAPIBase(srv.httpsrv.URL))
	return srv
}

func fakeSecretStore(_ context.Context, target, _ string) (string, error) {
	return "keychain://" + target + "/talon", nil
}

func TestChannelsTelegramVerify_OK(t *testing.T) {
	newFakeTelegramServer(t)
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths)

	res, ferr := h.handleVerify(context.Background(), HandlerCtx{}, mustJSON(t, map[string]string{"token": "abc"}))
	if ferr != nil {
		t.Fatalf("verify: %+v", ferr)
	}
	got := res.(map[string]any)
	if got["ok"] != true {
		t.Fatalf("ok=true expected, got %v", got["ok"])
	}
	bot := got["bot"].(telegramBotResp)
	if bot.Username != "talon_test_bot" {
		t.Fatalf("unexpected bot username: %q", bot.Username)
	}
}

func TestChannelsTelegramVerify_RejectsEmptyToken(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths)
	_, ferr := h.handleVerify(context.Background(), HandlerCtx{}, mustJSON(t, map[string]string{"token": ""}))
	if ferr == nil {
		t.Fatal("expected error for empty token")
	}
	if ferr.Code != ErrCodeBadRequest {
		t.Fatalf("expected BadRequest, got %s", ferr.Code)
	}
}

func TestChannelsTelegramVerify_BadTokenSurfacesError(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.getMeStatus = http.StatusUnauthorized
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths)

	_, ferr := h.handleVerify(context.Background(), HandlerCtx{}, mustJSON(t, map[string]string{"token": "bad"}))
	if ferr == nil {
		t.Fatal("expected error for unauthorized token")
	}
}

func TestChannelsTelegramCaptureSender_OK(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.updates = []byte(`{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"date":0,"chat":{"id":555,"type":"private"},"from":{"id":555,"first_name":"Guy","username":"guyg"},"text":"/start"}}]}`)

	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths)

	res, ferr := h.handleCaptureSender(context.Background(), HandlerCtx{}, mustJSON(t, map[string]any{"token": "abc", "deadlineSec": 5}))
	if ferr != nil {
		t.Fatalf("captureSender: %+v", ferr)
	}
	got := res.(map[string]any)
	// JSON numbers come back as int64 in our typed return; preserve type checks.
	if got["senderId"].(int64) != 555 {
		t.Fatalf("senderId=555 expected, got %v", got["senderId"])
	}
	if got["chatId"].(int64) != 555 {
		t.Fatalf("chatId=555 expected, got %v", got["chatId"])
	}
	if got["displayName"] != "Guy" {
		t.Fatalf("displayName=Guy expected, got %v", got["displayName"])
	}
}

func TestChannelsTelegramPersist_WritesConfigAndConfirms(t *testing.T) {
	srv := newFakeTelegramServer(t)
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths).
		WithPluginCmd([]string{"/usr/bin/telegram-plugin-test"}).
		WithSecretStore(fakeSecretStore)

	params := mustJSON(t, map[string]any{
		"token":    "TKN",
		"senderId": 555,
		"chatId":   555,
		"agentId":  "main",
	})
	res, ferr := h.handlePersist(context.Background(), HandlerCtx{}, params)
	if ferr != nil {
		t.Fatalf("persist: %+v", ferr)
	}
	got := res.(map[string]any)
	if got["ok"] != true {
		t.Fatalf("ok=true expected, got %v", got)
	}
	if _, hasWarn := got["confirmWarning"]; hasWarn {
		t.Errorf("happy path should not emit confirmWarning, got: %v", got["confirmWarning"])
	}

	// Verify the config landed in the runtime view the gateway uses.
	raw, err := config.MergedBytes(paths)
	if err != nil {
		t.Fatalf("read merged config: %v", err)
	}
	if got := gjson.GetBytes(raw, "channels.telegram.botToken").Str; got != "keychain://talon.channels.telegram.botToken/talon" {
		t.Errorf("botToken not persisted: %q", got)
	}
	if got := gjson.GetBytes(raw, "channels.telegram.allowFrom.0").Str; got != "555" {
		t.Errorf("allowFrom[0]=\"555\" expected, got %q", got)
	}
	if got := gjson.GetBytes(raw, "channels.telegram.dmPolicy").Str; got != "allowlist" {
		t.Errorf("dmPolicy=allowlist expected, got %q", got)
	}
	if got := gjson.GetBytes(raw, "channels.telegram.agentId").Str; got != "main" {
		t.Errorf("agentId=main expected, got %q", got)
	}
	if !gjson.GetBytes(raw, "plugins.entries.telegram.enabled").Bool() {
		t.Errorf("plugins.entries.telegram.enabled should be true: %s", raw)
	}

	// Confirmation DM was sent to the captured chat id.
	if srv.lastSend == nil {
		t.Fatal("expected a sendMessage call (confirmation DM)")
	}
	if got := srv.lastSend.Get("chat_id"); got != "555" {
		t.Errorf("confirmation chat_id=555 expected, got %q", got)
	}
}

func TestChannelsTelegramPersist_DefaultsAgentToMain(t *testing.T) {
	newFakeTelegramServer(t)
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths).WithSecretStore(fakeSecretStore)

	params := mustJSON(t, map[string]any{"token": "TKN", "senderId": 42})
	if _, ferr := h.handlePersist(context.Background(), HandlerCtx{}, params); ferr != nil {
		t.Fatalf("persist: %+v", ferr)
	}
	raw, _ := config.MergedBytes(paths)
	if got := gjson.GetBytes(raw, "channels.telegram.agentId").Str; got != "main" {
		t.Errorf("agentId should default to main, got %q", got)
	}
}

func TestChannelsTelegramPersist_FallsBackChatIDToSender(t *testing.T) {
	srv := newFakeTelegramServer(t)
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths).WithSecretStore(fakeSecretStore)

	// chatId omitted; senderId should be used as the confirmation
	// target. (1:1 DMs have chat.id == from.id.)
	params := mustJSON(t, map[string]any{"token": "TKN", "senderId": 9001})
	if _, ferr := h.handlePersist(context.Background(), HandlerCtx{}, params); ferr != nil {
		t.Fatalf("persist: %+v", ferr)
	}
	if srv.lastSend == nil || srv.lastSend.Get("chat_id") != "9001" {
		t.Fatalf("confirmation chat_id should fall back to senderId, got %v", srv.lastSend)
	}
}

func TestChannelsTelegramPersist_ConfirmationFailureNonFatal(t *testing.T) {
	srv := newFakeTelegramServer(t)
	srv.sendMessageError = http.StatusBadRequest
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths).WithSecretStore(fakeSecretStore)

	params := mustJSON(t, map[string]any{"token": "TKN", "senderId": 1})
	res, ferr := h.handlePersist(context.Background(), HandlerCtx{}, params)
	if ferr != nil {
		t.Fatalf("persist should not error when only confirmation DM fails: %+v", ferr)
	}
	got := res.(map[string]any)
	if got["ok"] != true {
		t.Errorf("ok=true expected even with confirmation failure, got %v", got)
	}
	if _, ok := got["confirmWarning"]; !ok {
		t.Errorf("expected confirmWarning surfaced, got %v", got)
	}
	// Config still written despite the DM error.
	raw, _ := config.MergedBytes(paths)
	if got := gjson.GetBytes(raw, "channels.telegram.botToken").Str; got != "keychain://talon.channels.telegram.botToken/talon" {
		t.Errorf("config write should not be reverted by DM failure: %q", got)
	}
}

func TestChannelsTelegramPersist_RejectsMissingFields(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewChannelsSetupHandler(paths)

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"empty token", map[string]any{"token": "", "senderId": 1}},
		{"zero senderId", map[string]any{"token": "TKN", "senderId": 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ferr := h.handlePersist(context.Background(), HandlerCtx{}, mustJSON(t, c.params))
			if ferr == nil {
				t.Fatalf("expected BadRequest for %s", c.name)
			}
			if ferr.Code != ErrCodeBadRequest {
				t.Fatalf("expected BadRequest, got %s (%s)", ferr.Code, ferr.Message)
			}
		})
	}
}
