package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cronpkg "github.com/guygrigsby/talon/internal/cron"
)

// cronTestHandler builds a CronHandler bound to a fresh tempdir
// fixture. Returns the handler and the registry so tests can dispatch
// methods through the same routing the WS layer uses.
func cronTestHandler(t *testing.T) (*CronHandler, *Registry) {
	t.Helper()
	paths := readFixture(t, "{}")
	r := NewRegistry()
	// Register a no-op test method so cron.run has something to fire.
	r.Register("test.noop", func(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
		return map[string]any{"ok": true}, nil
	})
	r.Register("test.fail", func(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "broken on purpose"}
	})
	h, err := NewCronHandler(paths, dispatchFromRegistry(r))
	if err != nil {
		t.Fatalf("NewCronHandler: %v", err)
	}
	h.Register(r)
	return h, r
}

// callRPC dispatches a method via the registry and unmarshals the
// response into v. Cuts test boilerplate.
func callRPC(t *testing.T, r *Registry, method string, params any, v any) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	out, ferr := r.Dispatch(context.Background(), HandlerCtx{}, method, body)
	if ferr != nil {
		t.Fatalf("%s: %s: %s", method, ferr.Code, ferr.Message)
	}
	if v != nil {
		raw, _ := json.Marshal(out)
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("unmarshal %s response: %v", method, err)
		}
	}
}

func TestCronAdd_RejectsMissingFields(t *testing.T) {
	_, r := cronTestHandler(t)
	cases := []map[string]any{
		{"expression": "* * * * *", "method": "test.noop"},      // no id
		{"id": "x", "method": "test.noop"},                      // no expression
		{"id": "x", "expression": "* * * * *"},                  // no method
		{"id": "x", "expression": "bogus", "method": "test.noop"}, // bad expr
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		_, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "cron.add", body)
		if ferr == nil {
			t.Errorf("expected error for %+v", c)
			continue
		}
		if ferr.Code != ErrCodeBadRequest {
			t.Errorf("expected BAD_REQUEST for %+v, got %s", c, ferr.Code)
		}
	}
}

func TestCronAdd_StoresJob(t *testing.T) {
	_, r := cronTestHandler(t)

	var addResp struct {
		Job cronpkg.Job `json:"job"`
	}
	callRPC(t, r, "cron.add", map[string]any{
		"id":         "j1",
		"expression": "*/5 * * * *",
		"method":     "test.noop",
	}, &addResp)
	if addResp.Job.ID != "j1" || addResp.Job.Expression != "*/5 * * * *" {
		t.Errorf("add response: %+v", addResp.Job)
	}
	if !addResp.Job.Enabled {
		t.Errorf("default enabled should be true: %+v", addResp.Job)
	}
	if addResp.Job.NextRunMs == 0 {
		t.Errorf("NextRunMs not populated: %+v", addResp.Job)
	}

	// Round-trip through cron.list.
	var listResp struct {
		Jobs []cronpkg.Job `json:"jobs"`
	}
	callRPC(t, r, "cron.list", map[string]any{}, &listResp)
	if len(listResp.Jobs) != 1 || listResp.Jobs[0].ID != "j1" {
		t.Errorf("list response: %+v", listResp.Jobs)
	}
}

func TestCronAdd_HonorsNestedAction(t *testing.T) {
	// The RPC accepts both top-level method/params and nested
	// action.{method,params}. Verify the nested form works too.
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{
		"id":         "nested",
		"expression": "* * * * *",
		"action": map[string]any{
			"method": "test.noop",
			"params": map[string]any{"x": 1},
		},
	}, nil)
}

func TestCronRemove_ReturnsRemovedFlag(t *testing.T) {
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{
		"id":         "rm",
		"expression": "* * * * *",
		"method":     "test.noop",
	}, nil)

	var resp struct {
		OK      bool `json:"ok"`
		Removed bool `json:"removed"`
	}
	callRPC(t, r, "cron.remove", map[string]any{"id": "rm"}, &resp)
	if !resp.Removed {
		t.Error("removed flag should be true for known id")
	}

	callRPC(t, r, "cron.remove", map[string]any{"id": "rm"}, &resp)
	if resp.Removed {
		t.Error("second remove should report removed=false")
	}
}

