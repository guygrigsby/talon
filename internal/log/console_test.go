package log

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newTestHandler returns a console handler writing to a buffer with
// colors forced on. Lets us assert the color codes regardless of
// whether the test process's stderr is a TTY.
func newTestHandler() (*consoleHandler, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := newConsoleHandler(buf)
	h.color = true
	return h, buf
}

func TestConsoleHandler_RendersStructuredPairs(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Info("plugin loaded", "plugin", "telegram", "tools", 1, "providers", 0)
	out := buf.String()
	if !strings.Contains(out, "INFO") {
		t.Errorf("missing level: %q", out)
	}
	if !strings.Contains(out, "plugin loaded") {
		t.Errorf("missing message: %q", out)
	}
	if !strings.Contains(out, "plugin=telegram") {
		t.Errorf("missing structured pair plugin=telegram: %q", out)
	}
	if !strings.Contains(out, "tools=1") {
		t.Errorf("missing tools=1: %q", out)
	}
}

func TestConsoleHandler_ErrorIsRed(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Error("boom", "err", errors.New("bad"))
	out := buf.String()
	if !strings.Contains(out, ansiRed+"ERROR"+ansiReset) {
		t.Errorf("ERROR not red-wrapped: %q", out)
	}
	if !strings.Contains(out, "err=bad") {
		t.Errorf("err pair missing or unrendered: %q", out)
	}
}

func TestConsoleHandler_WarnIsYellow(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Warn("careful")
	out := buf.String()
	if !strings.Contains(out, ansiYellow+"WARN"+ansiReset) {
		t.Errorf("WARN not yellow-wrapped: %q", out)
	}
}

func TestConsoleHandler_InfoIsNotRedOrYellow(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Info("ok")
	out := buf.String()
	if strings.Contains(out, ansiRed) || strings.Contains(out, ansiYellow) {
		t.Errorf("INFO should not use red or yellow: %q", out)
	}
}

func TestConsoleHandler_NoColorWhenDisabled(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newConsoleHandler(buf)
	h.color = false
	logger := slog.New(h)
	logger.Error("boom")
	if strings.Contains(buf.String(), "\033[") {
		t.Errorf("expected no ANSI codes when color disabled; got %q", buf.String())
	}
}

func TestConsoleHandler_NoColorEnvHonored(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	buf := &bytes.Buffer{}
	h := newConsoleHandler(buf)
	if h.color {
		t.Error("NO_COLOR=1 should disable color")
	}
}

func TestConsoleHandler_QuotesValuesWithSpacesOrEquals(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Info("x", "msg", "hello world", "expr", "a=b")
	out := buf.String()
	if !strings.Contains(out, `msg="hello world"`) {
		t.Errorf("expected quoted msg: %q", out)
	}
	if !strings.Contains(out, `expr="a=b"`) {
		t.Errorf("expected quoted expr: %q", out)
	}
}

func TestConsoleHandler_WithAttrsPrefix(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h).With("plugin", "telegram")
	logger.Info("loaded", "tools", 1)
	out := buf.String()
	// Plugin attr should appear once, then tools.
	pi := strings.Index(out, "plugin=telegram")
	ti := strings.Index(out, "tools=1")
	if pi < 0 || ti < 0 {
		t.Fatalf("missing attrs: %q", out)
	}
	if pi > ti {
		t.Errorf("static attrs should appear before per-record: %q", out)
	}
}

func TestConsoleHandler_RendersErrorValueAsString(t *testing.T) {
	h, buf := newTestHandler()
	logger := slog.New(h)
	logger.Info("done", "err", errors.New("oh no"))
	out := buf.String()
	if !strings.Contains(out, `err="oh no"`) {
		t.Errorf("error value should render as quoted message: %q", out)
	}
}

func TestConsoleHandler_TimestampPresent(t *testing.T) {
	h, buf := newTestHandler()
	r := slog.Record{
		Time:    time.Date(2026, 5, 4, 12, 34, 56, int(789*time.Millisecond), time.UTC),
		Level:   slog.LevelInfo,
		Message: "x",
	}
	if err := h.Handle(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "12:34:56.789") {
		t.Errorf("expected timestamp 12:34:56.789 in output: %q", buf.String())
	}
}

func TestParseFormat(t *testing.T) {
	cases := map[string]Format{
		"json":  FormatJSON,
		"JSON":  FormatJSON,
		"text":  FormatText,
		"":      FormatText,
		"weird": FormatText,
	}
	for in, want := range cases {
		if got := ParseFormat(in); got != want {
			t.Errorf("ParseFormat(%q) = %v, want %v", in, got, want)
		}
	}
}
