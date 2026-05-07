package server

// Style preset registry. Each preset is a prompt/negative suffix +
// denoise default that the Studio UI appends to the user's prompt
// when running an img2img workflow. No LoRA install is required —
// presets work with any installed checkpoint, so the feature ships
// without depending on the user pre-staging files into ComfyUI's
// models/loras dir.
//
// Two sources merge into the dropdown:
//   1. Builtins embedded from styles/builtins.json — generic
//      illustration styles (Simpsons, Clone Wars, Ghibli, etc.)
//      anchored on prompt language.
//   2. User overlay at ~/.talon/images/styles.json. Same shape; user
//      entries shadow builtins on id collision so a user can tune the
//      shipped Simpsons preset without forking the registry.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/guygrigsby/talon/internal/openclaw"
)

//go:embed styles/builtins.json
var stylesBuiltinsJSON []byte

// StylePreset is one row in the styles registry. PromptSuffix and
// NegativeSuffix are appended to the user's prompts at submit time;
// Denoise gives the UI a sensible default for the slider when the
// preset is selected (still overridable per-run).
type StylePreset struct {
	ID             string  `json:"id"`
	Label          string  `json:"label"`
	Description    string  `json:"description,omitempty"`
	PromptSuffix   string  `json:"promptSuffix"`
	NegativeSuffix string  `json:"negativeSuffix,omitempty"`
	// Denoise is a default suggestion; 0.5 tends to preserve content,
	// 0.7+ regenerates substantially.
	Denoise float64 `json:"denoise"`
	// BaseModel is an advisory tag ("Pony", "SDXL", "SD1.5") so the UI
	// can warn when the active workflow's checkpoint doesn't match.
	BaseModel string `json:"baseModel,omitempty"`
	// Lora, when non-nil, declares a LoRA file that must be installed
	// in ComfyUI for this preset to render at full fidelity. The UI
	// cross-references against images.objectInfo to detect presence
	// and surfaces an install button (via images.manager.install when
	// ComfyUI-Manager is detected) or an external download link
	// otherwise. Prompt-only presets leave this nil and work with any
	// installed checkpoint.
	Lora *LoraRequirement `json:"lora,omitempty"`
	// Source is "builtin" or "user", set by the loader. Not part of
	// the on-disk shape.
	Source string `json:"source,omitempty"`
}

// LoraRequirement describes a LoRA file the preset needs and how to
// fetch it. Filename is the on-disk basename ComfyUI will look up
// under models/loras/. Strength is the recommended LoraLoader
// strength_model + strength_clip default; the UI may expose a slider
// around this.
//
// Civitai (when set) carries the metadata the install UI needs:
// download URL for ComfyUI-Manager + a fallback link the user can
// open manually if the manager isn't installed. SHA256 lets the UI
// hash-verify a downloaded file against expectations once support
// for that lands.
type LoraRequirement struct {
	Filename  string  `json:"filename"`
	Strength  float64 `json:"strength,omitempty"`
	BaseModel string  `json:"baseModel,omitempty"`
	Civitai   *CivitaiRef `json:"civitai,omitempty"`
}

