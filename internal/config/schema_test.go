package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// minimalSchemaEnvelope is a small but realistic envelope shape. It exercises:
// additionalProperties:false, type checking, nested objects, and required.
const minimalSchemaEnvelope = `{
  "generatedAt": "2026-04-27T12:00:00Z",
  "schema": {
    "$schema": "http://json-schema.org/draft-07/schema#",
    "type": "object",
    "additionalProperties": true,
    "properties": {
      "gateway": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "port": {"type": "integer", "minimum": 1, "maximum": 65535},
          "mode": {"type": "string"},
          "bind": {"type": "string", "enum": ["loopback","lan","tailnet","auto","custom"]},
          "auth": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "mode":  {"type": "string", "enum": ["none","token","password","trusted-proxy"]},
              "token": {"type": "string"}
            }
          }
        }
      }
    }
  }
}`

func TestWriteAndLoadCachedSchema_Roundtrip(t *testing.T) {
	p := fixture(t, "", "")
	if err := WriteSchemaCache(p, []byte(minimalSchemaEnvelope)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Talon.SchemaCachePath()); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
	schema, env, err := LoadCachedSchema(p)
	if err != nil {
		t.Fatal(err)
	}
	if schema == nil {
		t.Errorf("compiled schema is nil")
	}
	if env.GeneratedAt == "" {
		t.Errorf("generatedAt missing on envelope")
	}
}

func TestLoadCachedSchema_ErrSchemaNotCached(t *testing.T) {
	p := fixture(t, "", "")
	_, _, err := LoadCachedSchema(p)
	if !errors.Is(err, ErrSchemaNotCached) {
		t.Errorf("err = %v, want ErrSchemaNotCached", err)
	}
}

func TestWriteSchemaCache_RejectsInvalidShape(t *testing.T) {
	p := fixture(t, "", "")
	cases := []string{
		`not json`,
		`{}`,                              // missing .schema
		`{"generatedAt":"x","schema":42}`, // schema not an object
		`{"generatedAt":"x","schema":""}`, // empty schema string
	}
	for _, c := range cases {
		if err := WriteSchemaCache(p, []byte(c)); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

// --- ValidateMerged: schema-aware acceptance + rejection -------------------

func TestValidateMerged_AcceptsValidConfig(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"port":18789,"bind":"loopback","auth":{"mode":"token","token":"abc"}}}`)
	if err := WriteSchemaCache(p, []byte(minimalSchemaEnvelope)); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateMerged(p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid() {
		t.Errorf("expected valid, got issues: %+v", res.Issues)
	}
}

func TestValidateMerged_RejectsBadEnum(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"bind":"not-a-mode"}}`)
	if err := WriteSchemaCache(p, []byte(minimalSchemaEnvelope)); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateMerged(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid() {
		t.Errorf("expected schema rejection for bad enum, got valid")
	}
	found := false
	for _, issue := range res.Issues {
		if strings.Contains(issue.Path, "gateway.bind") || strings.Contains(issue.Message, "not-a-mode") {
			found = true
		}
	}
	if !found {
		t.Errorf("issues should mention gateway.bind or the bad value: %+v", res.Issues)
	}
}

func TestValidateMerged_RejectsAdditionalProperties(t *testing.T) {
	// gateway.auth has additionalProperties:false in this cached schema.
	// Use a real native-preserved field that the schema intentionally omits.
	p := fixture(t, "", `{"gateway":{"auth":{"mode":"password","password":"x"}}}`)
	if err := WriteSchemaCache(p, []byte(minimalSchemaEnvelope)); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateMerged(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid() {
		t.Errorf("expected rejection for additionalProperties:false violation")
	}
}

func TestValidateMerged_AcceptsMergedFromBothLayers(t *testing.T) {
	// Schema validates the merged view; an invalid override must be caught
	// even when the base would be valid.
	p := fixture(t,
		`{"gateway":{"port":18789}}`,
		`{"gateway":{"port":99999999}}`, // out of range per schema (max 65535)
	)
	if err := WriteSchemaCache(p, []byte(minimalSchemaEnvelope)); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateMerged(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid() {
		t.Errorf("expected rejection because merged port is out of schema range")
	}
}

func TestValidateMerged_NoCacheReturnsErrSchemaNotCached(t *testing.T) {
	p := fixture(t, "", `{"gateway":{"port":18789}}`)
	_, err := ValidateMerged(p)
	if !errors.Is(err, ErrSchemaNotCached) {
		t.Errorf("err = %v, want ErrSchemaNotCached", err)
	}
}

func TestValidateMerged_DanglingRefReturnsCompileFailedError(t *testing.T) {
	// A $ref to a $defs block that is not present at the document root
	// should surface as a compile failure.
	const danglingRefSchema = `{
  "generatedAt": "2026-04-27T12:00:00Z",
  "schema": {
    "$schema": "http://json-schema.org/draft-07/schema#",
    "type": "object",
    "properties": {
      "x": {"$ref": "#/$defs/missing"}
    }
  }
}`
	p := fixture(t, "", `{"x":1}`)
	if err := WriteSchemaCache(p, []byte(danglingRefSchema)); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateMerged(p)
	var compileErr *ErrSchemaCompileFailed
	if !errors.As(err, &compileErr) {
		t.Errorf("err = %v, want ErrSchemaCompileFailed", err)
	}
}

// --- ExtractSchemaSection -------------------------------------------------

func TestExtractSchemaSection_Toplevel(t *testing.T) {
	got, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), "gateway")
	if err != nil {
		t.Fatal(err)
	}
	// Should be the gateway subschema (object with type:object and properties).
	if !strings.Contains(string(got), `"type": "object"`) || !strings.Contains(string(got), `"port"`) {
		t.Errorf("section output looks wrong: %s", got)
	}
}

func TestExtractSchemaSection_Nested(t *testing.T) {
	got, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), "gateway.auth")
	if err != nil {
		t.Fatal(err)
	}
	// Should be the auth subschema with mode/token props.
	if !strings.Contains(string(got), `"mode"`) || !strings.Contains(string(got), `"token"`) {
		t.Errorf("nested section output looks wrong: %s", got)
	}
	// Should NOT contain the sibling 'port' key.
	if strings.Contains(string(got), `"port"`) {
		t.Errorf("nested section leaked the parent's siblings: %s", got)
	}
}

func TestExtractSchemaSection_EmptyReturnsEnvelopeUnchanged(t *testing.T) {
	got, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != minimalSchemaEnvelope {
		t.Errorf("empty section should return raw unchanged")
	}
}

func TestExtractSchemaSection_MissingSection(t *testing.T) {
	_, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), "noSuchSection")
	if err == nil {
		t.Errorf("expected error for missing section")
	}
}

func TestExtractSchemaSection_EmptySegmentRejected(t *testing.T) {
	_, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), "gateway..auth")
	if err == nil {
		t.Errorf("expected error for empty segment")
	}
	if _, err := ExtractSchemaSection([]byte(minimalSchemaEnvelope), ".gateway"); err == nil {
		t.Errorf("expected error for leading dot")
	}
}

func TestFormatGeneratedAt(t *testing.T) {
	if got := FormatGeneratedAt(""); got != "(unknown)" {
		t.Errorf("empty = %q, want (unknown)", got)
	}
	if got := FormatGeneratedAt("2026-04-27T12:00:00Z"); got == "" {
		t.Errorf("formatted output should be non-empty")
	}
	// Unparseable falls through to the raw string.
	if got := FormatGeneratedAt("not a timestamp"); got != "not a timestamp" {
		t.Errorf("unparseable should fall through, got %q", got)
	}
}
