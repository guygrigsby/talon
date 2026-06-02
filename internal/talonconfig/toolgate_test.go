package talonconfig

import (
	"testing"

	"github.com/tidwall/gjson"
)

// toolgate.mode + toolgate.defaults.allow survive the full round trip:
// runtime JSON -> native -> TOML -> native -> runtime JSON (the path the
// gateway's MergedBytes takes).
func TestToolgate_RoundTrip(t *testing.T) {
	rj := `{"toolgate":{"mode":"audit","defaults":{"allow":["exec","net.out"]}},"agents":{"list":[{"id":"main"}]}}`
	cfg, err := FromRuntimeJSON([]byte(rj))
	if err != nil {
		t.Fatalf("FromRuntimeJSON: %v", err)
	}
	if cfg.Toolgate.Mode != "audit" {
		t.Fatalf("FromRuntimeJSON mode = %q, want audit", cfg.Toolgate.Mode)
	}
	if len(cfg.Toolgate.Defaults.Allow) != 2 {
		t.Fatalf("FromRuntimeJSON allow = %v, want 2 entries", cfg.Toolgate.Defaults.Allow)
	}

	toml := MarshalTOML(cfg, MarshalOptions{})
	back, err := ReadTOMLBytes(toml)
	if err != nil {
		t.Fatalf("ReadTOMLBytes: %v\nTOML:\n%s", err, toml)
	}
	if back.Toolgate.Mode != "audit" {
		t.Fatalf("after TOML round trip mode = %q, want audit\nTOML:\n%s", back.Toolgate.Mode, toml)
	}
	if len(back.Toolgate.Defaults.Allow) != 2 {
		t.Fatalf("after TOML round trip allow = %v, want 2\nTOML:\n%s", back.Toolgate.Defaults.Allow, toml)
	}

	out, err := ToRuntimeJSON(back, nil)
	if err != nil {
		t.Fatalf("ToRuntimeJSON: %v", err)
	}
	if got := gjson.GetBytes(out, "toolgate.mode").Str; got != "audit" {
		t.Fatalf("runtime toolgate.mode = %q, want audit\n%s", got, out)
	}
	if got := gjson.GetBytes(out, "toolgate.defaults.allow").Array(); len(got) != 2 {
		t.Fatalf("runtime toolgate.defaults.allow = %v, want 2", got)
	}
}
