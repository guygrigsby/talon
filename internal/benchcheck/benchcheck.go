// Package benchcheck provides a tiny baseline-comparison helper used
// by per-package regression tests. The contract:
//
//  1. Each package with benchmarks ships a TestBenchmarkRegression
//     function that calls testing.Benchmark on every Benchmark* it
//     wants gated, then hands the named results to AssertNotRegressed.
//  2. A single shared baseline file (benchmarks/baseline.json at the
//     repo root) records expected ns/op + allocs/op per benchmark
//     name. New benchmarks can land without baseline entries — they
//     just don't participate in the regression gate until added.
//  3. Per-bench drift is reported but doesn't fail the test alone.
//     The gate is the AVERAGE ratio across all checked benchmarks
//     exceeding tolerance_avg_pct. Per-bench jitter washes out;
//     codebase-wide slowdowns trip.
//
// The regression test self-skips under `go test -short` and when
// TALON_BENCH_SKIP=1 is set in the environment, so iteration loops
// stay fast and CI on heterogeneous hardware can opt out.
package benchcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// NamedResult pairs a benchmark name with the testing.BenchmarkResult
// produced by testing.Benchmark. Tests construct one slice and pass
// it to AssertNotRegressed.
type NamedResult struct {
	Name   string
	Result testing.BenchmarkResult
}

// Median runs f n times via testing.Benchmark and returns the median
// result by ns/op. n=3 is a good default — three samples kill most
// per-run jitter (GC pauses, FS-cache state, CPU scheduling) while
// keeping the per-bench cost at ~3s instead of 1s. Pass n=1 for the
// no-median behavior testing.Benchmark gives by default.
//
// Without this, a single sample on a shared dev machine routinely
// spikes 20-30% above its long-run average, which trips a 3%
// regression gate purely from noise.
func Median(f func(*testing.B), n int) testing.BenchmarkResult {
	if n <= 1 {
		return testing.Benchmark(f)
	}
	results := make([]testing.BenchmarkResult, n)
	for i := range results {
		results[i] = testing.Benchmark(f)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].NsPerOp() < results[j].NsPerOp()
	})
	return results[n/2]
}

// Baseline is the on-disk shape of benchmarks/baseline.json.
type Baseline struct {
	ToleranceAvgPct float64                     `json:"tolerance_avg_pct"`
	Benchmarks      map[string]BenchmarkBaseline `json:"benchmarks"`
}

// BenchmarkBaseline is one expected-numbers row.
type BenchmarkBaseline struct {
	NsPerOp     int64 `json:"ns_per_op"`
	AllocsPerOp int64 `json:"allocs_per_op"`
}

