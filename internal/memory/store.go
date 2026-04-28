// Package memory writes durable notes to an agent's workspace memory dir.
// The on-disk shape matches what agentcontext.Build reads back into the
// system prompt: <workspace>/memory/<YYYY-MM-DD>.md, one file per day,
// markdown body. Plain text only — no DB, no embeddings — so the user's
// existing openclaw memory dir Just Works and stays grep-able.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrEmptyText is returned by Append when text is whitespace-only.
var ErrEmptyText = errors.New("memory: text is empty")

// ErrNoWorkspace is returned by Append when workspace is "".
var ErrNoWorkspace = errors.New("memory: workspace not configured")

// nowFunc is the time source. Tests override it; production uses time.Now.
var nowFunc = time.Now

// Append writes text to <workspace>/memory/<YYYY-MM-DD>.md, creating the
// file with an H1 date header on first write of the day. Existing files
// get the new note appended after a blank line so consecutive remember
// calls within a day stay readable.
//
// Returns ErrEmptyText / ErrNoWorkspace for caller-fixable errors;
// filesystem failures wrap the underlying os error.
func Append(workspace, text string) error {
	if strings.TrimSpace(text) == "" {
		return ErrEmptyText
	}
	if workspace == "" {
		return ErrNoWorkspace
	}
	today := nowFunc().Format("2006-01-02")
	dir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("memory: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, today+".md")

	var existing string
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}

	var sb strings.Builder
	if existing == "" {
		sb.WriteString("# ")
		sb.WriteString(today)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(strings.TrimRight(existing, "\n"))
		sb.WriteString("\n\n")
	}
	sb.WriteString(strings.TrimSpace(text))
	sb.WriteString("\n")

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", path, err)
	}
	return nil
}
