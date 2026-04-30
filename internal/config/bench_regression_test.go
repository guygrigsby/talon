package config

import (
	"testing"

	"github.com/guygrigsby/talon/internal/benchcheck"
)

// TestBenchmarkRegression runs this package's benchmarks against the
// repo-root baseline. Self-skips under -short or TALON_BENCH_SKIP=1.
func TestBenchmarkRegression(t *testing.T) {
	results := []benchcheck.NamedResult{
		{Name: "BenchmarkMergedBytes_TalonOnly", Result: benchcheck.Median(BenchmarkMergedBytes_TalonOnly, 5)},
		{Name: "BenchmarkMergedBytes_BothLayers", Result: benchcheck.Median(BenchmarkMergedBytes_BothLayers, 5)},
	}
	benchcheck.AssertNotRegressed(t, results)
}
