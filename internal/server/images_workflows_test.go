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
	// Public-checkpoint builtins surface, with stable ids the UI sends back.
	want := map[string]bool{
		"pony-cyberrealistic":       false,
		"pony-cyberrealistic-tiled": false,
		"pony-real-anime":           false,
		"illustrious-char":          false,
		"illustrious-hyper":         false,
		"img2img-pony":              false,
		"sdxl-juggernaut":           false,
		"img2img-juggernaut":        false,
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
	// No workflow file exists at the default path AND no user-dir
	// workflows; the user-row should be elided (only builtins remain).
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

	// The legacy default uses ID "" as a sentinel. User-dir entries
	// (also Source="user") have non-empty IDs, so this test stays
	// specific to the legacy row even with discovery layered on.
	var legacyRow *imagesWorkflowEntry
	for i, w := range wfs {
		if w.Source == "user" && w.ID == "" {
			legacyRow = &wfs[i]
			break
		}
	}
	if legacyRow == nil {
		t.Fatalf("expected a legacy default user row when default workflow file exists; got %+v", wfs)
	}
	if !strings.Contains(legacyRow.Description, wfPath) {
		t.Errorf("legacy default row should mention the workflow path: %+v", legacyRow)
	}
}

func TestImagesWorkflowsList_DiscoversUserDirEntries(t *testing.T) {
	// User-dir discovery: a workflow JSON paired with a sidecar
	// .meta.json under ~/.talon/images/workflows/ shows up in the
	// list as Source=user with the meta-defined id/label/description.
	paths := readFixture(t, "{}")
	wfDir := filepath.Join(paths.Talon.Dir, "images", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Minimal valid workflow + sidecar — only the registry path is
	// under test, not workflow patching.
	if err := os.WriteFile(filepath.Join(wfDir, "myflow.json"),
		[]byte(`{"6":{"inputs":{"text":"%prompt%"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := []byte(`{"id":"my-flow","label":"My Flow","description":"a test","promptNodeId":"6"}`)
	if err := os.WriteFile(filepath.Join(wfDir, "myflow.meta.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewImagesHandler(paths)
	res, ferr := h.handleWorkflowsList(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("list: %+v", ferr)
	}
	wfs := res.(map[string]any)["workflows"].([]imagesWorkflowEntry)
	var hit *imagesWorkflowEntry
	for i, w := range wfs {
		if w.ID == "my-flow" {
			hit = &wfs[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("user-dir workflow my-flow missing from list: %+v", wfs)
	}
	if hit.Source != "user" {
		t.Errorf("user-dir workflow should have Source=user, got %q", hit.Source)
	}
	if hit.Label != "My Flow" || hit.Description != "a test" {
		t.Errorf("user-dir workflow metadata mismatch: %+v", hit)
	}
}

func TestImagesWorkflowsList_SkipsUserDirEntryWithoutSidecar(t *testing.T) {
	// A workflow JSON without its <id>.meta.json is skipped — the
	// patcher needs the node-id pins, so silently registering the
	// row would land bogus pins on submit.
	paths := readFixture(t, "{}")
	wfDir := filepath.Join(paths.Talon.Dir, "images", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "noemeta.json"),
		[]byte(`{"6":{"inputs":{"text":"%prompt%"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewImagesHandler(paths)
	res, _ := h.handleWorkflowsList(context.Background(), HandlerCtx{}, nil)
	wfs := res.(map[string]any)["workflows"].([]imagesWorkflowEntry)
	for _, w := range wfs {
		if w.ID == "noemeta" {
			t.Errorf("workflow without sidecar should be skipped: %+v", w)
		}
	}
}

func TestLoadComfyUIConfig_BuiltinReturnsEmbeddedJSON(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cfg, ferr := h.loadComfyUIConfig("pony-cyberrealistic")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig builtin: %+v", ferr)
	}
	if len(cfg.WorkflowJSON) == 0 {
		t.Fatal("WorkflowJSON should be populated for builtin")
	}
	if cfg.WorkflowPath != "" {
		t.Errorf("WorkflowPath should be empty for builtin (got %q)", cfg.WorkflowPath)
	}
	// Pinned node ids from the registry should match the JSON's
	// CLIPTextEncode + KSampler nodes.
	if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != "5" {
		t.Errorf("builtin node ids: %+v", cfg)
	}
	// Sanity check: the embedded JSON contains the checkpoint
	// reference so a wrong-file mix-up surfaces here, not at runtime.
	if !strings.Contains(string(cfg.WorkflowJSON), "cyberrealisticPony_v170") {
		t.Errorf("checkpoint missing from embedded JSON: %s", cfg.WorkflowJSON)
	}
}

func TestLoadComfyUIConfig_UserDirEntryReturnsDiskJSON(t *testing.T) {
	// User-dir entry: loadComfyUIConfig should resolve the id, read
	// the JSON from disk, and copy the meta-defined pins onto cfg.
	paths := readFixture(t, "{}")
	wfDir := filepath.Join(paths.Talon.Dir, "images", "workflows")
	if err := os.MkdirAll(wfDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"6":{"inputs":{"text":"%prompt%"}},"7":{"inputs":{"text":"%neg%"}},"9":{"inputs":{"seed":"%seed%"}}}`)
	if err := os.WriteFile(filepath.Join(wfDir, "ud.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	meta := []byte(`{"id":"ud","label":"UD","promptNodeId":"6","negativePromptNodeId":"7","seedNodeId":"9"}`)
	if err := os.WriteFile(filepath.Join(wfDir, "ud.meta.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewImagesHandler(paths)
	cfg, ferr := h.loadComfyUIConfig("ud")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig user-dir: %+v", ferr)
	}
	if len(cfg.WorkflowJSON) == 0 {
		t.Fatal("WorkflowJSON should be populated for user-dir entry")
	}
	if cfg.PromptNodeID != "6" || cfg.NegativePromptNodeID != "7" || cfg.SeedNodeID != "9" {
		t.Errorf("user-dir pins not loaded: %+v", cfg)
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

func TestLoadComfyUIConfig_PonyBuiltinsLoadCorrectly(t *testing.T) {
	// Pony variants ship as public-checkpoint builtins. Verify each
	// loads the right .safetensors so a registry/file mix-up surfaces
	// here, not in production.
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

func TestLoadComfyUIConfig_IllustriousBuiltinsLoadCorrectly(t *testing.T) {
	// Illustrious workflows use prompt=6/negative=7/seed=3 (vs.
	// 2/3/5 for the pony set). A registry typo would land the
	// patcher on the wrong nodes and swap prompt/negative silently
	// — verify the right pins resolve and the right checkpoint is
	// in the embedded JSON.
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cases := []struct {
		id          string
		wantSubstr  string
		description string
	}{
		{"illustrious-char", "illustriousXL10Improved_v30", "Illustrious Character"},
		{"illustrious-hyper", "Hyper-SDXL-8steps-lora.safetensors", "Illustrious Hyper-8"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			cfg, ferr := h.loadComfyUIConfig(c.id)
			if ferr != nil {
				t.Fatalf("%s: %+v", c.description, ferr)
			}
			if cfg.PromptNodeID != "6" || cfg.NegativePromptNodeID != "7" || cfg.SeedNodeID != "3" {
				t.Errorf("%s: pin mismatch (Illustrious uses 6/7/3): %+v", c.description, cfg)
			}
			if !strings.Contains(string(cfg.WorkflowJSON), c.wantSubstr) {
				t.Errorf("%s: workflow JSON missing %q", c.description, c.wantSubstr)
			}
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
			// Illustrious-hyper's positive node 6 has a baked-in
			// "masterpiece, best quality, ..., %prompt%" prefix —
			// patching replaces the whole string, so the prefix is
			// expected to be gone after patch. We only assert that
			// the user prompt landed.
			if !strings.Contains(parsed["6"].Inputs["text"].(string), "p") {
				t.Errorf("%s: positive not patched on node 6: %+v", c.description, parsed["6"].Inputs)
			}
			if parsed["7"].Inputs["text"] != "n" {
				t.Errorf("%s: negative not patched on node 7", c.description)
			}
			if int64(parsed["3"].Inputs["seed"].(float64)) != 7 {
				t.Errorf("%s: seed not patched on node 3", c.description)
			}
			// External VAELoader (node 10) and tiled decode (node 8)
			// must survive — these distinguish Illustrious from the
			// pony workflows.
			if parsed["10"].Inputs["vae_name"] != "sdxl_vae.safetensors" {
				t.Errorf("%s: VAELoader lost: %+v", c.description, parsed["10"].Inputs)
			}
			if _, ok := parsed["8"].Inputs["tile_size"]; !ok {
				t.Errorf("%s: VAEDecodeTiled lost tile_size: %+v", c.description, parsed["8"].Inputs)
			}
		})
	}
}

func TestLoadComfyUIConfig_JuggernautBuiltinsLoadCorrectly(t *testing.T) {
	// Juggernaut workflows ship with the recommended-author settings:
	// dpmpp_2m_sde sampler, karras scheduler, 32 steps, cfg 4, 832x1216
	// portrait. Pin verification + ckpt_name reference + structural
	// integrity after patch.
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cases := []struct {
		id           string
		seedNode     string
		isImg2Img    bool
	}{
		{"sdxl-juggernaut", "5", false},
		{"img2img-juggernaut", "6", true},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			cfg, ferr := h.loadComfyUIConfig(c.id)
			if ferr != nil {
				t.Fatalf("loadComfyUIConfig: %+v", ferr)
			}
			if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != c.seedNode {
				t.Errorf("pin mismatch (expected 2/3/%s): %+v", c.seedNode, cfg)
			}
			if !strings.Contains(string(cfg.WorkflowJSON), "juggernautXL") {
				t.Errorf("workflow JSON missing juggernautXL ckpt reference")
			}
			if !strings.Contains(string(cfg.WorkflowJSON), "dpmpp_2m_sde") {
				t.Errorf("workflow JSON missing dpmpp_2m_sde sampler")
			}
			seed := int64(7)
			out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{
				Prompt: "p", NegativePrompt: "n", Seed: &seed,
			})
			if ferr != nil {
				t.Fatalf("patch: %+v", ferr)
			}
			var parsed map[string]struct {
				ClassType string         `json:"class_type"`
				Inputs    map[string]any `json:"inputs"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed["2"].Inputs["text"] != "p" {
				t.Errorf("positive not patched: %+v", parsed["2"].Inputs)
			}
			if parsed["3"].Inputs["text"] != "n" {
				t.Errorf("negative not patched: %+v", parsed["3"].Inputs)
			}
			if int64(parsed[c.seedNode].Inputs["seed"].(float64)) != 7 {
				t.Errorf("seed not patched on node %s", c.seedNode)
			}
			// img2img variant: LoadImage → VAEEncode → KSampler chain
			// must survive (otherwise img2img degrades to t2i with
			// the wrong latent source).
			if c.isImg2Img {
				if parsed["4"].ClassType != "LoadImage" {
					t.Errorf("img2img: node 4 should be LoadImage, got %q", parsed["4"].ClassType)
				}
				if parsed["5"].ClassType != "VAEEncode" {
					t.Errorf("img2img: node 5 should be VAEEncode, got %q", parsed["5"].ClassType)
				}
				li, ok := parsed[c.seedNode].Inputs["latent_image"].([]any)
				if !ok || len(li) != 2 || li[0] != "5" {
					t.Errorf("img2img: KSampler latent_image not wired to VAEEncode: %v", parsed[c.seedNode].Inputs["latent_image"])
				}
			}
		})
	}
}

// TestLoadComfyUIConfig_TwoPassPatchesAllSeedNodes covers the
// hires-fix bug: pony-cyberrealistic-tiled has TWO KSampler nodes
// (5 = base pass, 18 = polish pass) and both have inputs.seed
// templated as "%seed%". Patching only the primary seed leaves the
// literal string in the second sampler, ComfyUI rejects it as
// non-numeric, and the run fails with execution_error. Both must be
// patched to the same seed (deterministic continuity between passes
// is expected).
func TestLoadComfyUIConfig_TwoPassPatchesAllSeedNodes(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cfg, ferr := h.loadComfyUIConfig("pony-cyberrealistic-tiled")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig pony-cyberrealistic-tiled: %+v", ferr)
	}
	if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" {
		t.Errorf("pony-cyberrealistic-tiled pin mismatch (expected prompt=2/negative=3): %+v", cfg)
	}
	if cfg.SeedNodeID != "5" {
		t.Errorf("pony-cyberrealistic-tiled primary seed pin should be 5 (first KSampler), got %q", cfg.SeedNodeID)
	}
	if len(cfg.ExtraSeedNodeIDs) != 1 || cfg.ExtraSeedNodeIDs[0] != "18" {
		t.Errorf("pony-cyberrealistic-tiled extra seed pin should be [18], got %v", cfg.ExtraSeedNodeIDs)
	}

	seed := int64(2024)
	out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{Prompt: "a", NegativePrompt: "b", Seed: &seed})
	if ferr != nil {
		t.Fatalf("patch: %+v", ferr)
	}
	var parsed map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	// Both seed nodes must be numeric (placeholder gone) AND equal:
	// hires polish uses the primary pass's seed for coherent
	// continuity; if they diverge the second pass drifts.
	for _, nodeID := range []string{"5", "18"} {
		v, ok := parsed[nodeID].Inputs["seed"]
		if !ok {
			t.Fatalf("node %s missing inputs.seed: %+v", nodeID, parsed[nodeID].Inputs)
		}
		if s, isStr := v.(string); isStr {
			t.Errorf("node %s seed still a string %q — placeholder not patched", nodeID, s)
			continue
		}
		f, ok := v.(float64)
		if !ok {
			t.Errorf("node %s seed unexpected type %T: %v", nodeID, v, v)
			continue
		}
		if int64(f) != 2024 {
			t.Errorf("node %s seed = %d, want 2024", nodeID, int64(f))
		}
	}
	// Permanent-anchor scaffolding (concat nodes 11/13) and the
	// upscale chain (14/15/16/17/19) must survive.
	for _, nodeID := range []string{"11", "13", "14", "15", "16", "17", "19"} {
		if parsed[nodeID].Inputs == nil {
			t.Errorf("node %s lost during patch", nodeID)
		}
	}
}

func TestReadAndPatchWorkflow_BuiltinPatchesEmbeddedJSON(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)
	cfg, ferr := h.loadComfyUIConfig("pony-cyberrealistic")
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
	// Untouched fields preserved — verify the checkpoint loader still
	// has its ckpt_name so a regression that destroys the rest of the
	// workflow surfaces here.
	if got := parsed["1"].Inputs["ckpt_name"]; got != "cyberrealisticPony_v170.safetensors" {
		t.Errorf("checkpoint loader lost ckpt_name: %+v", parsed["1"].Inputs)
	}
}

func TestImagesGenerate_AcceptsWorkflowID(t *testing.T) {
	// End-to-end: the handler honors p.WorkflowID and builds a cfg
	// with the embedded JSON. We verify by stubbing Submit and
	// asserting the submitted workflow has the pony checkpoint —
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
		WorkflowID: "pony-cyberrealistic",
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
	if !strings.Contains(string(got), "cyberrealisticPony_v170") {
		t.Errorf("submitted workflow missing pony checkpoint: %s", got)
	}
}
