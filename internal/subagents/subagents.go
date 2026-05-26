// Package subagents loads Talon's file-backed subagent definitions.
package subagents

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is the normalized view of one subagent markdown file.
type Definition struct {
	ID          string
	Name        string
	Description string
	Model       string
	Prompt      string
	Path        string
	Tools       []string
	Disabled    bool
}

type frontMatter struct {
	ID          string         `yaml:"id"`
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Model       string         `yaml:"model"`
	Tools       any            `yaml:"tools"`
	Disabled    bool           `yaml:"disabled"`
	Extra       map[string]any `yaml:",inline"`
}

// LoadDir reads all markdown subagents from dir. Missing directories are a
// valid empty set so a fresh Talon install does not need bootstrap files.
func LoadDir(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var defs []Definition
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.ToLower(filepath.Ext(name)) != ".md" {
			continue
		}
		path := filepath.Join(dir, name)
		def, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if def.ID == "" || def.Disabled {
			continue
		}
		defs = append(defs, def)
	}

	sort.Slice(defs, func(i, j int) bool {
		return defs[i].ID < defs[j].ID
	})
	return defs, nil
}

// LoadFile parses one opencode-style markdown agent file. The expected shape is
// YAML front matter followed by the prompt body:
//
//	---
//	description: Reviews code for regressions.
//	model: anthropic/claude-sonnet-4-6
//	tools: [read, grep]
//	---
//	You are a focused code reviewer...
//
// The parser is tolerant: id/name/description/model are optional, tools may be
// a YAML list, string list, or map of enabled booleans.
func LoadFile(path string) (Definition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}

	fmBytes, body := splitFrontMatter(raw)
	var fm frontMatter
	if len(fmBytes) > 0 {
		if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
			return Definition{}, fmt.Errorf("parse subagent %s front matter: %w", path, err)
		}
	}

	id := slugOrBase(fm.ID, path)
	prompt := strings.TrimSpace(string(body))
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		desc = firstPromptLine(prompt)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = titleFromID(id)
	}

	return Definition{
		ID:          id,
		Name:        name,
		Description: desc,
		Model:       strings.TrimSpace(fm.Model),
		Prompt:      prompt,
		Path:        path,
		Tools:       normalizeTools(fm.Tools),
		Disabled:    fm.Disabled,
	}, nil
}

// Find loads one enabled subagent by id.
func Find(dir, id string) (Definition, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Definition{}, false, nil
	}
	defs, err := LoadDir(dir)
	if err != nil {
		return Definition{}, false, err
	}
	for _, def := range defs {
		if def.ID == id {
			return def, true, nil
		}
	}
	return Definition{}, false, nil
}

func splitFrontMatter(raw []byte) ([]byte, []byte) {
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if !bytes.HasPrefix(raw, []byte("---\n")) && !bytes.HasPrefix(raw, []byte("---\r\n")) {
		return nil, raw
	}
	start := 4
	if bytes.HasPrefix(raw, []byte("---\r\n")) {
		start = 5
	}
	rest := raw[start:]
	for _, marker := range [][]byte{[]byte("\n---\n"), []byte("\n---\r\n"), []byte("\r\n---\r\n"), []byte("\r\n---\n")} {
		if idx := bytes.Index(rest, marker); idx >= 0 {
			return rest[:idx], rest[idx+len(marker):]
		}
	}
	return nil, raw
}

func slugOrBase(id, path string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func firstPromptLine(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			if len(line) > 160 {
				return line[:160]
			}
			return line
		}
	}
	return ""
}

func titleFromID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	out := strings.Join(parts, " ")
	if out == "" {
		return id
	}
	return out
}

func normalizeTools(raw any) []string {
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}

	switch v := raw.(type) {
	case nil:
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, item := range v {
			add(item)
		}
	case string:
		for _, item := range strings.Split(v, ",") {
			add(item)
		}
	case map[string]any:
		for name, enabled := range v {
			if boolValue(enabled, true) {
				add(name)
			}
		}
	case map[any]any:
		for name, enabled := range v {
			if boolValue(enabled, true) {
				add(fmt.Sprint(name))
			}
		}
	}

	sort.Strings(out)
	dedup := out[:0]
	for _, item := range out {
		if len(dedup) == 0 || dedup[len(dedup)-1] != item {
			dedup = append(dedup, item)
		}
	}
	return dedup
}

func boolValue(v any, fallback bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on", "1":
			return true
		case "false", "no", "off", "0":
			return false
		}
	}
	return fallback
}
