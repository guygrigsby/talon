package telegram

import (
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeBotAPIErrorRedactsBotToken(t *testing.T) {
	const token = "123456:secret-token"
	err := sanitizeBotAPIError(&url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot" + token + "/getUpdates?timeout=30",
		Err: errTimeout{},
	})
	got := err.Error()
	if strings.Contains(got, token) {
		t.Fatalf("sanitized error leaked token: %q", got)
	}
	if !strings.Contains(got, "/bot<redacted>/getUpdates") {
		t.Fatalf("sanitized error did not preserve useful URL shape: %q", got)
	}
}

type errTimeout struct{}

func (errTimeout) Error() string   { return "i/o timeout" }
func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }
