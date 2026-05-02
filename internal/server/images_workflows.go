package server

// Builtin workflow registry. The original images flow had one
// workflow at a config-driven path (typically
// ~/.talon/images/comfyui-default.json) — fine for a single-style
// setup, but the agent should be able to swap between models/LoRAs
// without the user editing JSON. This file embeds shipped workflow
// templates for popular models so they appear as a dropdown in the
// /images UI alongside the user's custom workflow.
//
// Each entry pins the prompt/negative/seed node ids for the
// corresponding JSON file so readAndPatchWorkflow doesn't have to
// guess. Adding a new builtin: drop a workflow JSON next to this
// file (under ./workflows), append an entry here. The shipped JSON
// uses placeholder strings (%prompt%, %seed%) but they're overwritten
// by the patcher — values in the file are illustrative only.

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// builtinWorkflowsFS holds the shipped workflow JSON templates. The
// glob pattern matches every .json file under ./workflows so a new
// drop-in doesn't require touching this file.
//
//go:embed workflows/*.json
var builtinWorkflowsFS embed.FS

// builtinWorkflow is one entry in the registry: identity for the
// dropdown + the node-id pins the patcher needs. Description shows
// up as a tooltip / sub-line so users can pick by trait without
// reading the underlying JSON.
type builtinWorkflow struct {
	ID                   string
	Label                string
	Filename             string
	PromptNodeID         string
	NegativePromptNodeID string
	SeedNodeID           string
	// ExtraSeedNodeIDs are additional KSampler nodes that share the
	// same seed. Two-pass workflows (base → hires refine) need both
	// samplers seeded to the same value for deterministic
	// continuity, and any node still holding the literal "%seed%"
	// placeholder fails ComfyUI's KSampler validation. Empty for
	// single-pass workflows.
	ExtraSeedNodeIDs []string
	Description      string
}

// builtinWorkflows is the source of truth for the dropdown. Order is
// preserved on the wire so the UI doesn't have to sort. New entries
// go at the bottom; reordering changes the picker's first-load
// default for users who haven't picked a workflow yet.
var builtinWorkflows = []builtinWorkflow{
	{
		ID:                   "dixar-character",
		Label:                "Dixar Character (30 steps)",
		Filename:             "dixar_character.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "dixar_4DixGalore checkpoint, deis sampler, 30 steps. Higher quality, slower (~20–30s).",
	},
	{
		ID:                   "dixar-character-hyper8",
		Label:                "Dixar Character — Hyper-8 (fast)",
		Filename:             "dixar_character_hyper8.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "dixar_4DixGalore + Hyper-SDXL 8-step LoRA. ~4x faster, slightly lower fidelity.",
	},
	{
		ID:                   "dixar-3d",
		Label:                "Dixar 3D LoRA (30 steps)",
		Filename:             "dixar_3d.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "dixar_4DixGalore + 3d.safetensors style LoRA, deis sampler, 30 steps. Stylized 3D look.",
	},
	{
		ID:                   "dixar-3d-hyper8",
		Label:                "Dixar 3D LoRA — Hyper-8 (fast)",
		Filename:             "dixar_3d_hyper8.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "dixar_4DixGalore + 3d LoRA + Hyper-SDXL 8-step LoRA. Stylized 3D, ~4x faster.",
	},
	{
		ID:                   "pony-cyberrealistic",
		Label:                "Pony — CyberRealistic (photoreal)",
		Filename:             "pony_cyberrealistic.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "cyberrealisticPony_v170 checkpoint, dpmpp_2m_sde/karras, 30 steps. Photoreal output with Pony score-tag anchors and anti-anime/cartoon negative.",
	},
	{
		ID:                   "pony-real-anime",
		Label:                "Pony — Real Anime",
		Filename:             "pony_real_anime.json",
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "realPony_realAnimeNo04 checkpoint, dpmpp_2m_sde/karras, 28 steps. Vibrant anime aesthetic with the Pony score-tag system.",
	},
	{
		// Illustrious uses different node ids than the dixar/pony
		// set: prompt=6, negative=7, seed=3 (KSampler). VAE is
		// externalized via VAELoader (sdxl_vae.safetensors) and
		// decoding goes through VAEDecodeTiled at 1024x1024.
		ID:                   "illustrious-char",
		Label:                "SDXL Illustrious — Character (28 steps)",
		Filename:             "illustrious_char.json",
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "3",
		Description:          "illustriousXL10Improved_v30 + sdxl_vae external VAE, dpmpp_2m/karras, 28 steps, 1024x1024. Tiled decode.",
	},
	{
		ID:                   "illustrious-hyper",
		Label:                "SDXL Illustrious — Hyper-8 (fast)",
		Filename:             "illustrious_hyper.json",
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "3",
		Description:          "illustriousXL10Improved_v30 + Hyper-SDXL 8-step LoRA + sdxl_vae external VAE. ~4x faster, 1024x1024.",
	},
	{
		// vyx is a two-pass hires-fix workflow on duchaitenPonyReal:
		// 832x1216 base → Remacri 4x upscale → 1248x1824 downscale →
		// 0.4-denoise refine. Both KSamplers (9 base, 15 refine)
		// share the same seed so the refine pass stays coherent.
		// LoRA stack: ExpressiveH, Skin Color slider (off), Disney
		// Princess XL (0.4), Perfect Eyes XL (0.5).
		ID:                   "vyx",
		Label:                "VYX — duchaitenPonyReal + hires refine",
		Filename:             "vyx.json",
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "9",
		ExtraSeedNodeIDs:     []string{"15"},
		Description:          "duchaitenPonyReal_v20 + 4 LoRAs (ExpressiveH / Skin Color slider / Disney Princess / Perfect Eyes), two-pass hires fix via Remacri 4x upscaler. Slow (~60s) but high fidelity.",
	},
}