// AssertNotRegressed runs the comparison and calls t.Fatal/t.Error as
// appropriate. Self-skips under -short or TALON_BENCH_SKIP=1.
//
// Behavior:
//   - For every result whose Name appears in the baseline, compute
//     ratio = result.NsPerOp / baseline.NsPerOp.
//   - Per-bench ratios over 1.10 (10%+) print a t.Logf so a single
//     spike is visible without tripping the gate alone.
//   - Per-bench ratios over 2.0 (200%+) are an outlier — those DO
//     fail individually, regardless of the average. A 2× regression
//     is almost certainly a real bug.
//   - The gate: average ratio across all matched benchmarks. If
//     it exceeds 1 + tolerance_avg_pct/100, t.Errorf.
//
// Results not in the baseline are ignored (not new-benchmark
// resistant — add the line to baseline.json when wanted).
func AssertNotRegressed(t *testing.T, results []NamedResult) {
	t.Helper()
	if testing.Short() {
		t.Skip("benchcheck: skipped under -short")
	}
	if os.Getenv("TALON_BENCH_SKIP") == "1" {
		t.Skip("benchcheck: TALON_BENCH_SKIP=1")
	}
	// Default off when invoked as part of `go test ./...` because
	// parallel package execution under default GOMAXPROCS hammers the
	// same cores the bench is timing — observed individual-bench
	// spikes up to 70% even with median-of-5 sampling. `make test`
	// sets TALON_BENCH=1 and serializes packages with -p=1; that's
	// the path the gate is meant for.
	if os.Getenv("TALON_BENCH") != "1" {
		t.Skip("benchcheck: set TALON_BENCH=1 to enable (or run `make test`)")
	}

	baseline, err := LoadBaseline()
	if err != nil {
		t.Fatalf("benchcheck: load baseline: %v", err)
	}

	matched := 0
	var ratioSum float64
	type drift struct {
		name      string
		baseNs    int64
		gotNs     int64
		ratio     float64
		baseAlloc int64
		gotAlloc  int64
	}
	var drifts []drift

	for _, r := range results {
		base, ok := baseline.Benchmarks[r.Name]
		if !ok {
			t.Logf("benchcheck: %q has no baseline entry; skipping", r.Name)
			continue
		}
		if base.NsPerOp == 0 {
			t.Errorf("benchcheck: baseline %q has zero ns_per_op", r.Name)
			continue
		}
		ratio := float64(r.Result.NsPerOp()) / float64(base.NsPerOp)
		matched++
		ratioSum += ratio
		drifts = append(drifts, drift{
			name:      r.Name,
			baseNs:    base.NsPerOp,
			gotNs:     r.Result.NsPerOp(),
			ratio:     ratio,
			baseAlloc: base.AllocsPerOp,
			gotAlloc:  r.Result.AllocsPerOp(),
		})
	}

	if matched == 0 {
		t.Fatalf("benchcheck: 0 of %d results matched the baseline; nothing to gate against", len(results))
	}

	// Print a deterministic table for readable failure context.
	sort.Slice(drifts, func(i, j int) bool { return drifts[i].ratio > drifts[j].ratio })
	for _, d := range drifts {
		pct := (d.ratio - 1) * 100
		marker := "  "
		if d.ratio > 1.10 {
			marker = " ⚠"
		}
		t.Logf("%s %-50s base=%d got=%d (%+.1f%%) allocs base=%d got=%d",
			marker, d.name, d.baseNs, d.gotNs, pct, d.baseAlloc, d.gotAlloc)

		// Outlier: a 2× regression is nearly always a real bug; fail
		// individually so the responsible diff stands out even if
		// other benchmarks happen to be fast that run.
		if d.ratio > 2.0 {
			t.Errorf("benchcheck: %s regressed %.1fx (base %d ns/op, got %d ns/op)",
				d.name, d.ratio, d.baseNs, d.gotNs)
		}
	}

	avgRatio := ratioSum / float64(matched)
	tolerance := baseline.ToleranceAvgPct
	if tolerance == 0 {
		tolerance = 3.0
	}
	limit := 1 + tolerance/100
	if avgRatio > limit {
		t.Errorf("benchcheck: average regression %.2f%% across %d benches exceeds %.1f%% threshold",
			(avgRatio-1)*100, matched, tolerance)
	}
}

// LoadBaseline reads benchmarks/baseline.json. The path is resolved
// relative to the runtime caller's source file, walking up to the
// repo root. This avoids hardcoding cwd assumptions — `go test` runs
// in the package directory, but the baseline lives at the repo top.
func LoadBaseline() (*Baseline, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "benchmarks", "baseline.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Strip JSON comments not supported by encoding/json. The file
	// uses _comment / _* keys instead so we don't need a tolerant
	// parser; nothing to strip in practice.
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(b.Benchmarks) == 0 {
		return nil, errors.New("baseline has no benchmarks")
	}
	return &b, nil
}

// repoRoot walks up from this file's directory until it finds a
// go.mod. Tests can run from any package; the baseline is at the
// repo root regardless.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found walking up from " + file)
		}
		dir = parent
	}
}
