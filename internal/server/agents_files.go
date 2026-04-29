package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/guygrigsby/talon/internal/config"
)

// agents.files.{list,get,set} mirror the openclaw RPC set the web UI's
// /agents tab calls into. The canonical list of files an agent owns is
// the bootstrap markdown set plus MEMORY.md — listed below so it stays
// in sync with the openclaw constants under
// openclaw/src/agents/workspace.ts. Order matters: the UI renders rows
// in the order returned, and BOOTSTRAP.md is shown last (when present)
// because it's only relevant pre-onboarding.
//
// AGENTS.md is the agent's user-facing system prompt. SOUL/TOOLS/IDENTITY/
// USER/HEARTBEAT are persona/runtime files the model edits during a
// conversation. BOOTSTRAP.md is openclaw's onboarding sentinel; once the
// workspace is "set up" it disappears from openclaw's view, but talon
// has no equivalent state machine yet so we always emit it (with
// missing=true when absent).
var bootstrapFileNames = []string{
	"AGENTS.md",
	"SOUL.md",
	"TOOLS.md",
	"IDENTITY.md",
	"USER.md",
	"HEARTBEAT.md",
	"BOOTSTRAP.md",
}

const memoryFileName = "MEMORY.md"

// allowedAgentFile is the union of canonical files agents.files.{get,set}
// accept. Any other name is rejected as INVALID_REQUEST so the UI can't
// be tricked into reading/writing arbitrary workspace paths.
func allowedAgentFile(name string) bool {
	if name == memoryFileName {
		return true
	}
	for _, n := range bootstrapFileNames {
		if n == name {
			return true
		}
	}
	return false
}

type agentsFilesListParams struct {
	AgentID string `json:"agentId"`
}

type agentFileEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Missing     bool   `json:"missing"`
	Size        int64  `json:"size,omitempty"`
	UpdatedAtMs int64  `json:"updatedAtMs,omitempty"`
	Content     string `json:"content,omitempty"`
}

func (h *ReadHandler) handleAgentsFilesList(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p agentsFilesListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.list: " + err.Error()}
		}
	}
	if p.AgentID == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.list: agentId is required"}
	}
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agents.files.list: " + err.Error()}
	}
	workspace := resolveWorkspace(merged, p.AgentID)
	if workspace == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.list: unknown agent or no resolvable workspace: " + p.AgentID}
	}

	files := make([]agentFileEntry, 0, len(bootstrapFileNames)+1)
	for _, name := range bootstrapFileNames {
		files = append(files, statAgentFile(workspace, name))
	}
	files = append(files, statAgentFile(workspace, memoryFileName))

	return map[string]any{
		"agentId":   p.AgentID,
		"workspace": workspace,
		"files":     files,
	}, nil
}

type agentsFilesGetParams struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name"`
}

func (h *ReadHandler) handleAgentsFilesGet(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p agentsFilesGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.get: " + err.Error()}
	}
	if p.AgentID == "" || p.Name == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.get: agentId and name are required"}
	}
	if !allowedAgentFile(p.Name) {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.get: name not allowed: " + p.Name}
	}
	workspace, ferr := h.workspaceFor(p.AgentID, "agents.files.get")
	if ferr != nil {
		return nil, ferr
	}
	abs, ferr := safeWorkspaceFile(workspace, p.Name, "agents.files.get")
	if ferr != nil {
		return nil, ferr
	}
	entry := statAgentFile(workspace, p.Name)
	if !entry.Missing {
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, &FrameError{Code: ErrCodeInternal, Message: "agents.files.get: read: " + err.Error()}
		}
		entry.Content = string(raw)
	}
	return map[string]any{
		"agentId":   p.AgentID,
		"workspace": workspace,
		"file":      entry,
	}, nil
}

type agentsFilesSetParams struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (h *ReadHandler) handleAgentsFilesSet(_ context.Context, _ HandlerCtx, params json.RawMessage) (any, *FrameError) {
	var p agentsFilesSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.set: " + err.Error()}
	}
	if p.AgentID == "" || p.Name == "" {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.set: agentId and name are required"}
	}
	if !allowedAgentFile(p.Name) {
		return nil, &FrameError{Code: ErrCodeBadRequest, Message: "agents.files.set: name not allowed: " + p.Name}
	}
	workspace, ferr := h.workspaceFor(p.AgentID, "agents.files.set")
	if ferr != nil {
		return nil, ferr
	}
	abs, ferr := safeWorkspaceFile(workspace, p.Name, "agents.files.set")
	if ferr != nil {
		return nil, ferr
	}
	// MkdirAll matches openclaw's set behavior — first-write to a freshly-
	// configured agent shouldn't fail just because the workspace dir
	// hasn't been materialized yet.
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agents.files.set: mkdir: " + err.Error()}
	}
	if err := os.WriteFile(abs, []byte(p.Content), 0o600); err != nil {
		return nil, &FrameError{Code: ErrCodeInternal, Message: "agents.files.set: write: " + err.Error()}
	}
	return map[string]any{
		"ok":        true,
		"agentId":   p.AgentID,
		"workspace": workspace,
		"file":      statAgentFile(workspace, p.Name),
	}, nil
}

// statAgentFile builds the AgentFileEntry the UI consumes — populating
// size/updatedAtMs when present, missing=true otherwise. Path is always
// the absolute on-disk path so the UI can render it as a hint.
func statAgentFile(workspace, name string) agentFileEntry {
	abs := filepath.Join(workspace, name)
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return agentFileEntry{Name: name, Path: abs, Missing: true}
	}
	return agentFileEntry{
		Name:        name,
		Path:        abs,
		Missing:     false,
		Size:        st.Size(),
		UpdatedAtMs: st.ModTime().UnixMilli(),
	}
}

// workspaceFor returns the absolute workspace path for agentID using
// the shared agents.list precedence, mapping the empty-string (unknown
// agent) case to BAD_REQUEST. method is the prefix used in the error
// message.
func (h *ReadHandler) workspaceFor(agentID, method string) (string, *FrameError) {
	merged, err := config.MergedBytes(h.paths)
	if err != nil {
		return "", &FrameError{Code: ErrCodeInternal, Message: method + ": " + err.Error()}
	}
	workspace := resolveWorkspace(merged, agentID)
	if workspace == "" {
		return "", &FrameError{Code: ErrCodeBadRequest, Message: method + ": unknown agent or no resolvable workspace: " + agentID}
	}
	return workspace, nil
}

// safeWorkspaceFile joins workspace + name and rejects path-traversal
// attempts. allowedAgentFile already covers the canonical list, but the
// rel check is a belt-and-braces guard against future allowlist changes.
func safeWorkspaceFile(workspace, name, method string) (string, *FrameError) {
	if name == "" {
		return "", &FrameError{Code: ErrCodeBadRequest, Message: method + ": name is required"}
	}
	clean := filepath.Clean(workspace)
	abs := filepath.Clean(filepath.Join(clean, name))
	rel, err := filepath.Rel(clean, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", &FrameError{Code: ErrCodeBadRequest, Message: fmt.Sprintf("%s: name escapes workspace: %q", method, name)}
	}
	return abs, nil
}
