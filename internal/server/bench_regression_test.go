package server

import (
	"testing"

	"github.com/guygrigsby/talon/internal/benchcheck"
)

// TestBenchmarkRegression runs this package's benchmarks against the
// repo-root baseline. Self-skips under -short or TALON_BENCH_SKIP=1.
//
// Note: ChatStore_LongHistorySnapshot is a sub-benchmark
// (b.Run("...", ...)) so it isn't directly addressable as a
// top-level Benchmark*; we exclude it from the gate. The two flat
// benches in this package are sufficient signal for chat-path
// regressions.
func TestBenchmarkRegression(t *testing.T) {
	results := []benchcheck.NamedResult{
		{Name: "BenchmarkChatTurn_SingleDelta", Result: benchcheck.Median(BenchmarkChatTurn_SingleDelta, 5)},
		{Name: "BenchmarkChatTurn_ManyDeltas", Result: benchcheck.Median(BenchmarkChatTurn_ManyDeltas, 5)},
		{Name: "BenchmarkChatTurn_WithToolDispatch", Result: benchcheck.Median(BenchmarkChatTurn_WithToolDispatch, 5)},
		{Name: "BenchmarkChatStore_AppendSnapshot", Result: benchcheck.Median(BenchmarkChatStore_AppendSnapshot, 5)},
	}
	benchcheck.AssertNotRegressed(t, results)
}