// CivitaiRef points at a CivitAI model + version so the UI can render
// install hints. ModelID identifies the model page; VersionID picks a
// specific version's weights. DownloadURL is the direct download
// (typically https://civitai.com/api/download/models/<versionId>) and
// is what gets passed to ComfyUI-Manager's install endpoint. Page is
// the human-readable URL the user can open.
type CivitaiRef struct {
	ModelID     int64  `json:"modelId,omitempty"`
	VersionID   int64  `json:"versionId,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	Page        string `json:"page,omitempty"`
}

// InstalledLoras extracts the set of installed LoRA filenames from a
// ComfyUI object_info payload. ComfyUI exposes the LoRA list as the
// enum of LoraLoader.input.required.lora_name's first element (which
// is itself an array of filenames). Returns an empty set when the
// node class is missing (e.g. plain ComfyUI without LoraLoader, which
// shouldn't happen but the helper shouldn't panic on it).
//
// Defined here (not in comfyui/) so it can move with the styles
// surface that consumes it.
func InstalledLoras(info map[string]json.RawMessage) map[string]struct{} {
	out := map[string]struct{}{}
	loaderRaw, ok := info["LoraLoader"]
	if !ok {
		return out
	}
	var loader struct {
		Input struct {
			Required map[string]json.RawMessage `json:"required"`
		} `json:"input"`
	}
	if err := json.Unmarshal(loaderRaw, &loader); err != nil {
		return out
	}
	nameRaw, ok := loader.Input.Required["lora_name"]
	if !ok {
		return out
	}
	// ComfyUI shape: ["a.safetensors", "b.safetensors"] OR
	// [["a.safetensors", "b.safetensors"], {... metadata ...}]. Try
	// the nested-array form first; fall back to the flat shape.
	var nested []json.RawMessage
	if err := json.Unmarshal(nameRaw, &nested); err == nil && len(nested) > 0 {
		var names []string
		if err := json.Unmarshal(nested[0], &names); err == nil {
			for _, n := range names {
				out[n] = struct{}{}
			}
			return out
		}
		// Flat fallback: nested[0..N] are themselves filenames.
		for _, item := range nested {
			var s string
			if err := json.Unmarshal(item, &s); err == nil && s != "" {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

type stylesFile struct {
	Styles []StylePreset `json:"styles"`
}

// StylesHandler serves images.styles.list.
type StylesHandler struct {
	paths openclaw.Paths
}

// NewStylesHandler returns a handler bound to paths. The user-overlay
// path is <talon>/images/styles.json; missing means "no overlay" (not
// an error).
func NewStylesHandler(paths openclaw.Paths) *StylesHandler {
	return &StylesHandler{paths: paths}
}

// Register wires the images.styles.* RPCs into r.
func (h *StylesHandler) Register(r *Registry) {
	r.Register("images.styles.list", h.handleList)
}

// userStylesPath returns the on-disk overlay path. Sibling of the
// gallery index.json and the workflows dir.
func (h *StylesHandler) userStylesPath() string {
	return filepath.Join(h.paths.Talon.Dir, "images", "styles.json")
}

// loadBuiltins parses the embedded styles JSON. Stamped Source on
// every entry so the merged response is unambiguous.
func loadBuiltinStyles() ([]StylePreset, error) {
	var f stylesFile
	if err := json.Unmarshal(stylesBuiltinsJSON, &f); err != nil {
		return nil, err
	}
	for i := range f.Styles {
		f.Styles[i].Source = workflowSourceBuiltin
	}
	return f.Styles, nil
}

// loadUserStyles reads the user overlay. Missing file returns nil
// without error. A parse failure is logged but doesn't fail the whole
// list — builtins still surface.
func (h *StylesHandler) loadUserStyles() ([]StylePreset, error) {
	raw, err := os.ReadFile(h.userStylesPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f stylesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	for i := range f.Styles {
		f.Styles[i].Source = workflowSourceUser
	}
	return f.Styles, nil
}

// handleList returns the merged registry: user-overlay entries
// shadow builtins by id. Output ordering is stable: user entries
// first (so user-tuned defaults sort to the top of the picker),
// then alphabetical-by-id builtins.
func (h *StylesHandler) handleList(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	builtins, err := loadBuiltinStyles()
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.styles.list: builtins: " + err.Error()}
	}
	users, err := h.loadUserStyles()
	if err != nil {
		// Log + continue with builtins; a malformed overlay shouldn't
		// take the whole picker down.
		slog.Warn("images.styles.list: user overlay unreadable; falling back to builtins",
			"path", h.userStylesPath(), "err", err)
	}
	byID := make(map[string]StylePreset, len(builtins)+len(users))
	for _, s := range builtins {
		byID[s.ID] = s
	}
	for _, s := range users {
		byID[s.ID] = s // shadow
	}
	out := make([]StylePreset, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// User entries first, then alpha by id.
		if out[i].Source != out[j].Source {
			return out[i].Source == workflowSourceUser
		}
		return out[i].ID < out[j].ID
	})
	return map[string]any{"styles": out}, nil
}
