package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/comfyui"
)

// --- config + workflow loading --------------------------------------------

func TestLoadComfyUIConfig_DefaultsAndOverrides(t *testing.T) {
	paths := readFixture(t, `{}`)
	talonCfg := []byte(`{"images":{"providers":{"comfyui":{"baseUrl":"http://10.0.0.226:8188","workflow":{"promptNodeId":"6","negativePromptNodeId":"7","seedNodeId":"3"}}}}}`)
	if err := os.WriteFile(paths.Talon.Config, talonCfg, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewImagesHandler(paths)
	cfg, ferr := h.loadComfyUIConfig("")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig: %+v", ferr)
	}
	if cfg.BaseURL != "http://10.0.0.226:8188" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.PromptNodeID != "6" || cfg.NegativePromptNodeID != "7" || cfg.SeedNodeID != "3" {
		t.Errorf("node ids: %+v", cfg)
	}
	if !strings.HasSuffix(cfg.WorkflowPath, "images/comfyui-default.json") {
		t.Errorf("default workflow path: %q", cfg.WorkflowPath)
	}
}

func TestReadAndPatchWorkflow_RequiresPromptNodeID(t *testing.T) {
	// Validation moved from loadComfyUIConfig to readAndPatchWorkflow
	// so list/fetch handlers (which only need BaseURL) work for users
	// who haven't exported a workflow yet. Generate still bails when
	// PromptNodeID is missing — verify here.
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "wf.json")
	if err := os.WriteFile(wfPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ferr := readAndPatchWorkflow(comfyUIConfig{WorkflowPath: wfPath}, imagesGenerateParams{Prompt: "x"})
	if ferr == nil || !strings.Contains(ferr.Message, "promptNodeId") {
		t.Fatalf("expected promptNodeId requirement error, got %+v", ferr)
	}
}

func TestReadAndPatchWorkflow_PatchesPromptNegativeAndSeed(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "wf.json")
	wf := `{
		"3": {"class_type": "KSampler", "inputs": {"seed": 0, "steps": 20}},
		"6": {"class_type": "CLIPTextEncode", "inputs": {"text": "PLACEHOLDER_POS"}},
		"7": {"class_type": "CLIPTextEncode", "inputs": {"text": "PLACEHOLDER_NEG"}}
	}`
	if err := os.WriteFile(wfPath, []byte(wf), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := comfyUIConfig{
		WorkflowPath:         wfPath,
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "3",
	}
	seed := int64(42)
	out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{
		Prompt:         "a cat",
		NegativePrompt: "blurry",
		Seed:           &seed,
	})
	if ferr != nil {
		t.Fatalf("patch: %+v", ferr)
	}
	var got map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["6"].Inputs["text"] != "a cat" {
		t.Errorf("positive prompt not patched: %+v", got["6"].Inputs)
	}
	if got["7"].Inputs["text"] != "blurry" {
		t.Errorf("negative prompt not patched: %+v", got["7"].Inputs)
	}
	if int64(got["3"].Inputs["seed"].(float64)) != 42 {
		t.Errorf("seed not patched: %+v", got["3"].Inputs)
	}
	// Untouched fields preserved.
	if got["3"].Inputs["steps"].(float64) != 20 {
		t.Errorf("KSampler.steps lost during patch: %+v", got["3"].Inputs)
	}
}

func TestReadAndPatchWorkflow_RandomSeedWhenUnspecified(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "wf.json")
	wf := `{"3": {"class_type": "KSampler", "inputs": {"seed": 0}}, "6": {"class_type": "CLIPTextEncode", "inputs": {"text": ""}}}`
	if err := os.WriteFile(wfPath, []byte(wf), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := comfyUIConfig{WorkflowPath: wfPath, PromptNodeID: "6", SeedNodeID: "3"}

	out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "x"})
	if ferr != nil {
		t.Fatalf("patch: %+v", ferr)
	}
	var got map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	_ = json.Unmarshal(out, &got)
	seed := int64(got["3"].Inputs["seed"].(float64))
	if seed == 0 {
		t.Errorf("expected randomized seed, got 0")
	}
}