func TestCronRun_FiresAndRecordsRun(t *testing.T) {
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{
		"id":         "manual",
		"expression": "0 9 * * *", // not due now — only manual fires
		"method":     "test.noop",
	}, nil)

	var resp struct {
		Run cronpkg.Run `json:"run"`
	}
	callRPC(t, r, "cron.run", map[string]any{"id": "manual"}, &resp)
	if !resp.Run.OK {
		t.Errorf("manual run should be ok: %+v", resp.Run)
	}
	if !resp.Run.Manual {
		t.Errorf("manual flag not set: %+v", resp.Run)
	}
	if resp.Run.JobID != "manual" {
		t.Errorf("run.jobId mismatch: %+v", resp.Run)
	}

	// Run log should reflect it.
	var runsResp struct {
		Runs []cronpkg.Run `json:"runs"`
	}
	callRPC(t, r, "cron.runs", map[string]any{"id": "manual"}, &runsResp)
	if len(runsResp.Runs) != 1 {
		t.Errorf("runs len: %d", len(runsResp.Runs))
	}
}

func TestCronRun_PropagatesDispatchError(t *testing.T) {
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{
		"id":         "fails",
		"expression": "* * * * *",
		"method":     "test.fail",
	}, nil)

	var resp struct {
		Run cronpkg.Run `json:"run"`
	}
	callRPC(t, r, "cron.run", map[string]any{"id": "fails"}, &resp)
	if resp.Run.OK {
		t.Errorf("expected run.OK=false: %+v", resp.Run)
	}
	if resp.Run.Error == "" || !strings.Contains(resp.Run.Error, "broken on purpose") {
		t.Errorf("expected error message in run: %+v", resp.Run)
	}
}

func TestCronRun_UnknownJobIsBadRequest(t *testing.T) {
	_, r := cronTestHandler(t)
	body, _ := json.Marshal(map[string]any{"id": "nope"})
	_, ferr := r.Dispatch(context.Background(), HandlerCtx{}, "cron.run", body)
	if ferr == nil {
		t.Fatal("expected error")
	}
	if ferr.Code != ErrCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %s: %s", ferr.Code, ferr.Message)
	}
}

func TestCronStatus_FiltersByID(t *testing.T) {
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{"id": "a", "expression": "* * * * *", "method": "test.noop"}, nil)
	callRPC(t, r, "cron.add", map[string]any{"id": "b", "expression": "* * * * *", "method": "test.noop"}, nil)

	var resp struct {
		Jobs []cronpkg.Job `json:"jobs"`
	}
	callRPC(t, r, "cron.status", map[string]any{"id": "a"}, &resp)
	if len(resp.Jobs) != 1 || resp.Jobs[0].ID != "a" {
		t.Errorf("status filtered: %+v", resp.Jobs)
	}
}

func TestCronRuns_FiltersAndLimits(t *testing.T) {
	_, r := cronTestHandler(t)
	callRPC(t, r, "cron.add", map[string]any{"id": "x", "expression": "0 9 * * *", "method": "test.noop"}, nil)
	callRPC(t, r, "cron.add", map[string]any{"id": "y", "expression": "0 9 * * *", "method": "test.noop"}, nil)

	for range 3 {
		callRPC(t, r, "cron.run", map[string]any{"id": "x"}, nil)
	}
	callRPC(t, r, "cron.run", map[string]any{"id": "y"}, nil)

	var all struct {
		Runs []cronpkg.Run `json:"runs"`
	}
	callRPC(t, r, "cron.runs", map[string]any{}, &all)
	if len(all.Runs) != 4 {
		t.Errorf("all runs len: %d", len(all.Runs))
	}

	var filtered struct {
		Runs []cronpkg.Run `json:"runs"`
	}
	callRPC(t, r, "cron.runs", map[string]any{"id": "x", "limit": 2}, &filtered)
	if len(filtered.Runs) != 2 {
		t.Errorf("filtered limit: %d", len(filtered.Runs))
	}
	for _, r := range filtered.Runs {
		if r.JobID != "x" {
			t.Errorf("filter leaked: %+v", r)
		}
	}
}
