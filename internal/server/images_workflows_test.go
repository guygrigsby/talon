package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guygrigsby/talon/internal/comfyui"
)

func TestImagesWorkflowsList_ListsBuiltins(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	res, ferr := h.handleWorkflowsList(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("handleWorkflowsList: %+v", ferr)
	}
	got, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("response not a map: %#v", res)
	}
	wfs, ok := got["workflows"].([]imagesWorkflowEntry)
	if !ok {
		t.Fatalf("workflows not a typed slice: %#v", got["workflows"])
	}
	// Both shipped builtins surface, with stable ids the UI sends back.
	want := map[string]bool{
		"dixar-character":        false,
		"dixar-character-hyper8": false,
		"dixar-3d":               false,
		"dixar-3d-hyper8":        false,
		"pony-cyberrealistic":    false,
		"pony-real-anime":        false,
	}
	for _, w := range wfs {
		if _, named := want[w.ID]; named {
			want[w.ID] = true
			if w.Source != "builtin" {
				t.Errorf("%s should have source=builtin, got %q", w.ID, w.Source)
			}
			if w.Label == "" || w.Description == "" {
				t.Errorf("%s missing label/description: %+v", w.ID, w)
			}
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("expected builtin %q in workflows list", id)
		}
	}
}

func TestImagesWorkflowsList_OmitsUserDefaultWhenFileMissing(t *testing.T) {
	paths := readFixture(t, "{}")
	// No workflow file exists at the default path; the user-row
	// should be elided (only builtins remain).
	h := NewImagesHandler(paths)
	res, ferr := h.handleWorkflowsList(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("handleWorkflowsList: %+v", ferr)
	}
	wfs := res.(map[string]any)["workflows"].([]imagesWorkflowEntry)
	for _, w := range wfs {
		if w.Source == "user" {
			t.Errorf("user row should be omitted when file missing: %+v", w)
		}
	}
}

