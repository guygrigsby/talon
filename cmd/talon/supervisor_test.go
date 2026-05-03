package main

import (
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/openclaw"
)

func mkEvents(now time.Time, ages ...time.Duration) []gatewayEvent {
	out := make([]gatewayEvent, 0, len(ages))
	for _, age := range ages {
		out = append(out, gatewayEvent{
			Ts:   now.Add(-age).UnixMilli(),
			Kind: "start",
		})
	}
	return out
}

func TestCrashLoopDetected_BelowThreshold(t *testing.T) {
	now := time.Now()
	// Two starts in 5 min — under threshold (3).
	events := mkEvents(now, 1*time.Minute, 30*time.Second)
	if crashLoopDetected(events, now) {
		t.Errorf("two starts should not trip threshold")
	}
}

func TestCrashLoopDetected_AtThreshold(t *testing.T) {
	now := time.Now()
	// Three starts in 5 min — trips threshold.
	events := mkEvents(now, 4*time.Minute, 2*time.Minute, 10*time.Second)
	if !crashLoopDetected(events, now) {
		t.Errorf("three starts within window should trip threshold")
	}
}

func TestCrashLoopDetected_OutsideWindow(t *testing.T) {
	now := time.Now()
	// Three starts but only one is recent enough — older two are
	// outside the 5-min window.
	events := mkEvents(now, 30*time.Minute, 20*time.Minute, 10*time.Second)
	if crashLoopDetected(events, now) {
		t.Errorf("starts outside window should not contribute to count")
	}
}

func TestCrashLoopDetected_IgnoresAlertSentEvents(t *testing.T) {
	now := time.Now()
	events := []gatewayEvent{
		{Ts: now.Add(-30 * time.Second).UnixMilli(), Kind: "start"},
		{Ts: now.Add(-20 * time.Second).UnixMilli(), Kind: "alert-sent"},
		{Ts: now.Add(-10 * time.Second).UnixMilli(), Kind: "alert-sent"},
		{Ts: now.UnixMilli(), Kind: "start"},
	}
	// Only 2 start events in window — alert-sent shouldn't be
	// double-counted as starts.
	if crashLoopDetected(events, now) {
		t.Errorf("alert-sent events should not count toward crash-loop threshold")
	}
}

func TestRecentlyAlerted_True(t *testing.T) {
	now := time.Now()
	events := []gatewayEvent{
		{Ts: now.Add(-2 * time.Minute).UnixMilli(), Kind: "start"},
		{Ts: now.Add(-1 * time.Minute).UnixMilli(), Kind: "alert-sent"},
		{Ts: now.UnixMilli(), Kind: "start"},
	}
	if !recentlyAlerted(events, now) {
		t.Errorf("alert-sent within cooldown should be recently-alerted")
	}
}

func TestRecentlyAlerted_FalseAfterCooldown(t *testing.T) {
	now := time.Now()
	events := []gatewayEvent{
		{Ts: now.Add(-2 * time.Hour).UnixMilli(), Kind: "alert-sent"},
		{Ts: now.UnixMilli(), Kind: "start"},
	}
	if recentlyAlerted(events, now) {
		t.Errorf("alert-sent outside cooldown should NOT be recently-alerted")
	}
}

func TestAppendAndTailGatewayEvents(t *testing.T) {
	dir := t.TempDir()
	paths := openclaw.Paths{Talon: openclaw.Layer{Dir: dir}}
	// Two events.
	for _, kind := range []string{"start", "start"} {
		if err := appendGatewayEvent(paths, gatewayEvent{
			Ts: time.Now().UnixMilli(), Kind: kind,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := tailGatewayEvents(paths, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d (%+v)", len(got), got)
	}
	for _, ev := range got {
		if ev.Kind != "start" {
			t.Errorf("expected kind=start, got %q", ev.Kind)
		}
	}
}

func TestTailGatewayEvents_MissingFileReturnsError(t *testing.T) {
	paths := openclaw.Paths{Talon: openclaw.Layer{Dir: t.TempDir()}}
	if _, err := tailGatewayEvents(paths, 10); err == nil {
		t.Errorf("expected an error for missing events file (caller treats as 'no history')")
	}
}
