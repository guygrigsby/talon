package server

// Builtin + user workflow registry. The original images flow had one
// workflow at a config-driven path (typically
// ~/.talon/images/comfyui-default.json) — fine for a single-style
// setup, but the agent should be able to swap between models/LoRAs
// without the user editing JSON.
//
// Two sources merge into the dropdown the /images UI consumes:
//
//  1. Embedded builtins under ./workflows. Each entry pins the
//     prompt/negative/seed node ids for the corresponding JSON file
//     so readAndPatchWorkflow doesn't have to guess. These ship in
//     the OSS binary — only generic, public-checkpoint workflows
//     belong here. Personal LoRA stacks live in the user dir below.
//
//  2. User-dir discovery at ~/.talon/images/workflows/<id>.json,
//     each paired with a sidecar <id>.meta.json carrying the same
//     node-id pins + label/description as the embed entries. Drop a
//     workflow + its meta into that directory and it shows up in
//     the dropdown on the next images.workflows.list call. Lets a
//     user keep a private workflow stack outside the OSS repo.

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// builtinWorkflowsFS holds the shipped workflow JSON templates. The
// glob pattern matches every .json file under ./workflows so a new
// drop-in doesn't require touching this file.
//
//go:embed workflows/*.json
var builtinWorkflowsFS embed.FS

// workflowSourceBuiltin / workflowSourceUser tag a workflowEntry by
// where its JSON lives. Builtins read from the embed FS; user entries
// read from disk under ~/.talon/images/workflows/. The Source field
// also flows out on the wire (imagesWorkflowEntry.Source) so the UI
// can render section separators and per-source affordances.
const (
	workflowSourceBuiltin = "builtin"
	workflowSourceUser    = "user"
)

