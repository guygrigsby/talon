// Package agentcontext composes an agent's workspace persona files
// into the system prompt the chat handler attaches to
// provider.Request.System.
//
// Talon recognizes four workspace files:
//
//	AGENTS.md   — workspace ground rules / startup guidance
//	SOUL.md     — persona / tone / boundaries
//	IDENTITY.md — name, creature, vibe, emoji (the chat avatar source)
//	USER.md     — who the human is
//
// TOOLS.md / MEMORY.md / HEARTBEAT.md / BOOTSTRAP.md / PROJECTS.md
// and the legacy memory/*.md daily journals are NOT loaded.
// Memory now lives in the RAG store at ~/.talon/memory/ via the
// `remember` tool; the workspace-side memory/ pattern is deny-
// listed in the tools layer to keep the model from recreating it.
package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
)

// canonicalOrder is the load order for the workspace persona
// files. AGENTS.md sets ground rules first, then tone (SOUL),
// then identity, then who the human is.
var canonicalOrder = []string{
	"AGENTS.md",
	"SOUL.md",
	"IDENTITY.md",
	"USER.md",
}

// Build composes a system prompt from the agent's workspace
// persona files. Returns "" when workspace is empty or neither
// file exists; chat.send then sends an empty system message
// (which providers tolerate).
//
// SOUL.md gets a one-line preamble nudge when present, matching
// the original openclaw behavior.
func Build(workspace string) string {
	if workspace == "" {
		return ""
	}
	loaded := loadFiles(workspace)
	if len(loaded) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("The following project context files have been loaded:\n")
	for _, f := range loaded {
		if f.name == "SOUL.md" {
			b.WriteString("If SOUL.md is present, embody its persona and tone. Avoid stiff, generic replies; follow its guidance unless higher-priority instructions override it.\n")
			break
		}
	}
	b.WriteString("\n")
	for _, f := range loaded {
		b.WriteString("## ")
		b.WriteString(f.name)
		b.WriteString("\n\n")
		b.WriteString(strings.TrimRight(f.content, "\n"))
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type loadedFile struct {
	name    string
	content string
}

func loadFiles(workspace string) []loadedFile {
	out := make([]loadedFile, 0, len(canonicalOrder))
	for _, name := range canonicalOrder {
		raw, err := os.ReadFile(filepath.Join(workspace, name))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) == "" {
			// Empty / whitespace-only files (the openclaw IDENTITY.md
			// template starts that way before bootstrap) carry no signal
			// and just dilute the prompt.
			continue
		}
		out = append(out, loadedFile{name: name, content: string(raw)})
	}
	return out
}