func TestImagesWorkflowsList_IncludesUserDefaultWhenFileExists(t *testing.T) {
	paths := readFixture(t, "{}")
	// Write a workflow file at the default location AND configure
	// images.providers.comfyui.workflow.promptNodeId so the user row
	// passes validation. No need to wire negative/seed for the list
	// surface.
	talonImagesDir := filepath.Join(paths.Talon.Dir, "images")
	if err := os.MkdirAll(talonImagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wfPath := filepath.Join(talonImagesDir, "comfyui-default.json")
	if err := os.WriteFile(wfPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(`{"images":{"providers":{"comfyui":{"workflow":{"promptNodeId":"6"}}}}}`)
	if err := os.WriteFile(paths.Talon.Config, cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewImagesHandler(paths)
	res, _ := h.handleWorkflowsList(context.Background(), HandlerCtx{}, nil)
	wfs := res.(map[string]any)["workflows"].([]imagesWorkflowEntry)

	var userRow *imagesWorkflowEntry
	for i, w := range wfs {
		if w.Source == "user" {
			userRow = &wfs[i]
			break
		}
	}
	if userRow == nil {
		t.Fatalf("expected a user row when default workflow file exists; got %+v", wfs)
	}
	if userRow.ID != "" {
		t.Errorf("user row id should be empty (sentinel), got %q", userRow.ID)
	}
	if !strings.Contains(userRow.Description, wfPath) {
		t.Errorf("user row should mention the workflow path: %+v", userRow)
	}
}

func TestLoadComfyUIConfig_BuiltinReturnsEmbeddedJSON(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cfg, ferr := h.loadComfyUIConfig("dixar-character")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig builtin: %+v", ferr)
	}
	if len(cfg.WorkflowJSON) == 0 {
		t.Fatal("WorkflowJSON should be populated for builtin")
	}
	if cfg.WorkflowPath != "" {
		t.Errorf("WorkflowPath should be empty for builtin (got %q)", cfg.WorkflowPath)
	}
	// Pinned node ids from the registry should match the dixar JSON's
	// CLIPTextEncode + KSampler nodes.
	if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != "5" {
		t.Errorf("builtin node ids: %+v", cfg)
	}
	// Sanity check: the embedded JSON contains the dixar checkpoint
	// reference so a wrong-file mix-up surfaces here, not at runtime.
	if !strings.Contains(string(cfg.WorkflowJSON), "dixar_4DixGalore") {
		t.Errorf("dixar checkpoint missing from embedded JSON: %s", cfg.WorkflowJSON)
	}
}

func TestLoadComfyUIConfig_UnknownBuiltinErrors(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	_, ferr := h.loadComfyUIConfig("not-a-real-id")
	if ferr == nil {
		t.Fatal("expected error for unknown workflowId")
	}
	if ferr.Code != ErrCodeBadRequest {
		t.Errorf("expected BadRequest, got %s", ferr.Code)
	}
	if !strings.Contains(ferr.Message, "not-a-real-id") {
		t.Errorf("error should name the bad id: %s", ferr.Message)
	}
}

func TestLoadComfyUIConfig_3DBuiltinsLoadCorrectly(t *testing.T) {
	// The 3D-LoRA variants share the same prompt/negative/seed pins
	// as the character workflows but add a LoRA stack on top. Verify
	// the embedded JSON for each loads, the pins resolve, and the
	// 3d LoRA reference survives — the latter catching any wrong-file
	// mix-up between the two 3D variants and the character ones.
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cases := []struct {
		id          string
		wantSubstr  string
		description string
	}{
		{"dixar-3d", "3d.safetensors", "30-step 3D"},
		{"dixar-3d-hyper8", "Hyper-SDXL-8steps-lora.safetensors", "Hyper-8 3D"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			cfg, ferr := h.loadComfyUIConfig(c.id)
			if ferr != nil {
				t.Fatalf("%s: %+v", c.description, ferr)
			}
			if len(cfg.WorkflowJSON) == 0 {
				t.Fatalf("%s: WorkflowJSON empty", c.description)
			}
			if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != "5" {
				t.Errorf("%s: pin mismatch: %+v", c.description, cfg)
			}
			if !strings.Contains(string(cfg.WorkflowJSON), c.wantSubstr) {
				t.Errorf("%s: workflow JSON missing %q", c.description, c.wantSubstr)
			}
			// Patch end-to-end so a structural change in the JSON
			// (e.g. a renamed node) trips this assertion before
			// shipping.
			seed := int64(7)
			out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "p", NegativePrompt: "n", Seed: &seed})
			if ferr != nil {
				t.Fatalf("%s: patch: %+v", c.description, ferr)
			}
			var parsed map[string]struct {
				Inputs map[string]any `json:"inputs"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["2"].Inputs["text"] != "p" {
				t.Errorf("%s: positive not patched", c.description)
			}
			if parsed["3"].Inputs["text"] != "n" {
				t.Errorf("%s: negative not patched", c.description)
			}
			if int64(parsed["5"].Inputs["seed"].(float64)) != 7 {
				t.Errorf("%s: seed not patched", c.description)
			}
		})
	}
}

func TestLoadComfyUIConfig_PonyBuiltinsLoadCorrectly(t *testing.T) {
	// Pony variants use distinct checkpoints from the dixar set —
	// verify each loads the right .safetensors so a registry/file
	// mix-up surfaces here, not in production.
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cases := []struct {
		id          string
		wantSubstr  string
		description string
	}{
		{"pony-cyberrealistic", "cyberrealisticPony_v170.safetensors", "CyberRealistic Pony"},
		{"pony-real-anime", "realPony_realAnimeNo04.safetensors", "Real Anime Pony"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			cfg, ferr := h.loadComfyUIConfig(c.id)
			if ferr != nil {
				t.Fatalf("%s: %+v", c.description, ferr)
			}
			if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != "5" {
				t.Errorf("%s: pin mismatch: %+v", c.description, cfg)
			}
			if !strings.Contains(string(cfg.WorkflowJSON), c.wantSubstr) {
				t.Errorf("%s: workflow JSON missing checkpoint %q", c.description, c.wantSubstr)
			}
			// Patch end-to-end so a structural break (e.g. KSampler
			// renamed off node 5) trips before shipping.
			seed := int64(99)
			out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "a", NegativePrompt: "b", Seed: &seed})
			if ferr != nil {
				t.Fatalf("%s: patch: %+v", c.description, ferr)
			}
			var parsed map[string]struct {
				Inputs map[string]any `json:"inputs"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["2"].Inputs["text"] != "a" {
				t.Errorf("%s: positive not patched", c.description)
			}
			if parsed["3"].Inputs["text"] != "b" {
				t.Errorf("%s: negative not patched", c.description)
			}
			if int64(parsed["5"].Inputs["seed"].(float64)) != 99 {
				t.Errorf("%s: seed not patched", c.description)
			}
			// Pony permanent-anchor scaffolding survives the patch:
			// node 11 is the positive ConditioningConcat, node 13 the
			// negative one. If these get clobbered the score-tag
			// anchors stop applying and quality collapses.
			if parsed["11"].Inputs["conditioning_to"] == nil {
				t.Errorf("%s: positive concat scaffold lost", c.description)
			}
			if parsed["13"].Inputs["conditioning_to"] == nil {
				t.Errorf("%s: negative concat scaffold lost", c.description)
			}
		})
	}
}