func TestReadAndPatchWorkflow_NoiseSeedFallback(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "wf.json")
	wf := `{"3": {"class_type": "SamplerCustom", "inputs": {"noise_seed": 0}}, "6": {"class_type": "CLIPTextEncode", "inputs": {"text": ""}}}`
	if err := os.WriteFile(wfPath, []byte(wf), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := comfyUIConfig{WorkflowPath: wfPath, PromptNodeID: "6", SeedNodeID: "3"}
	seed := int64(99)
	out, _ := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "x", Seed: &seed})
	var got map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	_ = json.Unmarshal(out, &got)
	if int64(got["3"].Inputs["noise_seed"].(float64)) != 99 {
		t.Errorf("noise_seed not patched: %+v", got["3"].Inputs)
	}
	if _, ok := got["3"].Inputs["seed"]; ok {
		t.Errorf("should not have introduced a 'seed' field on a noise_seed sampler")
	}
}

func TestReadAndPatchWorkflow_MissingFile(t *testing.T) {
	cfg := comfyUIConfig{WorkflowPath: filepath.Join(t.TempDir(), "missing.json"), PromptNodeID: "6"}
	_, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "x"})
	if ferr == nil || !strings.Contains(ferr.Message, "workflow file not found") {
		t.Fatalf("expected not-found error, got %+v", ferr)
	}
}

func TestReadAndPatchWorkflow_BadNodeID(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "wf.json")
	if err := os.WriteFile(wfPath, []byte(`{"6":{"class_type":"x","inputs":{"text":""}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := comfyUIConfig{WorkflowPath: wfPath, PromptNodeID: "999"}
	_, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "x"})
	if ferr == nil || !strings.Contains(ferr.Message, "no node") {
		t.Fatalf("expected unknown-node error, got %+v", ferr)
	}
}

// --- handler validation -----------------------------------------------------

func TestImagesGenerate_RejectsMissingFields(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{}`), 0o600)
	h := NewImagesHandler(paths)
	cases := []string{
		`{}`,
		`{"prompt":"hi"}`,
		`{"sessionKey":"foo"}`,
	}
	for _, body := range cases {
		_, ferr := h.handleGenerate(t.Context(), HandlerCtx{}, []byte(body))
		if ferr == nil || ferr.Code != ErrCodeBadRequest {
			t.Errorf("body=%s: expected BAD_REQUEST, got %+v", body, ferr)
		}
	}
}

func TestImagesFetch_RejectsMissingFilename(t *testing.T) {
	h := NewImagesHandler(readFixture(t, `{}`))
	_, ferr := h.handleFetch(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %+v", ferr)
	}
}

// --- handler happy path with stub client + captured events ----------------

type stubComfyUI struct {
	submit     func(ctx context.Context, w json.RawMessage, cid string) (*comfyui.SubmitResult, error)
	events     func(ctx context.Context, cid string) (<-chan comfyui.Event, <-chan error, error)
	history    func(ctx context.Context, pid string) (*comfyui.HistoryEntry, error)
	historyAll func(ctx context.Context, max int) ([]comfyui.HistoryListEntry, error)
	fetch      func(ctx context.Context, ref comfyui.ImageRef, preview string) ([]byte, string, error)
}

func (s *stubComfyUI) Submit(ctx context.Context, w json.RawMessage, cid string) (*comfyui.SubmitResult, error) {
	return s.submit(ctx, w, cid)
}
func (s *stubComfyUI) Events(ctx context.Context, cid string) (<-chan comfyui.Event, <-chan error, error) {
	return s.events(ctx, cid)
}
func (s *stubComfyUI) History(ctx context.Context, pid string) (*comfyui.HistoryEntry, error) {
	return s.history(ctx, pid)
}
func (s *stubComfyUI) HistoryAll(ctx context.Context, max int) ([]comfyui.HistoryListEntry, error) {
	if s.historyAll == nil {
		return nil, nil
	}
	return s.historyAll(ctx, max)
}
func (s *stubComfyUI) Fetch(ctx context.Context, ref comfyui.ImageRef, preview string) ([]byte, string, error) {
	return s.fetch(ctx, ref, preview)
}

