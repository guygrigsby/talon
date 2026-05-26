package main

// Crash-loop detector + Telegram alerter. Records every gateway
// startup to ~/.talon/logs/gateway-events.jsonl; on each startup
// we tail the recent events and, if we see 3+ starts within 5
// minutes, push a Telegram DM to the configured allowFrom[0]. The
// goal is "I left talon running and came back to a Telegram alert
// that something's wrong" — minimum-viable observability for an
// unattended gateway.
//
// Failure modes are all silent: missing telegram config, missing
// log dir, file write races. None of these should block startup,
// so the call site treats this as fire-and-forget.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/guygrigsby/talon/internal/config"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/guygrigsby/talon/internal/telegram"
	"github.com/tidwall/gjson"
)

const (
	// crashLoopWindow is the look-back interval. Three starts
	// within this window trips the alert. Tuned for "Docker
	// restarted me three times in five minutes" — slow enough
	// that a single intentional restart doesn't trip, fast
	// enough to catch a panic-loop.
	crashLoopWindow    = 5 * time.Minute
	crashLoopThreshold = 3
	// alertCooldown prevents the same crash-loop from spamming
	// Telegram every restart. After we send an alert we won't
	// send another for at least this duration.
	alertCooldown = 30 * time.Minute
	gatewayEventsFile = "logs/gateway-events.jsonl"
)

// gatewayEvent is one line in the events log. Append-only; readers
// tail and decode the last N lines.
type gatewayEvent struct {
	Ts        int64  `json:"ts"`
	Kind      string `json:"kind"` // "start" | "alert-sent"
	Version   string `json:"version,omitempty"`
}

// recordStartupAndAlert is the supervisor's main entry point.
// Appends a "start" event, reads recent events to detect crash
// loops, and sends a Telegram alert if the threshold is hit (and
// we haven't alerted recently). Errors are logged to stderr but
// never returned — startup must not depend on this.
func recordStartupAndAlert(ctx context.Context, paths talonpath.Paths) {
	if paths.Talon.Dir == "" {
		return
	}
	now := time.Now()
	if err := appendGatewayEvent(paths, gatewayEvent{
		Ts:      now.UnixMilli(),
		Kind:    "start",
		Version: talonVersion,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "talon: supervisor record: %v\n", err)
		return
	}
	events, err := tailGatewayEvents(paths, 64)
	if err != nil {
		return
	}
	if !crashLoopDetected(events, now) {
		return
	}
	if recentlyAlerted(events, now) {
		return
	}
	go func() {
		if err := sendCrashLoopAlert(ctx, paths, events, now); err != nil {
			fmt.Fprintf(os.Stderr, "talon: supervisor alert: %v\n", err)
			return
		}
		// Record that we sent so we don't spam.
		_ = appendGatewayEvent(paths, gatewayEvent{
			Ts:   time.Now().UnixMilli(),
			Kind: "alert-sent",
		})
	}()
}

func eventsPath(paths talonpath.Paths) string {
	return filepath.Join(paths.Talon.Dir, gatewayEventsFile)
}

// appendGatewayEvent serializes ev as a single JSON line and
// appends it to the events file. Creates parent dirs as needed.
func appendGatewayEvent(paths talonpath.Paths, ev gatewayEvent) error {
	p := eventsPath(paths)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = f.Write(body)
	return err
}

// tailGatewayEvents returns the last N entries from the events
// file. For our window (5 min, ~10 entries max) we just read the
// whole file — there's no point optimizing for a log we cap at
// dozens of entries per day.
func tailGatewayEvents(paths talonpath.Paths, n int) ([]gatewayEvent, error) {
	f, err := os.Open(eventsPath(paths))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []gatewayEvent{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev gatewayEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// crashLoopDetected reports whether events contains at least
// crashLoopThreshold "start" events within crashLoopWindow ending
// at now. Counts the just-appended start, so threshold=3 means
// "this is the third start in 5 min."
func crashLoopDetected(events []gatewayEvent, now time.Time) bool {
	cutoff := now.Add(-crashLoopWindow).UnixMilli()
	count := 0
	for _, ev := range events {
		if ev.Kind != "start" {
			continue
		}
		if ev.Ts >= cutoff {
			count++
		}
	}
	return count >= crashLoopThreshold
}

// recentlyAlerted returns true when an "alert-sent" event landed
// in the last alertCooldown window — used to suppress repeated
// Telegram pings for the same ongoing crash loop.
func recentlyAlerted(events []gatewayEvent, now time.Time) bool {
	cutoff := now.Add(-alertCooldown).UnixMilli()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "alert-sent" && events[i].Ts >= cutoff {
			return true
		}
	}
	return false
}

// sendCrashLoopAlert composes a short Telegram DM to allowFrom[0]
// describing the crash loop. Best-effort: missing token /
// allowFrom returns a clean error so the caller logs and moves on.
func sendCrashLoopAlert(ctx context.Context, paths talonpath.Paths, events []gatewayEvent, now time.Time) error {
	merged, err := config.MergedBytes(paths)
	if err != nil {
		return err
	}
	token := gjson.GetBytes(merged, "channels.telegram.botToken").Str
	allowFrom := gjson.GetBytes(merged, "channels.telegram.allowFrom.0").Str
	if token == "" || allowFrom == "" {
		return fmt.Errorf("telegram not configured (channels.telegram.{botToken,allowFrom} required)")
	}
	chatID, err := strconv.ParseInt(allowFrom, 10, 64)
	if err != nil {
		return fmt.Errorf("allowFrom[0]=%q is not a numeric chat id: %w", allowFrom, err)
	}

	// Count starts in window for the message body.
	cutoff := now.Add(-crashLoopWindow).UnixMilli()
	starts := 0
	for _, ev := range events {
		if ev.Kind == "start" && ev.Ts >= cutoff {
			starts++
		}
	}
	body := fmt.Sprintf(
		"⚠️ talon-gateway crash loop: %d starts in the last %s. Last start at %s. Check the gateway stdout/stderr where you launched it (`bin/talon gateway run`).",
		starts,
		crashLoopWindow,
		now.Format(time.RFC3339),
	)

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return telegram.SendMessage(sendCtx, token, chatID, body)
}