func TestReadAndPatchWorkflow_BuiltinPatchesEmbeddedJSON(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)
	cfg, ferr := h.loadComfyUIConfig("dixar-character-hyper8")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig: %+v", ferr)
	}
	seed := int64(123)
	out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{
		Prompt:         "a knight",
		NegativePrompt: "blurry",
		Seed:           &seed,
	})
	if ferr != nil {
		t.Fatalf("patch: %+v", ferr)
	}
	var parsed map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["2"].Inputs["text"] != "a knight" {
		t.Errorf("positive prompt not patched: %+v", parsed["2"].Inputs)
	}
	if parsed["3"].Inputs["text"] != "blurry" {
		t.Errorf("negative prompt not patched: %+v", parsed["3"].Inputs)
	}
	if int64(parsed["5"].Inputs["seed"].(float64)) != 123 {
		t.Errorf("seed not patched: %+v", parsed["5"].Inputs)
	}
	// Untouched fields preserved — verify the Hyper LoRA node still
	// has its lora_name so a regression that destroys the rest of
	// the workflow surfaces here.
	if got := parsed["8"].Inputs["lora_name"]; got != "Hyper-SDXL-8steps-lora.safetensors" {
		t.Errorf("Hyper LoRA node lost: %+v", parsed["8"].Inputs)
	}
}

func TestImagesGenerate_AcceptsWorkflowID(t *testing.T) {
	// End-to-end: the handler honors p.WorkflowID and builds a cfg
	// with the embedded JSON. We verify by stubbing Submit and
	// asserting the submitted workflow has the dixar checkpoint —
	// confirming the builtin file made it all the way through.
	paths := readFixture(t, "{}")

	var (
		mu             sync.Mutex
		submitted      json.RawMessage
		submitReceived = make(chan struct{}, 1)
	)
	stub := &stubComfyUI{
		submit: func(_ context.Context, w json.RawMessage, _ string) (*comfyui.SubmitResult, error) {
			mu.Lock()
			submitted = append([]byte(nil), w...)
			mu.Unlock()
			select {
			case submitReceived <- struct{}{}:
			default:
			}
			// Trigger a fast successful run so runGenerate exits cleanly.
			return &comfyui.SubmitResult{PromptID: "p1"}, nil
		},
		events: func(_ context.Context, _ string) (<-chan comfyui.Event, <-chan error, error) {
			ev := make(chan comfyui.Event)
			er := make(chan error, 1)
			close(ev) // EOS: handler exits without final image — fine for this assertion.
			return ev, er, nil
		},
		history: func(_ context.Context, _ string) (*comfyui.HistoryEntry, error) {
			return nil, nil
		},
		fetch: func(_ context.Context, _ comfyui.ImageRef, _ string) ([]byte, string, error) {
			return nil, "", nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	// Discard the emit calls — we're asserting on what hit Submit.
	h.WithEmit(func(_ *Session, _, _, _ string, _ map[string]any) error { return nil })

	params := imagesGenerateParams{
		SessionKey: "main|s1",
		Prompt:     "test",
		WorkflowID: "dixar-character",
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, ferr := h.handleGenerate(context.Background(), HandlerCtx{}, body)
	if ferr != nil {
		t.Fatalf("generate: %+v", ferr)
	}
	if _, ok := res.(map[string]any)["runId"]; !ok {
		t.Fatalf("missing runId: %+v", res)
	}
	select {
	case <-submitReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit was not called within 2s")
	}
	mu.Lock()
	got := submitted
	mu.Unlock()
	if !strings.Contains(string(got), "dixar_4DixGalore") {
		t.Errorf("submitted workflow missing dixar checkpoint: %s", got)
	}
}
