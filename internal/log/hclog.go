package log

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// NewHCLogHandler returns a slog.Handler that writes each record as
// one JSON line in hashicorp/go-hclog's schema:
//
//	{"@level":"info","@message":"...","@timestamp":"2026-...","key":"val",...}
//
// Used by plugin subprocesses so go-plugin's host-side parser
// recognizes every log line and forwards it via the Logger handed
// to ClientConfig. Without this, plugin slog output falls through
// as unparseable raw text, gets demoted to Debug by go-plugin, and
// gets filtered by the host's default Info level — so plugin
// warnings and errors silently disappeared.
//
// Schema reference: github.com/hashicorp/go-hclog/intlogger.go's
// JSON output. Only the `@`-prefixed fields are reserved; arbitrary
// attribute keys go at the top level.
func NewHCLogHandler(w io.Writer) slog.Handler {
	return &hclogHandler{w: w, mu: &sync.Mutex{}}
}

// hclogHandler stores its mutex by pointer so WithAttrs / WithGroup
// can clone the struct without tripping the copylocks vet check —
// every clone shares the same write lock so interleaved Handle
// calls across cloned handlers still serialize.
type hclogHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	attrs []slog.Attr
	group string
}

func (h *hclogHandler) Enabled(_ context.Context, _ slog.Level) bool {
	// Plugin processes don't filter — let the host's level gate
	// drop noise. Trace from the plugin reaching the host as
	// Trace lets the host's adapter decide what to surface.
	return true
}

func (h *hclogHandler) Handle(_ context.Context, r slog.Record) error {
	obj := make(map[string]any, 4+len(h.attrs)+r.NumAttrs())
	obj["@level"] = hclogLevel(r.Level)
	obj["@message"] = r.Message
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	// hclog uses microsecond precision RFC3339 with no nanoseconds.
	obj["@timestamp"] = ts.UTC().Format("2006-01-02T15:04:05.000000Z")

	for _, a := range h.attrs {
		writeAttr(obj, h.group, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(obj, h.group, a)
		return true
	})

	line, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.w.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (h *hclogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]slog.Attr(nil), h.attrs...)
	clone.attrs = append(clone.attrs, attrs...)
	return &clone
}

func (h *hclogHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if h.group == "" {
		clone.group = name
	} else {
		clone.group = h.group + "." + name
	}
	return &clone
}

// hclogLevel maps a slog level onto the lowercase strings hclog
// emits. slog has Debug/Info/Warn/Error; hclog adds Trace below
// Debug. We surface slog Debug as "debug" and let levels below
// that (rare) also map to debug for safety.
func hclogLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	case l >= slog.LevelDebug:
		return "debug"
	default:
		return "trace"
	}
}

// writeAttr flattens a slog.Attr into the destination map. Group
// prefixes (from WithGroup) become dotted key paths, matching how
// slog's JSONHandler emits them. Arbitrary attribute keys collide
// with the @-prefixed ones only if the caller used "@level" /
// "@message" / "@timestamp" themselves — we don't guard against
// that, on the principle that the caller can pick any key but
// shouldn't shadow our schema fields.
func writeAttr(dst map[string]any, group string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindGroup:
		// Nested group: recurse with the dotted prefix.
		nested := group
		if a.Key != "" {
			if nested == "" {
				nested = a.Key
			} else {
				nested = nested + "." + a.Key
			}
		}
		for _, child := range val.Group() {
			writeAttr(dst, nested, child)
		}
	default:
		dst[key] = val.Any()
	}
}

// hclogFormatFromEnv lets callers (Init) detect when the env
// override picked the hclog format. Kept here so the schema
// reference and the env-recognition string stay co-located.
func hclogFormatFromEnv(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "hclog")
}
