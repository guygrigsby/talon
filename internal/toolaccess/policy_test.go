package toolaccess

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guygrigsby/talon/internal/talonpath"
)

func TestResolve_MainAgentAllowList(t *testing.T) {
	p, err := Resolve([]byte(`{
		"agents": {
			"defaults": {"tools": {"allow": ["read", "grep", "read"]}},
			"list": [{"id": "main", "tools": {"allow": ["agents", "subagent"]}}]
		}
	}`), talonpath.Paths{}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || !p.Restricted {
		t.Fatalf("policy = %+v, want enabled restricted", p)
	}
	if got := p.Names(); len(got) != 2 || got[0] != "agents" || got[1] != "subagent" {
		t.Fatalf("names = %+v", got)
	}
	if p.Allows("bash") {
		t.Fatal("bash should be denied")
	}
}

func TestResolve_ToolsEnabledFalseDisablesAll(t *testing.T) {
	p, err := Resolve([]byte(`{
		"agents": {"list": [{"id": "main", "tools": {"enabled": false, "allow": ["read"]}}]}
	}`), talonpath.Paths{}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled || p.Allows("read") {
		t.Fatalf("policy = %+v, want disabled", p)
	}
}

func TestResolve_SubagentFrontMatterAllowList(t *testing.T) {
	dir := t.TempDir()
	paths := talonpath.Paths{Talon: talonpath.Layer{Dir: dir}}
	subDir := paths.Talon.SubagentsDir()
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "coding.md"), []byte(`---
tools: [read, grep]
---
Code work.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Resolve([]byte(`{"agents":{"defaults":{"tools":{"allow":["read","write","bash"]}}}}`), paths, "coding")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Names(); len(got) != 2 || got[0] != "grep" || got[1] != "read" {
		t.Fatalf("names = %+v", got)
	}
	if p.Allows("write") {
		t.Fatal("write should be denied by subagent front matter")
	}
}

func TestResolve_DefaultDisableWinsOverSubagentFrontMatter(t *testing.T) {
	dir := t.TempDir()
	paths := talonpath.Paths{Talon: talonpath.Layer{Dir: dir}}
	subDir := paths.Talon.SubagentsDir()
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "coding.md"), []byte(`---
tools: [read, grep]
---
Code work.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Resolve([]byte(`{"agents":{"defaults":{"tools":{"enabled":false}}}}`), paths, "coding")
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled || p.Allows("read") {
		t.Fatalf("policy = %+v, want disabled", p)
	}
}
