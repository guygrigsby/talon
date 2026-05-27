package toolaccess

import (
	"fmt"
	"sort"
	"strings"

	"github.com/guygrigsby/talon/internal/subagents"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// Policy is the LLM-facing tool access decision for one agent.
//
// Enabled=false means no tools are exposed. Restricted=false means all tools
// the runtime assembled are exposed. Restricted=true means only names in
// Allowed are exposed and executable.
type Policy struct {
	Enabled    bool
	Restricted bool
	Allowed    map[string]struct{}
}

// AllowAll is the default policy for existing configs.
func AllowAll() Policy {
	return Policy{Enabled: true}
}

// AllowOnly returns a policy restricted to names. Empty names produces an
// enabled but empty allowlist; callers may use Enabled=false for clearer
// "no tools" intent.
func AllowOnly(names []string) Policy {
	p := Policy{Enabled: true, Restricted: true, Allowed: map[string]struct{}{}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p.Allowed[name] = struct{}{}
	}
	return p
}

// Resolve reads the tool access policy for agentID from the merged runtime
// JSON and, for file-backed subagents, their opencode-style front matter.
func Resolve(merged []byte, paths talonpath.Paths, agentID string) (Policy, error) {
	p := AllowAll()
	applyToolsNode(&p, gjson.GetBytes(merged, "agents.defaults.tools"))

	agent := gjson.GetBytes(merged, fmt.Sprintf(`agents.list.#(id==%q)`, agentID))
	if agent.Exists() {
		applyToolsNode(&p, agent.Get("tools"))
		return p, nil
	}

	if paths.Talon.Dir != "" {
		def, ok, err := subagents.Find(paths.Talon.SubagentsDir(), agentID)
		if err != nil {
			return p, err
		}
		if ok && len(def.Tools) > 0 {
			p.setAllowed(def.Tools)
		}
	}
	return p, nil
}

// Allows reports whether name may be exposed/executed under p.
func (p Policy) Allows(name string) bool {
	if !p.Enabled {
		return false
	}
	if !p.Restricted {
		return true
	}
	_, ok := p.Allowed[name]
	return ok
}

// Names returns the allowed names in stable order. Empty when the policy is
// disabled or unrestricted.
func (p Policy) Names() []string {
	if !p.Enabled || !p.Restricted {
		return nil
	}
	out := make([]string, 0, len(p.Allowed))
	for name := range p.Allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func applyToolsNode(p *Policy, node gjson.Result) {
	if !node.Exists() {
		return
	}
	if node.IsArray() || node.Type == gjson.String {
		p.setAllowed(gjsonStringList(node))
		return
	}
	if !node.IsObject() {
		return
	}
	if enabled := node.Get("enabled"); enabled.Exists() {
		p.Enabled = enabled.Bool()
	}
	for _, key := range []string{"allow", "allowed"} {
		if v := node.Get(key); v.Exists() {
			p.setAllowed(gjsonStringList(v))
			return
		}
	}
}

func (p *Policy) setAllowed(names []string) {
	next := AllowOnly(names)
	p.Restricted = true
	p.Allowed = next.Allowed
}

func gjsonStringList(v gjson.Result) []string {
	var out []string
	add := func(s string) {
		for _, item := range strings.Split(s, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
	}
	switch {
	case v.IsArray():
		v.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				add(item.Str)
			}
			return true
		})
	case v.Type == gjson.String:
		add(v.Str)
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
