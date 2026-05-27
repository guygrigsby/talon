package log

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestLevelFromEnv(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"DEBUG": slog.LevelDebug,
		" info": slog.LevelInfo,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"WARN":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := LevelFromEnv(in); got != want {
			t.Errorf("LevelFromEnv(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestInit_LevelFiltersDebug(t *testing.T) {
	if LevelFromEnv("debug") != slog.LevelDebug {
		t.Fatal("debug")
	}
	if LevelFromEnv("") != slog.LevelInfo {
		t.Fatal("default info")
	}
	h := newConsoleHandlerLevel(io.Discard, slog.LevelWarn)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be filtered at warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error should pass at warn")
	}
}

// newConsoleHandler keeps working as an Info-level wrapper.
func TestNewConsoleHandler_DefaultsInfo(t *testing.T) {
	h := newConsoleHandler(io.Discard)
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should pass at default")
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be filtered at default info")
	}
}
