package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type grepTool struct{ ws string }

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return "Search files in the workspace for a regex. Returns up to 200 matches as 'path:line:text' rows."
}

func (t *grepTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Go RE2 regex."},
			"path":    {"type": "string", "description": "Optional workspace-relative root (file or dir). Default: workspace root."},
			"include": {"type": "string", "description": "Optional glob filter applied to file basenames, e.g. \"*.go\"."}
		},
		"required": ["pattern"]
	}`)
}

func (t *grepTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: invalid regex: %w", err)
	}
	root := t.ws
	if p.Path != "" {
		abs, err := resolveInWorkspace(t.ws, p.Path)
		if err != nil {
			return "", err
		}
		root = abs
	}

	const maxMatches = 200
	var b strings.Builder
	matches := 0

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate permission errors etc.
		}
		if d.IsDir() {
			// Skip hidden dirs and a couple of common heavy ones.
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".talon" {
				return filepath.SkipDir
			}
			return nil
		}
		if p.Include != "" {
			ok, _ := filepath.Match(p.Include, d.Name())
			if !ok {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		rel, _ := filepath.Rel(t.ws, path)
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if re.MatchString(line) {
				matches++
				fmt.Fprintf(&b, "%s:%d:%s\n", rel, lineNo, line)
				if matches >= maxMatches {
					return filepath.SkipAll
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return b.String(), fmt.Errorf("grep: %w", walkErr)
	}
	if matches == 0 {
		return "(no matches)", nil
	}
	if matches >= maxMatches {
		fmt.Fprintf(&b, "(stopped at %d matches; refine the pattern or pass a path/include filter)\n", maxMatches)
	}
	return b.String(), nil
}
