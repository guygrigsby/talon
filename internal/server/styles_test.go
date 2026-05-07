package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStylesList_ReturnsBuiltins(t *testing.T) {
	paths := readFixture(t, "{}")
	h := NewStylesHandler(paths)
	res, ferr := h.handleList(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("list: %+v", ferr)
	}
	got := res.(map[string]any)["styles"].([]StylePreset)
	want := map[string]bool{
		"simpsons": false, "clone-wars": false, "studio-ghibli": false,
		"pixar": false, "watercolor": false, "oil-painting": false,
		"comic-book": false, "manga": false, "low-poly": false,
		"pixel-art": false,
	}
	for _, s := range got {
		if _, named := want[s.ID]; named {
			want[s.ID] = true
			if s.Source != "builtin" {
				t.Errorf("%s should be source=builtin, got %q", s.ID, s.Source)
			}
			if s.Label == "" || s.PromptSuffix == "" {
				t.Errorf("%s missing label/promptSuffix: %+v", s.ID, s)
			}
			if s.Denoise <= 0 || s.Denoise >= 1 {
				t.Errorf("%s denoise out of range: %v", s.ID, s.Denoise)
			}
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("expected builtin style %q", id)
		}
	}
}

func TestStylesList_UserOverlayShadowsBuiltin(t *testing.T) {
	paths := readFixture(t, "{}")
	imagesDir := filepath.Join(paths.Talon.Dir, "images")
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := []byte(`{"styles":[{"id":"simpsons","label":"My Simpsons","promptSuffix":"my override","denoise":0.5}]}`)
	if err := os.WriteFile(filepath.Join(imagesDir, "styles.json"), overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewStylesHandler(paths)
	res, _ := h.handleList(context.Background(), HandlerCtx{}, nil)
	got := res.(map[string]any)["styles"].([]StylePreset)
	var sim *StylePreset
	for i, s := range got {
		if s.ID == "simpsons" {
			sim = &got[i]
			break
		}
	}
	if sim == nil {
		t.Fatal("simpsons missing")
	}
	if sim.Source != "user" {
		t.Errorf("expected source=user after shadow, got %q", sim.Source)
	}
	if sim.Label != "My Simpsons" {
		t.Errorf("user label not used: %q", sim.Label)
	}
}

func TestStylesList_AddsUserStyle(t *testing.T) {
	paths := readFixture(t, "{}")
	imagesDir := filepath.Join(paths.Talon.Dir, "images")
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := []byte(`{"styles":[{"id":"my-style","label":"Mine","promptSuffix":", custom","denoise":0.6}]}`)
	if err := os.WriteFile(filepath.Join(imagesDir, "styles.json"), overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewStylesHandler(paths)
	res, _ := h.handleList(context.Background(), HandlerCtx{}, nil)
	got := res.(map[string]any)["styles"].([]StylePreset)
	// User entry should sort before any builtin.
	if got[0].ID != "my-style" {
		t.Errorf("user entry should sort first: %+v", got[0])
	}
}

func TestStylesList_TolerantOfMalformedOverlay(t *testing.T) {
	paths := readFixture(t, "{}")
	imagesDir := filepath.Join(paths.Talon.Dir, "images")
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "styles.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewStylesHandler(paths)
	res, ferr := h.handleList(context.Background(), HandlerCtx{}, nil)
	if ferr != nil {
		t.Fatalf("malformed overlay should not fail the call: %+v", ferr)
	}
	got := res.(map[string]any)["styles"].([]StylePreset)
	if len(got) == 0 {
		t.Error("builtins should still surface when overlay is bad")
	}
}

func TestInstalledLoras_NestedShape(t *testing.T) {
	// ComfyUI's standard shape: lora_name is [[<names>], <metadata>].
	info := map[string]json.RawMessage{
		"LoraLoader": json.RawMessage(`{"input":{"required":{"lora_name":[["a.safetensors","b.safetensors","c.safetensors"],{"tooltip":"…"}]}}}`),
	}
	got := InstalledLoras(info)
	if len(got) != 3 {
		t.Fatalf("expected 3 LoRAs, got %d: %+v", len(got), got)
	}
	for _, want := range []string{"a.safetensors", "b.safetensors", "c.safetensors"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from set", want)
		}
	}
}

func TestInstalledLoras_MissingNodeReturnsEmpty(t *testing.T) {
	got := InstalledLoras(map[string]json.RawMessage{})
	if len(got) != 0 {
		t.Errorf("expected empty set, got %+v", got)
	}
}

func TestInstalledLoras_MalformedShape(t *testing.T) {
	// Malformed payload should not panic and should return empty.
	info := map[string]json.RawMessage{
		"LoraLoader": json.RawMessage(`"not an object"`),
	}
	got := InstalledLoras(info)
	if len(got) != 0 {
		t.Errorf("expected empty set on malformed, got %+v", got)
	}
}

func TestStylesList_ResponseMarshalsCleanly(t *testing.T) {
	// Round-trip through JSON to make sure StylePreset shape (camelCase
	// json tags, omitempty) holds — the UI consumes this directly.
	paths := readFixture(t, "{}")
	h := NewStylesHandler(paths)
	res, _ := h.handleList(context.Background(), HandlerCtx{}, nil)
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	styles, ok := back["styles"].([]any)
	if !ok || len(styles) == 0 {
		t.Fatalf("response shape wrong: %+v", back)
	}
	first := styles[0].(map[string]any)
	if _, ok := first["promptSuffix"]; !ok {
		t.Errorf("missing promptSuffix field: %+v", first)
	}
}
