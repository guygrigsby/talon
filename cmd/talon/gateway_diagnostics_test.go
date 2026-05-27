package main

import (
	"archive/zip"
	"encoding/json"
	"strings"
	"testing"
)

func zipReader(path string) (*zip.ReadCloser, error) {
	return zip.OpenReader(path)
}

func TestRedactConfigJSON_RedactsSensitiveLeaves(t *testing.T) {
	in := `{
		"gateway": {
			"port": 18790,
			"auth": {"mode": "token", "token": "abc123"}
		},
		"channels": {
			"telegram": {"botToken": "secret", "agentId": "main", "enabled": true}
		},
		"agents": {
			"list": [
				{"id": "main", "auth": {"apiKey": "sk-real-key"}}
			]
		}
	}`
	out, err := redactConfigJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{
		`"token": "[REDACTED]"`,
		`"botToken": "[REDACTED]"`,
		`"apiKey": "[REDACTED]"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
	for _, leak := range []string{"abc123", "secret", "sk-real-key"} {
		if strings.Contains(got, leak) {
			t.Errorf("redactor leaked %q in output:\n%s", leak, got)
		}
	}
	// Non-secret structural values must be preserved unchanged so
	// reviewers can see what's configured.
	for _, keep := range []string{`"port": 18790`, `"mode": "token"`, `"enabled": true`, `"agentId": "main"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("redactor clobbered non-secret %q:\n%s", keep, got)
		}
	}
}

func TestRedactConfigJSON_PreservesEmptyValues(t *testing.T) {
	// Empty token is a meaningful signal ("auth disabled") — don't
	// turn it into [REDACTED] which would lie about what's configured.
	in := `{"gateway": {"auth": {"token": ""}}}`
	out, _ := redactConfigJSON([]byte(in))
	if !strings.Contains(string(out), `"token": ""`) {
		t.Errorf("empty token should not be redacted:\n%s", out)
	}
}

func TestRedactConfigJSON_HandlesArraysOfObjects(t *testing.T) {
	in := `{"providers": [{"name": "openai", "apiKey": "k1"}, {"name": "anthropic", "apiKey": "k2"}]}`
	out, _ := redactConfigJSON([]byte(in))
	got := string(out)
	if strings.Contains(got, "k1") || strings.Contains(got, "k2") {
		t.Errorf("apiKey leaked through array walk:\n%s", got)
	}
	// Both entries should still be present in shape.
	if !strings.Contains(got, `"name": "openai"`) || !strings.Contains(got, `"name": "anthropic"`) {
		t.Errorf("array walk dropped structural entries:\n%s", got)
	}
}

func TestRedactConfigJSON_BadInputPassesThrough(t *testing.T) {
	// Malformed JSON shouldn't fail the export — better to ship a
	// raw config and let the reviewer see the parse failure than to
	// abort the whole bundle.
	in := []byte(`not really json`)
	out, err := redactConfigJSON(in)
	if err != nil {
		t.Fatalf("bad-json passthrough failed: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("bad-json input should round-trip unchanged: got %q", out)
	}
}

func TestShouldRedact_KeyPatterns(t *testing.T) {
	cases := map[string]bool{
		"token":          true,
		"botToken":       true,
		"api_key":        true,
		"apiKey":         true,
		"PRIVATE_KEY":    true,
		"clientSecret":   true,
		"auth":           true,
		"enabled":        false,
		"port":           false,
		"name":           false,
		"agentId":        false,
		"":               false,
		"refreshToken":   true,
		"signing_secret": true,
	}
	for key, want := range cases {
		if got := shouldRedact(key); got != want {
			t.Errorf("shouldRedact(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRedactWalk_NoOpOnNonContainers(t *testing.T) {
	// Don't crash on scalar inputs — defense-in-depth for malformed
	// top-level config.
	cases := []any{nil, "string", 42, true, []any{1, 2, 3}}
	for _, c := range cases {
		redactWalk(c, "") // panics on bad type assertion if buggy
	}
}

func TestDiagnosticsExportEnd2End_NoGateway(t *testing.T) {
	// timeoutMs=0 disables the health probe, so this exercises the
	// rest of the export without needing a live gateway.
	tmpDir := t.TempDir()
	zipPath := tmpDir + "/diag.zip"
	opts := diagnosticsExportOpts{
		output:   zipPath,
		jsonOut:  true,
		logLines: 100,
		logBytes: 10000,
	}
	if err := diagnosticsExportRunE(opts); err != nil {
		t.Fatalf("export: %v", err)
	}
	// Manifest is the contract — verify it exists and lists at
	// least config.json + paths.json.
	contents := readZipManifest(t, zipPath)
	included, _ := contents["contents"].([]any)
	if len(included) < 2 {
		t.Fatalf("expected at least 2 files in manifest, got %v", included)
	}
	seen := map[string]bool{}
	for _, c := range included {
		if s, ok := c.(string); ok {
			seen[s] = true
		}
	}
	if !seen["config.json"] || !seen["paths.json"] || !seen["manifest.json"] && false /* manifest not in own contents list */ {
		t.Errorf("missing required entries in manifest contents: %v", seen)
	}
}

// readZipManifest extracts manifest.json from path and returns the
// parsed object. Helper kept local to this test file.
func readZipManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	r, err := zipReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open manifest entry: %v", err)
		}
		defer func() { _ = rc.Close() }()
		var m map[string]any
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return m
	}
	t.Fatal("manifest.json not found in zip")
	return nil
}
