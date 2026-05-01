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
	Description          string
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