// findBuiltinWorkflow returns the entry by id, or nil when id is
// empty / unknown. nil signals "fall through to the user's
// config-driven default workflow."
func findBuiltinWorkflow(id string) *builtinWorkflow {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range builtinWorkflows {
		if builtinWorkflows[i].ID == id {
			return &builtinWorkflows[i]
		}
	}
	return nil
}

// loadBuiltinWorkflowJSON reads the shipped JSON for entry. The
// filename is joined under "workflows/" so callers can't request
// arbitrary embed paths.
func loadBuiltinWorkflowJSON(entry *builtinWorkflow) ([]byte, error) {
	if entry == nil {
		return nil, fmt.Errorf("nil builtin workflow")
	}
	path := filepath.ToSlash(filepath.Join("workflows", entry.Filename))
	raw, err := builtinWorkflowsFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read builtin workflow %s: %w", entry.ID, err)
	}
	// Validate JSON shape upfront so misconfiguration surfaces here
	// rather than mid-patch with a less helpful error.
	if !json.Valid(raw) {
		return nil, fmt.Errorf("builtin workflow %s: invalid JSON", entry.ID)
	}
	return raw, nil
}

// --- images.workflows.list ------------------------------------------------

type imagesWorkflowEntry struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Source distinguishes the user's config-driven default ("user")
	// from shipped builtins ("builtin"). The UI uses this to render
	// section separators and a "Configure path" link for the user
	// row.
	Source string `json:"source"`
}

// handleWorkflowsList returns the available workflow templates: the
// user's config-driven workflow first (when configured) and every
// builtin entry. The list is the input to the /images dropdown.
func (h *ImagesHandler) handleWorkflowsList(_ context.Context, _ HandlerCtx, _ json.RawMessage) (any, *FrameError) {
	out := []imagesWorkflowEntry{}
	// Surface the user's default workflow only when it actually
	// resolves — pointing the dropdown at a missing file would just
	// surface a load error on submit. Source="user" so the UI can
	// label/distinguish it.
	cfg, ferr := h.loadComfyUIConfig("")
	if ferr == nil && cfg.WorkflowPath != "" && fileExists(cfg.WorkflowPath) {
		out = append(out, imagesWorkflowEntry{
			ID:          "",
			Label:       "Default (configured)",
			Description: "Your workflow at " + cfg.WorkflowPath,
			Source:      "user",
		})
	}
	for _, b := range builtinWorkflows {
		out = append(out, imagesWorkflowEntry{
			ID:          b.ID,
			Label:       b.Label,
			Description: b.Description,
			Source:      "builtin",
		})
	}
	return map[string]any{"workflows": out}, nil
}
