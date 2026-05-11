package server

import (
	"context"
	"io"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/provider"
)

// silenceLogs muzzles both log and slog for the duration of a
// benchmark so the per-turn timing log line doesn't inflate ns/op.
// slog is set to a handler with a prohibitively high level so
// Enabled() returns false and slog.Info skips record allocation
// entirely. Restored via b.Cleanup.
func silenceLogs(b *testing.B) {
	b.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))
	b.Cleanup(func() {
		log.SetOutput(prev)
		slog.SetDefault(prevSlog)
	})
}

// instantProvider emits a fixed sequence of deltas and closes,
// without any go-routine handoff delay. Use for benchmarks where
// model latency is irrelevant — we measure pure talon overhead.
type instantProvider struct {
	deltas []provider.Delta
}

func (p *instantProvider) Name() string { return "instant" }

func (p *instantProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Delta, error) {
	ch := make(chan provider.Delta, len(p.deltas))
	for _, d := range p.deltas {
		ch <- d
	}
	close(ch)
	return ch, nil
}

// makeBenchHandler builds a ChatHandler whose provider emits the given
// deltas instantly. workspace="" disables tool runner + skips
// agentcontext.Build's filesystem walk — keeps benchmarks focused on
// the chat loop itself.
func makeBenchHandler(deltas []provider.Delta) *ChatHandler {
	prov := &instantProvider{deltas: deltas}
	return NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "instant/m"}},
		&stubFactory{provider: prov},
		NewChatStore(),
	)
}

// BenchmarkChatTurn_SingleDelta measures the lower bound: a turn
// where the model emits exactly one short text delta and ends. This
// is the floor for any chat.send's talon-side cost.
func BenchmarkChatTurn_SingleDelta(b *testing.B) {
	deltas := []provider.Delta{
		{Kind: provider.DeltaText, Text: "ok"},
	}
	h := makeBenchHandler(deltas)
	ctx := context.Background()
	silenceLogs(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// RunForSession with a fresh sessionKey each iter to avoid
		// history accumulation skewing the measurement.
		key := "bench:" + itoa(i)
		// Prime the new instantProvider per call so the prebuffered
		// channel isn't drained.
		h.factory.(*stubFactory).provider = &instantProvider{deltas: deltas}
		_, err := h.RunForSession(ctx, key, "main", "ping")
		if err != nil {
			b.Fatalf("RunForSession: %v", err)
		}
	}
}

// BenchmarkChatTurn_ManyDeltas measures cost per-delta. The provider
// emits a hundred small text deltas — talon emits a "delta" chat
// event for each one (subagent path suppresses these to avoid the
// WS-write cost, so this benchmark also tracks the WS-write-skipped
// path). Useful for catching regressions in delta dispatch (string
// concatenation, ctx ops, emitter overhead).
func BenchmarkChatTurn_ManyDeltas(b *testing.B) {
	const numDeltas = 100
	deltas := make([]provider.Delta, numDeltas)
	for i := range deltas {
		deltas[i] = provider.Delta{Kind: provider.DeltaText, Text: "x"}
	}
	h := makeBenchHandler(deltas)
	ctx := context.Background()
	silenceLogs(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.factory.(*stubFactory).provider = &instantProvider{deltas: deltas}
		_, err := h.RunForSession(ctx, "bench-many:"+itoa(i), "main", "ping")
		if err != nil {
			b.Fatalf("RunForSession: %v", err)
		}
	}
}

// BenchmarkChatTurn_WithToolDispatch measures the tool dispatch path:
// model emits a tool_call delta, runner runs (instantly via stub),
// model emits final text on iter 2. This is the "iters>1" case the
// production multi-turn loop covers.
func BenchmarkChatTurn_WithToolDispatch(b *testing.B) {
	scripts := [][]provider.Delta{
		// Iteration 1: model emits a tool call.
		{
			{Kind: provider.DeltaToolCall, ToolCall: &provider.ToolCall{
				ID: "call-1", Name: "noop", ArgumentsJSON: `{}`,
			}},
		},
		// Iteration 2: model emits a final text reply.
		{
			{Kind: provider.DeltaText, Text: "done"},
		},
	}
	prov := &scriptedProvider{scripts: scripts}
	h := NewChatHandler(
		&stubResolver{models: map[string]provider.ModelID{"main": "scripted/m"}},
		&stubFactory{provider: prov},
		NewChatStore(),
	)
	h.workspace = &stubWorkspace{dir: b.TempDir()}
	h.tools = func(_ string) ToolRunner {
		return &stubToolRunner{
			specs:   []provider.ToolSpec{{Name: "noop"}},
			outputs: map[string]string{"noop": "ok"},
		}
	}
	ctx := context.Background()
	silenceLogs(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Refresh scripts so the provider stub doesn't run out.
		prov.idx = 0
		prov.calls = nil
		_, err := h.RunForSession(ctx, "bench-tool:"+itoa(i), "main", "ping")
		if err != nil {
			b.Fatalf("RunForSession: %v", err)
		}
	}
}

// BenchmarkChatStore_AppendSnapshot measures the in-memory chat
// store's per-turn cost. Each turn does ~2 Appends (user + assistant)
// + 1 Snapshot, so this approximates one turn's store overhead.
func BenchmarkChatStore_AppendSnapshot(b *testing.B) {
	s := NewChatStore()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := "bench:" + itoa(i)
		s.Append(key, "user", "hello")
		s.Append(key, "assistant", "hi there")
		_ = s.Snapshot(key)
	}
}

// BenchmarkChatStore_LongHistorySnapshot measures Snapshot cost when
// the session has accumulated N turns. Captures the linear cost of
// the slice copy that protects callers from in-place mutation.
func BenchmarkChatStore_LongHistorySnapshot(b *testing.B) {
	cases := []int{10, 100, 500}
	for _, n := range cases {
		b.Run(itoa(n)+"_turns", func(b *testing.B) {
			s := NewChatStore()
			for i := 0; i < n; i++ {
				s.Append("k", "user", "u")
				s.Append("k", "assistant", "a")
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = s.Snapshot("k")
			}
		})
	}
}

// itoa avoids a strconv import for a private helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return strings.Clone(string(buf[i:]))
}
