package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentsFilesFixture writes a config with agent "main" pointing at a
// per-agent workspace under the temp dir, with a few seeded files. The
// other handler test helpers point workspaces at /ws/* placeholders;
// agents.files.* actually stats the on-disk path so we need real files.
func agentsFilesFixture(t *testing.T) (h *ReadHandler, workspace, agentID string) {
	t.Helper()
	paths := readFixture(t, `{}`)
	wsDir := filepath.Join(paths.Openclaw.Dir, "workspace-main")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"agents":{"list":[{"id":"main","workspace":%q}]}}`, wsDir)
	if err := os.WriteFile(paths.Openclaw.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte("hello agents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "IDENTITY.md"), []byte("# IDENTITY"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewReadHandler(paths), wsDir, "main"
}

func TestAgentsFilesList_AllCanonicalEntries(t *testing.T) {
	h, ws, id := agentsFilesFixture(t)
	res, ferr := h.handleAgentsFilesList(t.Context(), HandlerCtx{}, mustJSON(t, map[string]any{"agentId": id}))
	if ferr != nil {
		t.Fatalf("list: %+v", ferr)
	}
	m := res.(map[string]any)
	if m["agentId"] != id || m["workspace"] != ws {
		t.Fatalf("envelope mismatch: %+v", m)
	}
	files := m["files"].([]agentFileEntry)
	// Canonical persona set: AGENTS.md, SOUL.md, IDENTITY.md,
	// USER.md (in load order).
	wantNames := []string{"AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md"}
	if len(files) != len(wantNames) {
		t.Fatalf("got %d files, want %d", len(files), len(wantNames))
	}
	for i, want := range wantNames {
		if files[i].Name != want {
			t.Errorf("files[%d].Name=%q, want %q", i, files[i].Name, want)
		}
	}
	// AGENTS.md and IDENTITY.md were seeded with content.
	for _, f := range files {
		switch f.Name {
		case "AGENTS.md", "IDENTITY.md":
			if f.Missing || f.Size <= 0 || f.UpdatedAtMs == 0 {
				t.Errorf("seeded %q has wrong meta: %+v", f.Name, f)
			}
		default:
			if !f.Missing {
				t.Errorf("expected %q to be missing", f.Name)
			}
		}
	}
}

func TestAgentsFilesList_RejectsUnknownAgent(t *testing.T) {
	h, _, _ := agentsFilesFixture(t)
	_, ferr := h.handleAgentsFilesList(t.Context(), HandlerCtx{}, mustJSON(t, map[string]any{"agentId": "ghost"}))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Fatalf("expected BAD_REQUEST for unknown agent, got %+v", ferr)
	}
}

func TestAgentsFilesGet_ReturnsContentForExistingFile(t *testing.T) {
	h, _, id := agentsFilesFixture(t)
	res, ferr := h.handleAgentsFilesGet(t.Context(), HandlerCtx{},
		mustJSON(t, map[string]any{"agentId": id, "name": "AGENTS.md"}))
	if ferr != nil {
		t.Fatalf("get: %+v", ferr)
	}
	file := res.(map[string]any)["file"].(agentFileEntry)
	if file.Missing || file.Content != "hello agents" {
		t.Fatalf("wrong file payload: %+v", file)
	}
}

func TestAgentsFilesGet_MissingFileReturnsEntryWithoutContent(t *testing.T) {
	h, _, id := agentsFilesFixture(t)
	res, ferr := h.handleAgentsFilesGet(t.Context(), HandlerCtx{},
		mustJSON(t, map[string]any{"agentId": id, "name": "USER.md"}))
	if ferr != nil {
		t.Fatalf("get: %+v", ferr)
	}
	file := res.(map[string]any)["file"].(agentFileEntry)
	if !file.Missing || file.Content != "" {
		t.Fatalf("expected missing entry, got %+v", file)
	}
}

func TestAgentsFilesGet_RejectsNameOutsideAllowlist(t *testing.T) {
	h, _, id := agentsFilesFixture(t)
	for _, bad := range []string{"../etc/passwd", "config.json", "subdir/AGENTS.md", ""} {
		_, ferr := h.handleAgentsFilesGet(t.Context(), HandlerCtx{},
			mustJSON(t, map[string]any{"agentId": id, "name": bad}))
		if ferr == nil || ferr.Code != ErrCodeBadRequest {
			t.Errorf("name=%q: expected BAD_REQUEST, got %+v", bad, ferr)
		}
	}
}

func TestAgentsFilesSet_WritesAndStats(t *testing.T) {
	h, ws, id := agentsFilesFixture(t)
	res, ferr := h.handleAgentsFilesSet(t.Context(), HandlerCtx{},
		mustJSON(t, map[string]any{"agentId": id, "name": "USER.md", "content": "guy"}))
	if ferr != nil {
		t.Fatalf("set: %+v", ferr)
	}
	got, err := os.ReadFile(filepath.Join(ws, "USER.md"))
	if err != nil || string(got) != "guy" {
		t.Fatalf("USER.md not written: err=%v contents=%q", err, got)
	}
	envelope := res.(map[string]any)
	if envelope["ok"] != true {
		t.Errorf("ok flag missing: %+v", envelope)
	}
	file := envelope["file"].(agentFileEntry)
	if file.Missing || file.Size != int64(len("guy")) {
		t.Errorf("file meta wrong: %+v", file)
	}
}

func TestAgentsFilesSet_CreatesMissingWorkspaceDir(t *testing.T) {
	h, ws, id := agentsFilesFixture(t)
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	_, ferr := h.handleAgentsFilesSet(t.Context(), HandlerCtx{},
		mustJSON(t, map[string]any{"agentId": id, "name": "AGENTS.md", "content": "fresh"}))
	if ferr != nil {
		t.Fatalf("set: %+v", ferr)
	}
	got, err := os.ReadFile(filepath.Join(ws, "AGENTS.md"))
	if err != nil || string(got) != "fresh" {
		t.Fatalf("AGENTS.md not written after mkdir: err=%v contents=%q", err, got)
	}
}

func TestAgentsFilesSet_RejectsTraversal(t *testing.T) {
	h, _, id := agentsFilesFixture(t)
	_, ferr := h.handleAgentsFilesSet(t.Context(), HandlerCtx{},
		mustJSON(t, map[string]any{"agentId": id, "name": "../escape.md", "content": "x"}))
	if ferr == nil || ferr.Code != ErrCodeBadRequest {
		t.Fatalf("expected BAD_REQUEST for traversal, got %+v", ferr)
	}
	if !strings.Contains(ferr.Message, "name not allowed") && !strings.Contains(ferr.Message, "escapes workspace") {
		t.Errorf("error message should mention rejection, got: %s", ferr.Message)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