type capturedEvent struct {
	State string
	Data  map[string]any
}

func TestImagesGenerate_HappyPath_EmitsQueuedRunningFinal(t *testing.T) {
	paths := readFixture(t, `{}`)
	wfPath := filepath.Join(paths.Talon.Dir, "wf.json")
	if err := os.WriteFile(wfPath, []byte(`{"3":{"class_type":"KSampler","inputs":{"seed":0}},"6":{"class_type":"CLIPTextEncode","inputs":{"text":""}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"images":{"providers":{"comfyui":{"baseUrl":"http://stub","workflow":{"path":%q,"promptNodeId":"6","seedNodeId":"3"}}}}}`, wfPath)
	if err := os.WriteFile(paths.Talon.Config, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	evCh := make(chan comfyui.Event, 8)
	errCh := make(chan error, 1)

	stub := &stubComfyUI{
		submit: func(_ context.Context, _ json.RawMessage, _ string) (*comfyui.SubmitResult, error) {
			return &comfyui.SubmitResult{PromptID: "p1", Number: 0}, nil
		},
		events:  func(_ context.Context, _ string) (<-chan comfyui.Event, <-chan error, error) { return evCh, errCh, nil },
		history: func(_ context.Context, _ string) (*comfyui.HistoryEntry, error) {
			return &comfyui.HistoryEntry{
				Outputs: map[string]comfyui.NodeOutput{
					"9": {Images: []comfyui.ImageRef{{Filename: "out.png", Type: "output"}}},
				},
				Status: comfyui.HistoryStatus{StatusStr: "success", Completed: true},
			}, nil
		},
	}

	captured := make(chan capturedEvent, 16)
	var capMu sync.Mutex
	h := NewImagesHandler(paths).
		WithDial(func(string) ComfyUIClient { return stub }).
		WithEmit(func(_ *Session, _ string, _ string, state string, data map[string]any) error {
			capMu.Lock()
			defer capMu.Unlock()
			captured <- capturedEvent{State: state, Data: data}
			return nil
		})

	res, ferr := h.handleGenerate(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"agent:main:main","prompt":"a cat"}`))
	if ferr != nil {
		t.Fatalf("generate: %+v", ferr)
	}
	if _, ok := res.(map[string]any)["runId"]; !ok {
		t.Fatalf("missing runId: %+v", res)
	}

	// Drive the goroutine through running → success.
	pidData, _ := json.Marshal(map[string]any{"prompt_id": "p1", "node": "3"})
	evCh <- comfyui.Event{Type: "executing", Data: pidData}
	successData, _ := json.Marshal(map[string]any{"prompt_id": "p1"})
	evCh <- comfyui.Event{Type: "execution_success", Data: successData}

	// Collect emitted states; expect queued, running, final.
	deadline := time.After(2 * time.Second)
	states := []string{}
loop:
	for {
		select {
		case ev := <-captured:
			states = append(states, ev.State)
			if ev.State == "final" {
				if imgs, ok := ev.Data["images"].([]comfyui.ImageRef); !ok || len(imgs) != 1 || imgs[0].Filename != "out.png" {
					t.Errorf("final images payload wrong: %+v", ev.Data)
				}
				break loop
			}
		case <-deadline:
			t.Fatalf("timeout; states so far: %v", states)
		}
	}
	wantOrder := []string{"queued", "running", "final"}
	for i, w := range wantOrder {
		if i >= len(states) || states[i] != w {
			t.Errorf("states[%d] = %v, want %v (full=%v)", i, states[i], w, states)
		}
	}
}

func TestImagesGenerate_SubmitErrorEmitsErrorState(t *testing.T) {
	paths := readFixture(t, `{}`)
	wfPath := filepath.Join(paths.Talon.Dir, "wf.json")
	_ = os.WriteFile(wfPath, []byte(`{"6":{"class_type":"x","inputs":{"text":""}}}`), 0o600)
	cfgJSON := fmt.Sprintf(`{"images":{"providers":{"comfyui":{"baseUrl":"http://stub","workflow":{"path":%q,"promptNodeId":"6"}}}}}`, wfPath)
	_ = os.WriteFile(paths.Talon.Config, []byte(cfgJSON), 0o600)

	stub := &stubComfyUI{
		submit: func(_ context.Context, _ json.RawMessage, _ string) (*comfyui.SubmitResult, error) {
			return nil, errors.New("comfy down")
		},
		events: func(_ context.Context, _ string) (<-chan comfyui.Event, <-chan error, error) {
			return make(chan comfyui.Event), make(chan error), nil
		},
	}

	captured := make(chan capturedEvent, 4)
	h := NewImagesHandler(paths).
		WithDial(func(string) ComfyUIClient { return stub }).
		WithEmit(func(_ *Session, _ string, _ string, state string, data map[string]any) error {
			captured <- capturedEvent{State: state, Data: data}
			return nil
		})

	_, ferr := h.handleGenerate(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"agent:main:main","prompt":"x"}`))
	if ferr != nil {
		t.Fatalf("generate: %+v", ferr)
	}
	select {
	case ev := <-captured:
		if ev.State != "error" {
			t.Errorf("first emit state = %q, want error", ev.State)
		}
		if !strings.Contains(fmt.Sprintf("%v", ev.Data["errorMessage"]), "comfy down") {
			t.Errorf("error message: %+v", ev.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no error event emitted")
	}
}

func TestImagesGenerate_BailsAfterRepeatedEmitFailures(t *testing.T) {
	paths := readFixture(t, `{}`)
	wfPath := filepath.Join(paths.Talon.Dir, "wf.json")
	_ = os.WriteFile(wfPath, []byte(`{"6":{"class_type":"x","inputs":{"text":""}}}`), 0o600)
	cfgJSON := fmt.Sprintf(`{"images":{"providers":{"comfyui":{"baseUrl":"http://stub","workflow":{"path":%q,"promptNodeId":"6"}}}}}`, wfPath)
	_ = os.WriteFile(paths.Talon.Config, []byte(cfgJSON), 0o600)

	evCh := make(chan comfyui.Event, 16)
	stub := &stubComfyUI{
		submit:  func(_ context.Context, _ json.RawMessage, _ string) (*comfyui.SubmitResult, error) { return &comfyui.SubmitResult{PromptID: "p1"}, nil },
		events:  func(_ context.Context, _ string) (<-chan comfyui.Event, <-chan error, error) { return evCh, make(chan error), nil },
		history: func(_ context.Context, _ string) (*comfyui.HistoryEntry, error) { return &comfyui.HistoryEntry{}, nil },
	}

	var emitCount int
	var mu sync.Mutex
	h := NewImagesHandler(paths).
		WithDial(func(string) ComfyUIClient { return stub }).
		WithEmit(func(*Session, string, string, string, map[string]any) error {
			mu.Lock()
			defer mu.Unlock()
			emitCount++
			return errors.New("ws closed") // every emit fails
		})

	_, ferr := h.handleGenerate(t.Context(), HandlerCtx{}, []byte(`{"sessionKey":"agent:main:main","prompt":"x"}`))
	if ferr != nil {
		t.Fatalf("generate: %+v", ferr)
	}

	// Drive the goroutine — feed enough events that without the
	// emit-failure threshold we'd see >> 3 emits.
	for i := 0; i < 10; i++ {
		data, _ := json.Marshal(map[string]any{"prompt_id": "p1", "value": i, "max": 10})
		evCh <- comfyui.Event{Type: "progress", Data: data}
	}
	// Give the goroutine time to drain.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		c := emitCount
		mu.Unlock()
		if c > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no emits seen")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	// After the threshold (3) the closure stops calling h.emit, so
	// the recorded count caps near 3 even though the run keeps going
	// (persistence + ComfyUI accounting must not depend on the
	// client staying connected). Allow a little slack for racing but
	// flag clear runaways.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	final := emitCount
	mu.Unlock()
	if final > 5 {
		t.Errorf("expected emits to stop near threshold (≤ ~4); got %d", final)
	}
}

// --- images.list -----------------------------------------------------------

func TestImagesList_FlattensHistoryToImageItems(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)

	stub := &stubComfyUI{
		historyAll: func(_ context.Context, max int) ([]comfyui.HistoryListEntry, error) {
			// images.list over-fetches by 2× to leave headroom for
			// merging the persistent index with live history; the
			// final response is still capped at the requested limit.
			if max != 100 {
				t.Errorf("history fetch should be 2× default limit (100), got %d", max)
			}
			return []comfyui.HistoryListEntry{
				{
					PromptID: "p-1",
					Entry: comfyui.HistoryEntry{
						Outputs: map[string]comfyui.NodeOutput{
							"9": {Images: []comfyui.ImageRef{
								{Filename: "a.png", Type: "output"},
								{Filename: "b.png", Type: "output"},
							}},
						},
					},
				},
				{
					PromptID: "p-2",
					Entry: comfyui.HistoryEntry{
						Outputs: map[string]comfyui.NodeOutput{
							"9": {Images: []comfyui.ImageRef{{Filename: "c.png", Type: "output"}}},
						},
					},
				},
			}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	res, ferr := h.handleList(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr != nil {
		t.Fatalf("list: %+v", ferr)
	}
	images := res.(map[string]any)["images"].([]imagesListItem)
	if len(images) != 3 {
		t.Fatalf("got %d items, want 3", len(images))
	}
	// Group all items by prompt id and assert each filename appears once.
	byPrompt := map[string][]string{}
	for _, item := range images {
		byPrompt[item.PromptID] = append(byPrompt[item.PromptID], item.Filename)
	}
	if len(byPrompt["p-1"]) != 2 || len(byPrompt["p-2"]) != 1 {
		t.Fatalf("prompt grouping wrong: %+v", byPrompt)
	}
}

func TestImagesList_RespectsLimit(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)

	got := 0
	stub := &stubComfyUI{
		historyAll: func(_ context.Context, max int) ([]comfyui.HistoryListEntry, error) {
			got = max
			return nil, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	_, _ = h.handleList(t.Context(), HandlerCtx{}, []byte(`{"limit":12}`))
	if got != 24 {
		t.Errorf("history over-fetch should be 2×limit=24, got %d", got)
	}
}

func TestImagesList_MergesIndexAndHistoryDedupedByFilename(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)

	// Seed the index with one entry that overlaps with what live
	// history will return.
	indexPath := filepath.Join(paths.Talon.Dir, "images", "index.json")
	_ = os.MkdirAll(filepath.Dir(indexPath), 0o700)
	_ = os.WriteFile(indexPath, []byte(`{"items":[
		{"filename":"old.png","subfolder":"","type":"output","promptId":"p-old","createdAtMs":111},
		{"filename":"shared.png","subfolder":"","type":"output","promptId":"p-shared","createdAtMs":222}
	]}`), 0o600)

	stub := &stubComfyUI{
		historyAll: func(_ context.Context, _ int) ([]comfyui.HistoryListEntry, error) {
			return []comfyui.HistoryListEntry{
				{
					PromptID: "p-shared",
					Entry: comfyui.HistoryEntry{
						Outputs: map[string]comfyui.NodeOutput{
							"9": {Images: []comfyui.ImageRef{{Filename: "shared.png", Type: "output"}}},
						},
					},
				},
				{
					PromptID: "p-new",
					Entry: comfyui.HistoryEntry{
						Outputs: map[string]comfyui.NodeOutput{
							"9": {Images: []comfyui.ImageRef{{Filename: "new.png", Type: "output"}}},
						},
					},
				},
			}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	res, ferr := h.handleList(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr != nil {
		t.Fatalf("list: %+v", ferr)
	}
	images := res.(map[string]any)["images"].([]imagesListItem)
	// Index entries first (newest-first by file order), then live-only
	// entries appended after.
	wantOrder := []string{"old.png", "shared.png", "new.png"}
	if len(images) != len(wantOrder) {
		t.Fatalf("got %d items, want %d (%v)", len(images), len(wantOrder), images)
	}
	for i, want := range wantOrder {
		if images[i].Filename != want {
			t.Errorf("images[%d]=%q, want %q", i, images[i].Filename, want)
		}
	}
	// Index-sourced entries keep their createdAtMs; live-only entries
	// don't have one.
	for _, it := range images {
		switch it.Filename {
		case "old.png":
			if it.CreatedAtMs != 111 {
				t.Errorf("old.png lost timestamp: %+v", it)
			}
		case "new.png":
			if it.CreatedAtMs != 0 {
				t.Errorf("live-only new.png shouldn't have timestamp: %+v", it)
			}
		}
	}
}

func TestImagesList_TolerantOfHistoryFailure(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)
	indexPath := filepath.Join(paths.Talon.Dir, "images", "index.json")
	_ = os.MkdirAll(filepath.Dir(indexPath), 0o700)
	_ = os.WriteFile(indexPath, []byte(`{"items":[{"filename":"a.png","type":"output","createdAtMs":1}]}`), 0o600)

	stub := &stubComfyUI{
		historyAll: func(_ context.Context, _ int) ([]comfyui.HistoryListEntry, error) {
			return nil, errors.New("comfy down")
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	res, ferr := h.handleList(t.Context(), HandlerCtx{}, []byte(`{}`))
	if ferr != nil {
		t.Fatalf("list should not fail when history is down: %+v", ferr)
	}
	if len(res.(map[string]any)["images"].([]imagesListItem)) != 1 {
		t.Errorf("expected 1 indexed image to surface, got %+v", res)
	}
}

// --- images.delete ---------------------------------------------------------

func TestImagesDelete_RemovesFromIndex(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)
	indexPath := filepath.Join(paths.Talon.Dir, "images", "index.json")
	_ = os.MkdirAll(filepath.Dir(indexPath), 0o700)
	_ = os.WriteFile(indexPath, []byte(`{"items":[
		{"filename":"keep.png","type":"output"},
		{"filename":"goner.png","type":"output"}
	]}`), 0o600)

	h := NewImagesHandler(paths)
	res, ferr := h.handleDelete(t.Context(), HandlerCtx{}, []byte(`{"filename":"goner.png"}`))
	if ferr != nil {
		t.Fatalf("delete: %+v", ferr)
	}
	if res.(map[string]any)["removed"] != 1 {
		t.Errorf("removed count: %+v", res)
	}
	raw, _ := os.ReadFile(indexPath)
	if strings.Contains(string(raw), "goner.png") {
		t.Errorf("goner.png still in index after delete: %s", raw)
	}
	if !strings.Contains(string(raw), "keep.png") {
		t.Errorf("keep.png was wrongly removed: %s", raw)
	}
}

func TestImagesDelete_NoIndexEntryStillReturnsOK(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)
	h := NewImagesHandler(paths)
	res, ferr := h.handleDelete(t.Context(), HandlerCtx{}, []byte(`{"filename":"never-existed.png"}`))
	if ferr != nil {
		t.Fatalf("delete: %+v", ferr)
	}
	if res.(map[string]any)["ok"] != true {
		t.Errorf("ok flag missing: %+v", res)
	}
}

// --- images.fetch ----------------------------------------------------------

func TestImagesFetch_ReturnsBase64AndDataURL(t *testing.T) {
	paths := readFixture(t, `{}`)
	_ = os.WriteFile(paths.Talon.Config, []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`), 0o600)

	stub := &stubComfyUI{
		fetch: func(_ context.Context, ref comfyui.ImageRef, _ string) ([]byte, string, error) {
			if ref.Filename != "out.png" || ref.Type != "output" {
				return nil, "", fmt.Errorf("unexpected ref: %+v", ref)
			}
			return []byte{0x89, 'P', 'N', 'G'}, "image/png", nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	res, ferr := h.handleFetch(t.Context(), HandlerCtx{}, []byte(`{"filename":"out.png","type":"output"}`))
	if ferr != nil {
		t.Fatalf("fetch: %+v", ferr)
	}
	m := res.(map[string]any)
	if m["contentType"] != "image/png" || m["size"].(int) != 4 {
		t.Errorf("envelope wrong: %+v", m)
	}
	if !strings.HasPrefix(m["dataUrl"].(string), "data:image/png;base64,") {
		t.Errorf("dataUrl missing prefix: %v", m["dataUrl"])
	}
}
