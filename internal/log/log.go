// Package log centralizes talon's slog wiring. One Init call at
// process start swaps the default *slog.Logger and the stdlib log
// package over to a console handler (colored when stderr is a TTY)
// or a JSON handler. Every subsequent slog.Info/Warn/Error call goes
// through the configured handler.
//
// Why this exists: talon was on stdlib `log` with format-string
// messages, which loses the structure callers want when grepping or
// shipping logs. slog gives us key/value pairs; the console handler
// keeps the human-readable feel for interactive dev.
//
// Plugin subprocesses initialize via the same helper. When spawned
// under the host (TALON_PLUGIN_HANDSHAKE set), they default to JSON
// on stderr so the host can parse and re-emit each line as a
// structured event under the host's logger — keeping the output
// looking like one stream rather than scattered prefixes.
package log

import (
	stdlog "log"
	"log/slog"
	"os"
	"strings"
)

// Format selects the handler shape. Text is the human-readable
// console handler with optional ANSI colors; JSON is slog's
// stdlib JSON handler, suitable for log shippers.
type Format int

const (
	FormatText Format = iota
	FormatJSON
)

// ParseFormat is the CLI/env parser. Unknown values fall back to
// text — never JSON, since misconfiguring an interactive run with
// JSON would render the gateway nearly unreadable.
func ParseFormat(s string) Format {
	if strings.EqualFold(strings.TrimSpace(s), "json") {
		return FormatJSON
	}
	return FormatText
}

// Init replaces slog's default logger and the stdlib `log` package's
// output with a handler matching the requested format. Returns the
// configured *slog.Logger so callers that want a non-default logger
// can keep a reference.
//
// Side effects (intentional):
//   - slog.SetDefault is called.
//   - stdlib log.SetOutput / SetFlags are called so any leftover
//     log.Printf calls (third-party libs, code we haven't migrated
//     yet) flow through slog at INFO level instead of writing to
//     stderr with their own prefix.
//
// Idempotent: calling Init twice replaces the default cleanly.
func Init(format Format) *slog.Logger {
	var h slog.Handler
	switch format {
	case FormatJSON:
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	default:
		h = newConsoleHandler(os.Stderr)
	}
	logger := slog.New(h)
	slog.SetDefault(logger)
	// Bridge stdlib log → slog so unmigrated callers still flow
	// through the right pipeline. Strip stdlib's date/time prefix —
	// our handler emits its own timestamp.
	stdlog.SetFlags(0)
	stdlog.SetOutput(slog.NewLogLogger(h, slog.LevelInfo).Writer())
	return logger
}
