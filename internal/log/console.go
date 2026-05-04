package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ANSI escape sequences. Kept inline so the package has no external
// deps; the surface is small enough that pulling in a color lib is
// overkill.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGray   = "\033[90m"
)

// consoleHandler is a slog.Handler that writes one line per record:
//
//	HH:MM:SS.mmm LEVEL message  key=value key2=value2
//
// LEVEL is colored — red for ERROR, yellow for WARN — when the
// destination is a TTY. Pipes/files get plain text. Attribute values
// containing whitespace, '=' or '"' are quoted with backslash-escaped
// quotes so a downstream parser can read them.
//
// Pre-computed prefix attrs (via WithAttrs) are written before the
// per-record attrs so logger groups read naturally:
//
//	logger.With("plugin", "telegram").Info("loaded", "tools", 1)
//	→ ... INFO loaded plugin=telegram tools=1
type consoleHandler struct {
	mu    *sync.Mutex
	out   io.Writer
	color bool
	level slog.Level
	attrs []slog.Attr
	group string
}

func newConsoleHandler(out io.Writer) *consoleHandler {
	color := false
	if f, ok := out.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			color = true
		}
	}
	// Honor NO_COLOR (https://no-color.org/) and TALON_LOG_COLOR=0.
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TALON_LOG_COLOR") == "0" {
		color = false
	}
	return &consoleHandler{
		mu:    &sync.Mutex{},
		out:   out,
		color: color,
		level: slog.LevelInfo,
	}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	// Timestamp — high-resolution but compact. Drop the date because
	// the gateway reboots often enough that the time alone is the
	// useful bit interactively.
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	if h.color {
		b.WriteString(ansiGray)
	}
	b.WriteString(ts.Format("15:04:05.000"))
	if h.color {
		b.WriteString(ansiReset)
	}
	b.WriteByte(' ')

	level := levelLabel(r.Level)
	if h.color {
		b.WriteString(levelColor(r.Level))
		b.WriteString(level)
		b.WriteString(ansiReset)
	} else {
		b.WriteString(level)
	}
	// Pad the level to 5 chars so messages line up across mixed
	// levels (INFO/WARN are 4, ERROR is 5).
	for i := len(level); i < 5; i++ {
		b.WriteByte(' ')
	}
	b.WriteByte(' ')
	b.WriteString(r.Message)

	// Static attrs first (from WithAttrs chain), then per-record.
	for _, a := range h.attrs {
		appendAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	clone.attrs = append(clone.attrs, h.attrs...)
	clone.attrs = append(clone.attrs, attrs...)
	return &clone
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if clone.group != "" {
		clone.group = clone.group + "." + name
	} else {
		clone.group = name
	}
	return &clone
}

// levelLabel maps the slog level enum to a 4- or 5-char tag.
func levelLabel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

// levelColor picks the ANSI code for a level. Red for errors,
// yellow for warnings (per spec); cyan for info to lift it above
// gray ts/attrs without being loud; gray for debug.
func levelColor(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return ansiRed
	case l >= slog.LevelWarn:
		return ansiYellow
	case l >= slog.LevelInfo:
		return ansiCyan
	default:
		return ansiGray
	}
}

// appendAttr writes " key=value" with quoting when the value would
// otherwise be ambiguous on the line. Times render as RFC3339;
// errors as their .Error() string; everything else through
// slog.Value's default rendering.
func appendAttr(b *strings.Builder, a slog.Attr) {
	if a.Key == "" {
		return
	}
	b.WriteByte(' ')
	if false { // ascii color hook for the key — left off for readability
		b.WriteString(ansiGray)
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := renderValue(a.Value)
	if needsQuote(v) {
		b.WriteString(strconv.Quote(v))
	} else {
		b.WriteString(v)
	}
}

func renderValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindAny:
		// Errors are common — render their message rather than %v of
		// the struct.
		if e, ok := v.Any().(error); ok && e != nil {
			return e.Error()
		}
		return fmt.Sprint(v.Any())
	default:
		return v.String()
	}
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r == ' ' || r == '"' || r == '=' || r == '\t' || r == '\n' {
			return true
		}
	}
	return false
}
