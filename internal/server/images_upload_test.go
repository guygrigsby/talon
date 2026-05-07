package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/comfyui"
)

// TestImagesUpload_HappyPath verifies the RPC decodes the base64
// body, forwards it to comfyui.Upload with the right options, and
// returns the resolved filename + subfolder + type.
func TestImagesUpload_HappyPath(t *testing.T) {
	paths := readFixture(t, "{}")

	var (
		gotName string
		gotBody []byte
		gotOpts comfyui.UploadOptions
	)
	stub := &stubComfyUI{
		upload: func(_ context.Context, name string, body []byte, opts comfyui.UploadOptions) (*comfyui.UploadResult, error) {
			gotName, gotBody, gotOpts = name, body, opts
			// ComfyUI auto-suffixes on collision — exercise that path.
			return &comfyui.UploadResult{
				Filename:  name + " (1)",
				Subfolder: opts.Subfolder,
				Type:      "input",
			}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })

	body := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'p', 'a', 'y'}
	params, _ := json.Marshal(imagesUploadParams{
		Filename:    "src.png",
		Subfolder:   "uploads",
		Type:        "input",
		ContentType: "image/png",
		Base64:      base64.StdEncoding.EncodeToString(body),
	})
	res, ferr := h.handleUpload(context.Background(), HandlerCtx{}, params)
	if ferr != nil {
		t.Fatalf("upload: %+v", ferr)
	}
	got := res.(map[string]any)
	if got["filename"] != "src.png (1)" {
		t.Errorf("filename: %v", got["filename"])
	}
	if got["subfolder"] != "uploads" || got["type"] != "input" {
		t.Errorf("unexpected fields: %+v", got)
	}
	if gotName != "src.png" {
		t.Errorf("name not forwarded: %q", gotName)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body not forwarded: got %d bytes", len(gotBody))
	}
	if gotOpts.ContentType != "image/png" || gotOpts.Subfolder != "uploads" {
		t.Errorf("opts not forwarded: %+v", gotOpts)
	}
}

func TestImagesUpload_RejectsMissingFields(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return &stubComfyUI{} })

	cases := []imagesUploadParams{
		{Base64: "AAA="},                          // no filename
		{Filename: "x.png"},                       // no body
		{Filename: "x.png", Base64: "not-base64!"}, // bad base64
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		_, ferr := h.handleUpload(context.Background(), HandlerCtx{}, body)
		if ferr == nil {
			t.Errorf("expected error for %+v", c)
			continue
		}
		if ferr.Code != ErrCodeBadRequest {
			t.Errorf("expected BAD_REQUEST for %+v, got %s", c, ferr.Code)
		}
	}
}

func TestImagesUpload_PropagatesComfyUIError(t *testing.T) {
	paths := readFixture(t, "{}")
	stub := &stubComfyUI{
		upload: func(_ context.Context, _ string, _ []byte, _ comfyui.UploadOptions) (*comfyui.UploadResult, error) {
			return nil, errors.New("disk full")
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })

	params, _ := json.Marshal(imagesUploadParams{
		Filename: "x.png",
		Base64:   base64.StdEncoding.EncodeToString([]byte("data")),
	})
	_, ferr := h.handleUpload(context.Background(), HandlerCtx{}, params)
	if ferr == nil {
		t.Fatal("expected error")
	}
	if ferr.Code != ErrCodeInternal {
		t.Errorf("expected INTERNAL, got %s", ferr.Code)
	}
	if !strings.Contains(ferr.Message, "disk full") {
		t.Errorf("error not propagated: %s", ferr.Message)
	}
}

func TestImagesObjectInfo_ProxiesResponse(t *testing.T) {
	paths := readFixture(t, "{}")
	stub := &stubComfyUI{
		objectInfo: func(_ context.Context) (comfyui.ObjectInfo, error) {
			return comfyui.ObjectInfo{
				"LoraLoader": json.RawMessage(`{"input":{"required":{"lora_name":[["a.safetensors","b.safetensors"]]}}}`),
			}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })

	res, ferr := h.handleObjectInfo(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("objectInfo: %+v", ferr)
	}
	got := res.(map[string]any)
	info, ok := got["objectInfo"].(comfyui.ObjectInfo)
	if !ok {
		t.Fatalf("objectInfo missing or wrong type: %+v", got)
	}
	if _, ok := info["LoraLoader"]; !ok {
		t.Errorf("LoraLoader entry missing: %+v", info)
	}
}

func TestImagesManagerStatus_Proxies(t *testing.T) {
	paths := readFixture(t, "{}")
	stub := &stubComfyUI{
		managerStatus: func(_ context.Context) (*comfyui.ManagerStatus, error) {
			return &comfyui.ManagerStatus{Present: true, Endpoint: "/customnode/getmappings"}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })

	res, ferr := h.handleManagerStatus(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("status: %+v", ferr)
	}
	got := res.(map[string]any)
	if got["present"] != true {
		t.Errorf("present: %v", got["present"])
	}
	if got["endpoint"] != "/customnode/getmappings" {
		t.Errorf("endpoint: %v", got["endpoint"])
	}
}

