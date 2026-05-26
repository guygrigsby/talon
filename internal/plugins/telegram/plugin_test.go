package telegram

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestSanitizeTelegramErrorRedactsBotToken(t *testing.T) {
	const token = "123456:secret-token"
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot" + token + "/getUpdates?timeout=30",
		Err: timeoutErr{},
	}
	got := sanitizeTelegramError(err)
	if strings.Contains(got, token) {
		t.Fatalf("sanitized error leaked token: %q", got)
	}
	if !strings.Contains(got, "/bot<redacted>/getUpdates") {
		t.Fatalf("sanitized error did not preserve useful URL shape: %q", got)
	}
}

func TestSanitizeTelegramURLErrorPreservesTimeoutClassification(t *testing.T) {
	err := sanitizeTelegramURLError(&url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot123456:secret-token/getUpdates?timeout=30",
		Err: timeoutErr{},
	})
	if !isTransientTelegramPollErr(err) {
		t.Fatalf("sanitized timeout should still classify as transient: %v", err)
	}
}

func TestShouldLogTelegramPollWarnSuppressesTransientRepeats(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot123456:secret-token/getUpdates?timeout=30",
		Err: timeoutErr{},
	}
	now := time.Unix(100, 0)
	if !shouldLogTelegramPollWarn(err, 1, time.Time{}, now) {
		t.Fatal("first transient error should warn")
	}
	if shouldLogTelegramPollWarn(err, 2, now, now.Add(time.Minute)) {
		t.Fatal("transient repeat before interval should be suppressed")
	}
	if !shouldLogTelegramPollWarn(err, 2, now, now.Add(transientPollWarnInterval+time.Second)) {
		t.Fatal("transient repeat after interval should warn")
	}
	if !shouldLogTelegramPollWarn(errors.New("decode failed"), 10, now, now.Add(time.Minute)) {
		t.Fatal("non-transient error should warn every time")
	}
}

func TestTelegramHTTPErrorPermanentAuth(t *testing.T) {
	if !isPermanentAuthErr(&telegramHTTPError{Method: "getUpdates", Status: 401}) {
		t.Fatal("401 should be permanent")
	}
	if !isPermanentAuthErr(&telegramHTTPError{Method: "getMe", Status: 404}) {
		t.Fatal("404 should be permanent")
	}
	if isPermanentAuthErr(&telegramHTTPError{Method: "getUpdates", Status: 500}) {
		t.Fatal("500 should not be permanent")
	}
	if !isTransientTelegramPollErr(&telegramHTTPError{Method: "getUpdates", Status: 500}) {
		t.Fatal("500 should be transient")
	}
}
