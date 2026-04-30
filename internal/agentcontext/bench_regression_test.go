package agentcontext

import (
	"testing"

	"github.com/guygrigsby/talon/internal/benchcheck"
)

// TestBenchmarkRegression deliberately checks NO benchmarks for this
// package today: Build_* is filesystem-cache-sensitive and the
// underlying markdown memory-store is on the swap-out list anyway
// (project memory: "use simple/correct over fast and leave clean
// swap-out points; don't optimize MD reads prematurely"). Including
// Build_EmptyDir / Build_TypicalWorkspace / Build_MemoryHeavy in the
// gate produced 30-130% per-run noise on the dev box, which would
// trip the 3% average threshold purely from filesystem state.
//
// The benchmarks remain (BenchmarkBuild_* in build_bench_test.go)
// for ad-hoc `go test -bench=.` profiling. Re-add lines here when
// the memory-store rewrite lands and numbers stabilize.
func TestBenchmarkRegression(t *testing.T) {
	t.Skip("agentcontext benchmarks are FS-cache sensitive; gate disabled until the memory-store rewrite stabilizes them")
	_ = benchcheck.AssertNotRegressed
}