// workflowEntry is one row in the registry: identity for the dropdown
// + the node-id pins the patcher needs. Description shows up as a
// tooltip / sub-line so users can pick by trait without reading the
// underlying JSON.
//
// For Source=builtin, Filename is a basename relative to the embed's
// "workflows/" directory. For Source=user, Filename is the absolute
// disk path. loadWorkflowJSON dispatches on Source.
type workflowEntry struct {
	ID                   string
	Label                string
	Filename             string
	Source               string
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

// builtinWorkflows is the source of truth for the embedded dropdown
// rows. Order is preserved on the wire so the UI doesn't have to
// sort. Only generic, public-checkpoint workflows belong here —
// anything model-specific to a personal stack belongs in the user
// dir (~/.talon/images/workflows/) so it stays out of the OSS repo.
var builtinWorkflows = []workflowEntry{
	{
		ID:                   "pony-cyberrealistic",
		Label:                "Pony — CyberRealistic (photoreal)",
		Filename:             "pony_cyberrealistic.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "cyberrealisticPony_v170 checkpoint, dpmpp_2m_sde/karras, 30 steps. Photoreal output with Pony score-tag anchors and anti-anime/cartoon negative.",
	},
	{
		ID:                   "pony-real-anime",
		Label:                "Pony — Real Anime",
		Filename:             "pony_real_anime.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		Description:          "realPony_realAnimeNo04 checkpoint, dpmpp_2m_sde/karras, 28 steps. Vibrant anime aesthetic with the Pony score-tag system.",
	},
	{
		// pony_cyberrealistic_tiled adds a megapixel-tiled skin polish
		// (denoise 0.3) on top of the standard pony cyberrealistic
		// base. Uses the dpmpp_2m sampler (matches the corrected base
		// workflow that dropped _sde).
		ID:                   "pony-cyberrealistic-tiled",
		Label:                "Pony — CyberRealistic + Tiled Polish",
		Filename:             "pony_cyberrealistic_tiled.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "5",
		ExtraSeedNodeIDs:     []string{"18"},
		Description:          "cyberrealisticPony_v170 base (30 steps, dpmpp_2m) + megapixel-tiled skin polish pass (20 steps, denoise 0.3). Photoreal output with tiled high-res polish.",
	},
	{
		// Illustrious uses different node ids than the pony set:
		// prompt=6, negative=7, seed=3 (KSampler). VAE is externalized
		// via VAELoader (sdxl_vae.safetensors) and decoding goes
		// through VAEDecodeTiled at 1024x1024.
		ID:                   "illustrious-char",
		Label:                "SDXL Illustrious — Character (28 steps)",
		Filename:             "illustrious_char.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "3",
		Description:          "illustriousXL10Improved_v30 + sdxl_vae external VAE, dpmpp_2m/karras, 28 steps, 1024x1024. Tiled decode.",
	},
	{
		ID:                   "illustrious-hyper",
		Label:                "SDXL Illustrious — Hyper-8 (fast)",
		Filename:             "illustrious_hyper.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "6",
		NegativePromptNodeID: "7",
		SeedNodeID:           "3",
		Description:          "illustriousXL10Improved_v30 + Hyper-SDXL 8-step LoRA + sdxl_vae external VAE. ~4x faster, 1024x1024.",
	},
	{
		// img2img-pony: source-image-driven workflow. The Studio UI
		// uploads an image via images.upload, then submits this
		// workflow with nodeOverrides setting LoadImage (node 4)'s
		// `image` input to the uploaded filename. Denoise (node 6's
		// inputs.denoise) is the primary slider — 0.3 preserves the
		// source closely, 0.7+ regenerates substantially.
		ID:                   "img2img-pony",
		Label:                "Img2Img — Pony (CyberRealistic)",
		Filename:             "img2img_pony.json",
		Source:               workflowSourceBuiltin,
		PromptNodeID:         "2",
		NegativePromptNodeID: "3",
		SeedNodeID:           "6",
		Description:          "Image-to-image with cyberrealisticPony_v170. Upload a source image; denoise (0.3 = subtle restyle, 0.7 = regenerate) drives how much the output diverges. Composition follows the source.",
	},
}

// userWorkflowMeta is the on-disk sidecar shape. One <id>.meta.json
// per <id>.json under ~/.talon/images/workflows/. Mirrors the
// workflowEntry fields the patcher needs; everything else (Source,
// Filename) is filled in by the loader.
type userWorkflowMeta struct {
	ID                   string   `json:"id"`
	Label                string   `json:"label"`
	Description          string   `json:"description"`
	PromptNodeID         string   `json:"promptNodeId"`
	NegativePromptNodeID string   `json:"negativePromptNodeId"`
	SeedNodeID           string   `json:"seedNodeId"`
	ExtraSeedNodeIDs     []string `json:"extraSeedNodeIds,omitempty"`
}

// userWorkflowsDir returns the discovery root. Sibling of the gallery
// index.json and the auto-save directory.
func (h *ImagesHandler) userWorkflowsDir() string {
	return filepath.Join(h.paths.Talon.Dir, "images", "workflows")
}

// loadUserWorkflows scans the user dir and returns one entry per
// (<id>.json, <id>.meta.json) pair. Workflows missing a sidecar are
// skipped with a warning — the patcher can't run without the node
// pins, and silently dropping the row would surface as "my workflow
// disappeared from the dropdown" with no log trail.
//
// Errors only on directory-scan failures that aren't "doesn't exist".
// A missing dir is a normal first-run state; we return an empty slice.
func (h *ImagesHandler) loadUserWorkflows() ([]workflowEntry, error) {
	dir := h.userWorkflowsDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read user workflows dir %s: %w", dir, err)
	}
	out := make([]workflowEntry, 0, len(ents))
	seen := map[string]struct{}{}
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(name, ".json")
		metaPath := filepath.Join(dir, base+".meta.json")
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Warn("images user workflow missing sidecar; skipping",
					"workflow", filepath.Join(dir, name), "expected_meta", metaPath)
				continue
			}
			slog.Warn("images user workflow sidecar unreadable; skipping",
				"meta", metaPath, "err", err)
			continue
		}
		var meta userWorkflowMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			slog.Warn("images user workflow sidecar invalid JSON; skipping",
				"meta", metaPath, "err", err)
			continue
		}
		if strings.TrimSpace(meta.ID) == "" || strings.TrimSpace(meta.PromptNodeID) == "" {
			slog.Warn("images user workflow sidecar missing required fields (id, promptNodeId); skipping",
				"meta", metaPath)
			continue
		}
		// First-write wins on duplicate IDs — directory order is
		// stable enough that the warning identifies which file lost.
		if _, dup := seen[meta.ID]; dup {
			slog.Warn("images user workflow duplicate id; later entry skipped",
				"id", meta.ID, "meta", metaPath)
			continue
		}
		seen[meta.ID] = struct{}{}
		out = append(out, workflowEntry{
			ID:                   meta.ID,
			Label:                meta.Label,
			Filename:             filepath.Join(dir, name),
			Source:               workflowSourceUser,
			PromptNodeID:         meta.PromptNodeID,
			NegativePromptNodeID: meta.NegativePromptNodeID,
			SeedNodeID:           meta.SeedNodeID,
			ExtraSeedNodeIDs:     append([]string(nil), meta.ExtraSeedNodeIDs...),
			Description:          meta.Description,
		})
	}
	return out, nil
}

