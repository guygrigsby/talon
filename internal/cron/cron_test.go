package cron

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedClock returns a clock that always reports `t`.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// stepClock is a manually-advanced clock for tests that need to walk
// time forward across ticks. Get() reads the current time; Set(t)
// snaps to a new value.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Get() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *stepClock) Now() time.Time  { return c.Get() }

func newServiceForTest(t *testing.T, dispatch DispatchFunc, now func() time.Time) *Service {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, dispatch, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func TestNextAfter_FiveAndSixField(t *testing.T) {
	from := time.Date(2026, 5, 6, 12, 30, 15, 0, time.UTC)

	// 5-field: every minute should snap to the next minute boundary.
	got, err := nextAfter("* * * * *", from)
	if err != nil {
		t.Fatalf("5-field parse: %v", err)
	}
	want := time.Date(2026, 5, 6, 12, 31, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("5-field next: got %v want %v", got, want)
	}

	// 6-field: every 5 seconds should snap to the next 5-sec boundary.
	got, err = nextAfter("*/5 * * * * *", from)
	if err != nil {
		t.Fatalf("6-field parse: %v", err)
	}
	want = time.Date(2026, 5, 6, 12, 30, 20, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("6-field next: got %v want %v", got, want)
	}

	// Descriptor: @hourly fires on the next top-of-hour.
	got, err = nextAfter("@hourly", from)
	if err != nil {
		t.Fatalf("@hourly parse: %v", err)
	}
	want = time.Date(2026, 5, 6, 13, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("@hourly next: got %v want %v", got, want)
	}
}

func TestNextAfter_RejectsMalformed(t *testing.T) {
	cases := []string{"", "not-a-cron", "* * *", "* * * * * * *"}
	for _, c := range cases {
		if _, err := nextAfter(c, time.Now()); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestService_AddPersists(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, nil, fixedClock(now))
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.Add(Job{
		ID:         "test1",
		Expression: "*/5 * * * *",
		Action:     Action{Method: "noop"},
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if job.CreatedAtMs != now.UnixMilli() {
		t.Errorf("CreatedAtMs not set: %+v", job)
	}
	if job.NextRunMs == 0 {
		t.Errorf("NextRunMs not set: %+v", job)
	}

	// Reload from disk via a fresh service: jobs round-trip.
	svc2, err := New(store, runs, nil, fixedClock(now))
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	got, ok := svc2.Get("test1")
	if !ok {
		t.Fatal("job not loaded from disk")
	}
	if got.Expression != "*/5 * * * *" || got.Action.Method != "noop" {
		t.Errorf("reloaded job differs: %+v", got)
	}
}

func TestService_AddRejectsBadExpression(t *testing.T) {
	svc := newServiceForTest(t, nil, nil)
	_, err := svc.Add(Job{
		ID:         "bad",
		Expression: "not-a-cron",
		Action:     Action{Method: "noop"},
		Enabled:    true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_AddRejectsMissingFields(t *testing.T) {
	svc := newServiceForTest(t, nil, nil)
	cases := []Job{
		{Expression: "* * * * *", Action: Action{Method: "noop"}, Enabled: true}, // no id
		{ID: "x", Expression: "* * * * *", Action: Action{}, Enabled: true},      // no method
	}
	for _, c := range cases {
		if _, err := svc.Add(c); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestService_TickFiresDueJob(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}

	var fired int32
	dispatch := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		atomic.AddInt32(&fired, 1)
		return map[string]any{"method": method}, nil
	}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, dispatch, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(Job{
		ID:         "every-min",
		Expression: "* * * * *",
		Action:     Action{Method: "health"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}

	// Tick at the same time as Add — not yet due (NextRun is at +1 min).
	svc.Tick(context.Background())
	if got := atomic.LoadInt32(&fired); got != 0 {
		t.Errorf("fired before due: %d", got)
	}

	// Advance to the scheduled next-run boundary.
	clock.Set(time.Date(2026, 5, 6, 12, 1, 0, 0, time.UTC))
	svc.Tick(context.Background())
	if got := atomic.LoadInt32(&fired); got != 1 {
		t.Errorf("fired count: got %d want 1", got)
	}

	// Job's last status should reflect the success and next-run should
	// have advanced past the most recent fire.
	j, _ := svc.Get("every-min")
	if j.LastStatus != "ok" {
		t.Errorf("last status: %q", j.LastStatus)
	}
	if j.LastRunMs == 0 || j.NextRunMs <= j.LastRunMs {
		t.Errorf("next/last not advanced: %+v", j)
	}

	// Another fire when due again.
	clock.Set(time.Date(2026, 5, 6, 12, 2, 0, 0, time.UTC))
	svc.Tick(context.Background())
	if got := atomic.LoadInt32(&fired); got != 2 {
		t.Errorf("second fire count: got %d want 2", got)
	}
}

func TestService_TickRecordsErrors(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	dispatch := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, dispatch, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(Job{
		ID:         "errjob",
		Expression: "* * * * *",
		Action:     Action{Method: "broken"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 5, 6, 12, 1, 0, 0, time.UTC))
	svc.Tick(context.Background())

	j, _ := svc.Get("errjob")
	if j.LastStatus != "error" {
		t.Errorf("expected status=error, got %q", j.LastStatus)
	}
	if j.LastErr == "" {
		t.Errorf("expected error message recorded")
	}

	got, err := svc.Runs("errjob", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("runs len: %d", len(got))
	}
	if got[0].OK || got[0].Error == "" {
		t.Errorf("run not marked failed: %+v", got[0])
	}
}

func TestService_DisabledJobNotFired(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	var fired int32
	dispatch := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		atomic.AddInt32(&fired, 1)
		return nil, nil
	}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, dispatch, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(Job{
		ID:         "off",
		Expression: "* * * * *",
		Action:     Action{Method: "noop"},
		Enabled:    false,
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 5, 6, 12, 5, 0, 0, time.UTC))
	svc.Tick(context.Background())
	if atomic.LoadInt32(&fired) != 0 {
		t.Error("disabled job fired")
	}
}

func TestService_RunNowFiresImmediately(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	var fired int32
	var lastMethod string
	dispatch := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		atomic.AddInt32(&fired, 1)
		lastMethod = method
		return map[string]any{"ok": true}, nil
	}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, dispatch, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(Job{
		ID:         "manual",
		Expression: "0 9 * * *", // 9am daily — wouldn't fire on tick at 12:00
		Action:     Action{Method: "do-the-thing"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(context.Background(), "manual")
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if !run.OK || !run.Manual {
		t.Errorf("run not recorded as ok+manual: %+v", run)
	}
	if atomic.LoadInt32(&fired) != 1 || lastMethod != "do-the-thing" {
		t.Errorf("dispatch not invoked correctly: fired=%d method=%q", fired, lastMethod)
	}
}

func TestService_RunNowUnknownJob(t *testing.T) {
	svc := newServiceForTest(t, nil, nil)
	_, err := svc.RunNow(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_Remove(t *testing.T) {
	svc := newServiceForTest(t, nil, nil)
	if _, err := svc.Add(Job{
		ID:         "tmp",
		Expression: "* * * * *",
		Action:     Action{Method: "noop"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if !svc.Remove("tmp") {
		t.Error("Remove returned false for known id")
	}
	if svc.Remove("tmp") {
		t.Error("Remove returned true for already-removed id")
	}
	if _, ok := svc.Get("tmp"); ok {
		t.Error("job still present after Remove")
	}
}

func TestService_AddReplacesPreservesCreatedAt(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	svc, err := New(store, runs, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Add(Job{
		ID:         "edit",
		Expression: "* * * * *",
		Action:     Action{Method: "v1"},
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 5, 6, 13, 0, 0, 0, time.UTC))
	second, err := svc.Add(Job{
		ID:         "edit",
		Expression: "*/5 * * * *",
		Action:     Action{Method: "v2"},
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedAtMs != first.CreatedAtMs {
		t.Errorf("CreatedAtMs changed across replace: first=%d second=%d", first.CreatedAtMs, second.CreatedAtMs)
	}
	if second.Action.Method != "v2" || second.Expression != "*/5 * * * *" {
		t.Errorf("replace did not update fields: %+v", second)
	}
}

func TestRunLog_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	log := NewRunLog(filepath.Join(dir, "runs.jsonl"))

	for i := range 5 {
		if err := log.Append(Run{
			RunID:       "r" + string(rune('0'+i)),
			JobID:       "job1",
			StartedAtMs: int64(1000 + i),
			OK:          true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Append(Run{RunID: "r-other", JobID: "other", OK: true}); err != nil {
		t.Fatal(err)
	}

	// Default read returns all, newest first.
	all, err := log.Read("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Errorf("all len: %d", len(all))
	}
	if all[0].RunID != "r-other" {
		t.Errorf("expected newest first, got %q", all[0].RunID)
	}

	// Filter to job1.
	got, err := log.Read("job1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("limit not honored: %d", len(got))
	}
	for _, r := range got {
		if r.JobID != "job1" {
			t.Errorf("filter leaked other job: %+v", r)
		}
	}
}

func TestRunLog_LatestEmptyReturnsError(t *testing.T) {
	dir := t.TempDir()
	log := NewRunLog(filepath.Join(dir, "runs.jsonl"))
	if _, err := log.Latest("none"); err == nil {
		t.Error("expected error for empty log")
	}
}

func TestNew_RecomputesNextOnLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "jobs.json"))
	runs := NewRunLog(filepath.Join(dir, "runs.jsonl"))

	// First instance writes a job at noon, with NextRun at 12:01.
	clock := &stepClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	svc1, err := New(store, runs, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Add(Job{
		ID:         "j",
		Expression: "* * * * *",
		Action:     Action{Method: "noop"},
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate gateway down for an hour. New instance loads, should
	// recompute NextRun to 14:01 (the first whole minute past 14:00:30
	// boot time), not replay every minute that elapsed.
	clock.Set(time.Date(2026, 5, 6, 14, 0, 30, 0, time.UTC))
	svc2, err := New(store, runs, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := svc2.Get("j")
	want := time.Date(2026, 5, 6, 14, 1, 0, 0, time.UTC).UnixMilli()
	if got.NextRunMs != want {
		t.Errorf("NextRunMs after reload: got %d want %d", got.NextRunMs, want)
	}
}