func TestImagesManagerInstall_HappyPath(t *testing.T) {
	paths := readFixture(t, "{}")
	var got comfyui.ManagerInstallRequest
	stub := &stubComfyUI{
		managerInstall: func(_ context.Context, req comfyui.ManagerInstallRequest) (*comfyui.ManagerInstallResult, error) {
			got = req
			return &comfyui.ManagerInstallResult{OK: true, Message: "queued"}, nil
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })

	body, _ := json.Marshal(imagesManagerInstallParams{
		Type:     "loras",
		URL:      "https://civitai.com/api/download/models/12345",
		Filename: "simpsons_xl.safetensors",
		SavePath: "loras",
	})
	res, ferr := h.handleManagerInstall(context.Background(), HandlerCtx{}, body)
	if ferr != nil {
		t.Fatalf("install: %+v", ferr)
	}
	out := res.(map[string]any)
	if out["ok"] != true {
		t.Errorf("ok: %v", out["ok"])
	}
	if out["message"] != "queued" {
		t.Errorf("message: %v", out["message"])
	}
	if got.Type != "loras" || got.Filename != "simpsons_xl.safetensors" {
		t.Errorf("forwarded incorrectly: %+v", got)
	}
}

func TestImagesManagerInstall_RejectsMissingFields(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return &stubComfyUI{} })

	cases := []imagesManagerInstallParams{
		{URL: "https://x"},        // no type
		{Type: "loras"},           // no url
		{Type: " ", URL: "https://x"}, // blank type
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		_, ferr := h.handleManagerInstall(context.Background(), HandlerCtx{}, body)
		if ferr == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestImagesManagerInstall_PropagatesError(t *testing.T) {
	paths := readFixture(t, "{}")
	stub := &stubComfyUI{
		managerInstall: func(_ context.Context, _ comfyui.ManagerInstallRequest) (*comfyui.ManagerInstallResult, error) {
			return nil, errors.New("manager not installed")
		},
	}
	h := NewImagesHandler(paths).WithDial(func(string) ComfyUIClient { return stub })
	body, _ := json.Marshal(imagesManagerInstallParams{Type: "loras", URL: "https://x"})
	_, ferr := h.handleManagerInstall(context.Background(), HandlerCtx{}, body)
	if ferr == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(ferr.Message, "manager not installed") {
		t.Errorf("error not propagated: %s", ferr.Message)
	}
}

// TestImg2ImgWorkflow_LoadsAndPatches verifies the new builtin
// img2img workflow loads, has its pinned nodes resolved, and patches
// prompt/negative/seed without breaking the LoadImage + VAEEncode +
// KSampler chain that makes it img2img.
func TestImg2ImgWorkflow_LoadsAndPatches(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewImagesHandler(paths)

	cfg, ferr := h.loadComfyUIConfig("img2img-pony")
	if ferr != nil {
		t.Fatalf("loadComfyUIConfig: %+v", ferr)
	}
	if cfg.PromptNodeID != "2" || cfg.NegativePromptNodeID != "3" || cfg.SeedNodeID != "6" {
		t.Errorf("pin mismatch: %+v", cfg)
	}
	seed := int64(7)
	out, ferr := readAndPatchWorkflow(cfg, imagesGenerateParams{
		Prompt:         "p",
		NegativePrompt: "n",
		Seed:           &seed,
		// Override the LoadImage node with the uploaded filename, the
		// way the Studio UI will when it submits an img2img run.
		NodeOverrides: map[string]map[string]any{
			"4": {"image": "uploaded.png"},
		},
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
		t.Errorf("positive: %+v", parsed["2"].Inputs)
	}
	if parsed["3"].Inputs["text"] != "n" {
		t.Errorf("negative: %+v", parsed["3"].Inputs)
	}
	if int64(parsed["6"].Inputs["seed"].(float64)) != 7 {
		t.Errorf("seed: %+v", parsed["6"].Inputs)
	}
	if parsed["4"].Inputs["image"] != "uploaded.png" {
		t.Errorf("LoadImage not patched via override: %+v", parsed["4"].Inputs)
	}
	// Structural integrity: the chain LoadImage → VAEEncode →
	// KSampler.latent_image must survive.
	if parsed["4"].ClassType != "LoadImage" {
		t.Errorf("node 4 class_type: %q", parsed["4"].ClassType)
	}
	if parsed["5"].ClassType != "VAEEncode" {
		t.Errorf("node 5 class_type: %q", parsed["5"].ClassType)
	}
	li, ok := parsed["6"].Inputs["latent_image"].([]any)
	if !ok || len(li) != 2 || li[0] != "5" {
		t.Errorf("KSampler latent_image not wired to VAEEncode: %v", parsed["6"].Inputs["latent_image"])
	}
	// Denoise default should come through (UI may override).
	if d, _ := parsed["6"].Inputs["denoise"].(float64); d <= 0 || d >= 1 {
		t.Errorf("denoise out of img2img range: %v", parsed["6"].Inputs["denoise"])
	}
}