// findWorkflow returns the entry matching id from either the embed or
// the user dir, or nil when id is empty / unknown. Embed takes priority
// on collision — a user dropping their own "pony-cyberrealistic" into
// the user dir would be shadowed; that's the right call (drift between
// the shipped pin set and a user override silently corrupts patching).
func (h *ImagesHandler) findWorkflow(id string) *workflowEntry {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range builtinWorkflows {
		if builtinWorkflows[i].ID == id {
			return &builtinWorkflows[i]
		}
	}
	users, err := h.loadUserWorkflows()
	if err != nil {
		slog.Warn("images user workflows scan failed",
			"err", err)
		return nil
	}
	for i := range users {
		if users[i].ID == id {
			return &users[i]
		}
	}
	return nil
}

// loadWorkflowJSON reads the JSON for entry from whichever source
// (embed or disk). The embed path joins under "workflows/" so callers
// can't request arbitrary embed paths; the user path uses the absolute
// path stored on entry.Filename, which loadUserWorkflows constructed
// from the discovery root.
func loadWorkflowJSON(entry *workflowEntry) ([]byte, error) {
	if entry == nil {
		return nil, fmt.Errorf("nil workflow")
	}
	var raw []byte
	switch entry.Source {
	case workflowSourceUser:
		b, err := os.ReadFile(entry.Filename)
		if err != nil {
			return nil, fmt.Errorf("read user workflow %s: %w", entry.ID, err)
		}
		raw = b
	default:
		path := filepath.ToSlash(filepath.Join("workflows", entry.Filename))
		b, err := builtinWorkflowsFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read builtin workflow %s: %w", entry.ID, err)
		}
		raw = b
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("workflow %s: invalid JSON", entry.ID)
	}
	return raw, nil
}

// --- images.workflows.list ------------------------------------------------

type imagesWorkflowEntry struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Source distinguishes the user's config-driven default ("user")
	// from shipped builtins ("builtin"). User-dir discovery rows also
	// use "user" — same surface from the UI's perspective. The UI uses
	// this to render section separators and a "Configure path" link
	// for the legacy default row.
	Source string `json:"source"`
}

// --- images.workflows.get -------------------------------------------------

type imagesWorkflowGetParams struct {
	// ID matches one of imagesWorkflowEntry.ID values from
	// images.workflows.list. Empty string means "the user's
	// config-driven default workflow."
	ID string `json:"id"`
}

// handleWorkflowsGet returns the parsed workflow JSON for a given ID
// so the UI can introspect it (find KSampler nodes, list LoRAs and
// IPAdapters, read current input values for slider defaults). The
// payload is the literal node graph; downstream tooling parses it.
//
// Why expose the raw graph instead of a curated metadata struct: the
// UI's needs evolve faster than the server schema would (new node
// types, new input keys per ComfyUI release). Letting the UI read the
// graph directly keeps server churn low and the UI flexible.
func (h *ImagesHandler) handleWorkflowsGet(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p imagesWorkflowGetParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "images.workflows.get: " + err.Error()}
		}
	}
	cfg, ferr := h.loadComfyUIConfig(p.ID)
	if ferr != nil {
		return nil, ferr
	}
	var raw []byte
	if len(cfg.WorkflowJSON) > 0 {
		raw = cfg.WorkflowJSON
	} else {
		var err error
		raw, err = os.ReadFile(cfg.WorkflowPath)
		if err != nil {
			return nil, &FrameError{
				Code:    ErrCodeBadRequest,
				Message: "images.workflows.get: " + err.Error(),
			}
		}
	}
	var graph map[string]any
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "images.workflows.get: " + err.Error()}
	}
	return map[string]any{
		"id":    p.ID,
		"graph": graph,
	}, nil
}

// handleWorkflowsList returns the available workflow templates: the
// user's config-driven legacy default first (when configured), every
// builtin entry, then every user-dir workflow. The list is the input
// to the /images dropdown.
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
			Source:      workflowSourceUser,
		})
	}
	for _, b := range builtinWorkflows {
		out = append(out, imagesWorkflowEntry{
			ID:          b.ID,
			Label:       b.Label,
			Description: b.Description,
			Source:      workflowSourceBuiltin,
		})
	}
	users, err := h.loadUserWorkflows()
	if err != nil {
		// Don't fail the whole list — embed entries are still
		// usable. The discovery error is logged inside
		// loadUserWorkflows for follow-up.
		slog.Warn("images user workflows scan failed",
			"err", err)
	}
	for _, u := range users {
		out = append(out, imagesWorkflowEntry{
			ID:          u.ID,
			Label:       u.Label,
			Description: u.Description,
			Source:      workflowSourceUser,
		})
	}
	return map[string]any{"workflows": out}, nil
}
