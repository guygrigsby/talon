package audit

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/guygrigsby/talon/internal/secrets"
)

// Recorder persists agent-action events. Implementations must be
// non-blocking and best-effort: a slow or failed write must never block
// or fail the chat turn.
type Recorder interface {
	Record(Event) // non-blocking, best-effort
	Close() error
}

// Options configures a JSONLRecorder.
type Options struct {
	Path      string
	MaxSizeMB int64 // rotate when the file exceeds this (0 = 10)
	Keep      int   // rotated files to keep (0 = 3)
	MaxField  int   // cap on Args/Output/Text bytes (0 = 8192)
}

// JSONLRecorder appends redacted, bounded events as newline-delimited JSON
// to a size-rotated file. Writes happen on a single background goroutine fed
// by a buffered channel; Record drops (with a warn) when the buffer is full
// so the chat hot path never blocks.
type JSONLRecorder struct {
	opts Options
	ch   chan Event
	done chan struct{}
}

// NewJSONLRecorder constructs a recorder writing to o.Path. The parent
// directory is created 0o700. The background writer starts immediately.
func NewJSONLRecorder(o Options) (*JSONLRecorder, error) {
	if o.MaxSizeMB == 0 {
		o.MaxSizeMB = 10
	}
	if o.Keep == 0 {
		o.Keep = 3
	}
	if o.MaxField == 0 {
		o.MaxField = 8192
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
		return nil, err
	}
	r := &JSONLRecorder{opts: o, ch: make(chan Event, 256), done: make(chan struct{})}
	go r.run()
	return r, nil
}

// Record enqueues e for asynchronous, best-effort writing. It never blocks:
// if the buffer is full the event is dropped and a warning logged.
func (r *JSONLRecorder) Record(e Event) {
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	select {
	case r.ch <- e:
	default:
		slog.Warn("audit: drop event (buffer full)", "kind", e.Kind, "session", e.Session, "run", e.Run)
	}
}

// Close stops accepting events, drains the buffer, and returns after the
// background writer exits.
func (r *JSONLRecorder) Close() error {
	close(r.ch)
	<-r.done
	return nil
}

func (r *JSONLRecorder) run() {
	defer close(r.done)
	for e := range r.ch {
		r.write(r.redact(e))
	}
}

// redact scrubs secrets and bounds the size of secret-bearing fields.
// Args/Output that parse as JSON go through secrets.RedactJSON (sensitive
// keys → [REDACTED]); every secret-bearing field then has literal secret
// references/keys scrubbed and is truncated to MaxField.
func (r *JSONLRecorder) redact(e Event) Event {
	e.Args = r.bound(scrubLiterals(redactMaybeJSON(e.Args)))
	e.Output = r.bound(scrubLiterals(redactMaybeJSON(e.Output)))
	e.Text = r.bound(scrubLiterals(e.Text))
	return e
}

// redactMaybeJSON runs RedactJSON when s parses as a JSON object/array, else
// returns s unchanged. RedactJSON failure-opens (returns input) on malformed
// JSON, so the literal scrub below is the backstop for non-JSON payloads.
func redactMaybeJSON(s string) string {
	if s == "" {
		return s
	}
	out, err := secrets.RedactJSON([]byte(s))
	if err != nil {
		return s
	}
	return string(out)
}

// bound truncates s to MaxField bytes, appending a marker so a reader knows
// the record is incomplete.
func (r *JSONLRecorder) bound(s string) string {
	if len(s) <= r.opts.MaxField {
		return s
	}
	const marker = "…[truncated]"
	cut := r.opts.MaxField - len(marker)
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + marker
}

// secretRefPattern matches op:// and keychain:// reference targets, common
// provider API-key shapes (sk-..., sk-ant-..., sk-proj-...), and bearer
// tokens. Non-JSON output/text can carry these literally; RedactJSON only
// catches them inside JSON under a sensitive key, so this is the backstop.
var secretRefPattern = regexp.MustCompile(
	`(?i)(op://[^\s"']+|keychain://[^\s"']+|sk-[A-Za-z0-9_-]{12,}|bearer\s+[A-Za-z0-9._-]{12,})`,
)

func scrubLiterals(s string) string {
	if s == "" {
		return s
	}
	return secretRefPattern.ReplaceAllString(s, secrets.Placeholder)
}

// write appends one record as a JSON line, rotating first when the file has
// grown past MaxSizeMB. All errors are logged and dropped — never propagated.
func (r *JSONLRecorder) write(e Event) {
	line, err := json.Marshal(e)
	if err != nil {
		slog.Error("audit: marshal failed", "err", err)
		return
	}
	r.rotateIfNeeded(int64(len(line)) + 1)

	f, err := os.OpenFile(r.opts.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Error("audit: open failed", "path", r.opts.Path, "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		slog.Error("audit: write failed", "err", err)
	}
}

// rotateIfNeeded shifts agent-audit.jsonl → .1 → .2 … (dropping beyond Keep)
// when the live file plus the pending write would exceed MaxSizeMB.
func (r *JSONLRecorder) rotateIfNeeded(pending int64) {
	info, err := os.Stat(r.opts.Path)
	if err != nil {
		return // no file yet, or stat failed; nothing to rotate
	}
	if info.Size()+pending <= r.opts.MaxSizeMB<<20 {
		return
	}
	// Drop the oldest, then shift each rotated file up by one.
	_ = os.Remove(r.rotatedPath(r.opts.Keep))
	for n := r.opts.Keep - 1; n >= 1; n-- {
		_ = os.Rename(r.rotatedPath(n), r.rotatedPath(n+1))
	}
	if err := os.Rename(r.opts.Path, r.rotatedPath(1)); err != nil {
		slog.Error("audit: rotate failed", "err", err)
	}
}

func (r *JSONLRecorder) rotatedPath(n int) string {
	return r.opts.Path + "." + strconv.Itoa(n)
}
