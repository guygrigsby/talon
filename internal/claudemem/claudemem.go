// Package claudemem provides read-only, path-confined access to a
// Claude-code memory directory (a MEMORY.md index plus per-fact
// markdown files). It is the loader + tool behind talon's
// `memory.claude.*` feature (ADR 0013): inject the capped index into
// the system prompt and expose a `claude_memory` list/read tool.
//
// Everything here is read-only. The Store never writes, and Read is
// confined to the configured dir: slugs containing '/', '\\', or '..'
// are rejected, and the resolved path is re-verified to stay under the
// dir. This is the security crux of the feature.
package claudemem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// indexName is the filename of the Claude memory index, excluded from
// List and read by Index.
const indexName = "MEMORY.md"

// truncMarker is appended when Index/Read output is capped. It points
// the agent at the tool for the full content.
const truncMarker = "\n…(truncated — use the claude_memory tool to read full entries)"

// Store is a read-only, path-confined view of a Claude memory dir.
type Store struct {
	dir string
}

// New opens a Store over dir. It expands a leading "~", resolves an
// absolute path, and verifies dir exists and is a directory. Returns
// an error otherwise.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("claudemem: empty dir")
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("claudemem: expand ~: %w", err)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("claudemem: resolve %q: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("claudemem: stat %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("claudemem: %q is not a directory", abs)
	}
	return &Store{dir: filepath.Clean(abs)}, nil
}

// Dir returns the resolved directory the Store is confined to.
func (s *Store) Dir() string { return s.dir }

// Index returns MEMORY.md content capped to maxBytes (0 = uncapped).
// On overflow it truncates on a line boundary and appends a marker.
// A missing MEMORY.md yields ("", nil) — the index is optional.
func (s *Store) Index(maxBytes int) (string, error) {
	p := filepath.Join(s.dir, indexName)
	body, err := os.ReadFile(p) //nolint:gosec // p is fixed to MEMORY.md under the confined dir
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("claudemem: read index: %w", err)
	}
	return capText(string(body), maxBytes), nil
}

// List returns the memory slugs: filenames ending in ".md" with the
// extension stripped, excluding MEMORY.md. Sorted for stable output.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("claudemem: read dir: %w", err)
	}
	slugs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == indexName || !strings.HasSuffix(name, ".md") {
			continue
		}
		slugs = append(slugs, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(slugs)
	return slugs, nil
}

// Read returns the content of <slug>.md, path-confined to dir and
// bounded to maxBytes (0 = uncapped). Rejects slugs containing '/',
// '\\', or '..', and re-verifies the resolved path stays under dir.
func (s *Store) Read(slug string, maxBytes int) (string, error) {
	if slug == "" {
		return "", fmt.Errorf("claudemem: empty slug")
	}
	// Reject path separators and parent refs outright — a legitimate
	// slug is a bare filename stem.
	if strings.ContainsAny(slug, `/\`) || strings.Contains(slug, "..") {
		return "", fmt.Errorf("claudemem: invalid slug %q (no path separators or '..')", slug)
	}
	p := filepath.Join(s.dir, slug+".md")
	// Belt-and-suspenders: re-verify the cleaned path stays under dir.
	clean := filepath.Clean(p)
	if clean != filepath.Join(s.dir, slug+".md") || !withinDir(s.dir, clean) {
		return "", fmt.Errorf("claudemem: slug %q escapes memory dir", slug)
	}
	body, err := os.ReadFile(clean) //nolint:gosec // clean is verified to stay under s.dir above
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("claudemem: no memory %q", slug)
		}
		return "", fmt.Errorf("claudemem: read %q: %w", slug, err)
	}
	return capText(string(body), maxBytes), nil
}

// withinDir reports whether path is dir itself or lies under it,
// guarding against prefix-collision false positives (e.g. "/a/bc" vs
// "/a/b").
func withinDir(dir, path string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// capText truncates s to at most maxBytes (0 = uncapped). When it must
// cut, it trims back to the last newline ≤ maxBytes (falling back to a
// hard cut when there is none) and appends truncMarker.
func capText(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
		cut = cut[:nl]
	}
	return cut + truncMarker
}
