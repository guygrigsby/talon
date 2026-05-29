package chatdriver

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/talonpath"
)

// TestHandler_Run_BuildAgentError_LogsError asserts the handler emits
// an ERROR slog record when BuildAgent fails, carrying the agent key,
// and that the record carries no prompt text (no secret payload).
func TestHandler_Run_BuildAgentError_LogsError(t *testing.T) {
	clearProviderEnv(t)

	var rec capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(&capturingHandler{rec: &rec, level: slog.LevelDebug}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Empty config → BuildAgent returns "no model configured".
	h := NewHandler(talonpath.Paths{}, func() ([]byte, error) {
		return []byte(`{}`), nil
	})

	const secretPrompt = "super-secret-prompt-do-not-log"
	_, err := h.Run(context.Background(), RunRequest{
		AgentID: "main",
		Prompt:  secretPrompt,
		Sink:    &recordingSink{},
	})
	if err == nil {
		t.Fatal("expected BuildAgent error")
	}

	if rec.level != slog.LevelError {
		t.Fatalf("expected an ERROR record, got level %v msg %q", rec.level, rec.msg)
	}
	if !strings.Contains(rec.msg, "build-agent failed") {
		t.Fatalf("unexpected log message %q", rec.msg)
	}
	if rec.attrs["agent"] != "main" {
		t.Errorf("expected agent=main attr, got %v", rec.attrs["agent"])
	}
	// Secret payload (the prompt) must never reach the log line.
	if strings.Contains(rec.msg, secretPrompt) {
		t.Error("prompt text leaked into log message")
	}
	for k, v := range rec.attrs {
		if s, ok := v.(string); ok && strings.Contains(s, secretPrompt) {
			t.Errorf("prompt text leaked into attr %q", k)
		}
	}
}

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// capturingHandler records the last record it sees. Single-record
// capture is enough: the handler's first failure is the one we assert.
type capturingHandler struct {
	rec   *capturedRecord
	level slog.Level
}

func (h *capturingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.rec.level = r.Level
	h.rec.msg = r.Message
	h.rec.attrs = map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		h.rec.attrs[a.Key] = a.Value.Any()
		return true
	})
	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }
