// Package agentcontext composes an agent's workspace markdown files into a
// system prompt the chat handler attaches to provider.Request.System.
//
// Mirrors openclaw's CONTEXT_FILE_ORDER (src/agents/system-prompt.ts):
//
//	AGENTS.md   — workspace overview / session-startup guidance
//	SOUL.md     — persona / tone / boundaries
//	IDENTITY.md — name, creature, vibe, emoji (the chat avatar source)
//	USER.md     — who the human is
//	TOOLS.md    — local tool guidance (informational, not authoritative)
//	MEMORY.md   — long-running notes (project memory)
//
// Files outside this list are deliberately skipped — HEARTBEAT.md is
// handled by the heartbeat poller (separate from regular chat),
// BOOTSTRAP.md is consumed and deleted on first run, and PROJECTS.md
// isn't part of openclaw's canonical prompt either.
package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
)

// canonicalOrder mirrors openclaw's CONTEXT_FILE_ORDER. Order matters —
// AGENTS.md establishes ground rules first, IDENTITY.md grounds the
// assistant before USER.md grounds the human, etc.
var canonicalOrder = []string{
	"AGENTS.md",
	"SOUL.md",
	"IDENTITY.md",
	"USER.md",
	"TOOLS.md",
	"MEMORY.md",
}

// Build composes a system prompt from the agent's workspace context files.
// Returns "" when workspace is empty or no recognized files exist; chat.send
// then sends an empty system message (which providers tolerate).
//
// Each loaded file becomes a "## NAME.md\n\n<content>\n\n" section. The
// composed prompt opens with a single-line load notice, plus an extra
// nudge when SOUL.md is present (matching openclaw's behavior).
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
