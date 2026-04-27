package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/openclaw"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaMsgPrinter is the localized-message printer the jsonschema v6 API
// requires; nil panics inside x/text. We only render English.
var schemaMsgPrinter = message.NewPrinter(language.English)

// ErrSchemaNotCached is returned by LoadCachedSchema when the talon overlay
// has no schema cache yet. Callers should fall back to syntax-only
// validation or prompt the user to run "talon config schema --refresh".
var ErrSchemaNotCached = errors.New("config schema not cached; run 'talon config schema --refresh'")

// ErrSchemaCompileFailed wraps a jsonschema compilation error. The cache is
// present but the schema is unusable (typically: dangling $ref, unsupported
// dialect). Default callers treat this like a missing cache and fall back to
// syntax-only validation; --strict callers surface the wrapped error.
type ErrSchemaCompileFailed struct{ Err error }

func (e *ErrSchemaCompileFailed) Error() string {
	return "config schema cache is unusable: " + e.Err.Error()
}

func (e *ErrSchemaCompileFailed) Unwrap() error { return e.Err }

// SchemaEnvelope is the shape of the gateway's config.schema RPC response:
// {"generatedAt": "...", "schema": {...}}. We cache it whole so we can
// expose generatedAt to the user, and we extract the schema field for
// validation.
type SchemaEnvelope struct {
	GeneratedAt string          `json:"generatedAt"`
	Schema      json.RawMessage `json:"schema"`
}

// LoadCachedSchema reads the cached schema for the talon layer, parses it
// with jsonschema, and returns the compiled validator + the envelope
// (carrying generatedAt for display).
func LoadCachedSchema(p openclaw.Paths) (*jsonschema.Schema, *SchemaEnvelope, error) {
	path := p.Talon.SchemaCachePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrSchemaNotCached
		}
		return nil, nil, fmt.Errorf("read schema cache %s: %w", path, err)
	}
	var env SchemaEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("parse schema cache %s: %w", path, err)
	}
	if len(env.Schema) == 0 {
		return nil, nil, fmt.Errorf("schema cache %s has no .schema field", path)
	}
	compiled, err := compileSchema(env.Schema)
	if err != nil {
		return nil, &env, &ErrSchemaCompileFailed{Err: err}
	}
	return compiled, &env, nil
}

func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	var schemaDoc any
	if err := json.Unmarshal(raw, &schemaDoc); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("config.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("register schema: %w", err)
	}
	s, err := c.Compile("config.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return s, nil
}

// WriteSchemaCache atomically writes a SchemaEnvelope to the talon overlay's
// cache directory. raw must be a JSON object containing at least a "schema"
// field; we store the whole envelope as-is so callers can preserve other
// fields (e.g. generatedAt).
func WriteSchemaCache(p openclaw.Paths, raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("schema response is not valid JSON")
	}
	var env SchemaEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("schema response shape unexpected: %w", err)
	}
	if len(env.Schema) == 0 {
		return fmt.Errorf("schema response missing .schema field")
	}
	// Schema must be a JSON object (jsonschema needs an object root).
	trimmed := bytes.TrimSpace(env.Schema)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("schema response .schema must be a JSON object, got %s", string(trimmed))
	}
	if err := os.MkdirAll(p.Talon.CacheDir(), 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	// Re-marshal with stable formatting so cache diffs are reviewable.
	pretty := bytes.Buffer{}
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return fmt.Errorf("format schema cache: %w", err)
	}
	pretty.WriteByte('\n')
	return writeFile(p.Talon.SchemaCachePath(), pretty.Bytes(), 0o600)
}

// ValidateMerged validates the merged config against the cached schema.
// Returns ErrSchemaNotCached when no cache exists; the caller decides
// whether to fall back to syntax-only validation.
func ValidateMerged(p openclaw.Paths) (*ValidationResult, error) {
	merged, err := MergedBytes(p)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(merged, &doc); err != nil {
		return nil, fmt.Errorf("parse merged config: %w", err)
	}
	schema, env, err := LoadCachedSchema(p)
	if err != nil {
		return nil, err
	}
	res := &ValidationResult{SchemaGeneratedAt: env.GeneratedAt}
	if vErr := schema.Validate(doc); vErr != nil {
		var ve *jsonschema.ValidationError
		if errors.As(vErr, &ve) {
			res.Issues = flattenValidationError(ve)
		} else {
			res.Issues = []ValidationIssue{{Message: vErr.Error()}}
		}
	}
	return res, nil
}

// ValidationResult is the parsed outcome of a schema-aware validation.
type ValidationResult struct {
	SchemaGeneratedAt string            `json:"schemaGeneratedAt,omitempty"`
	Issues            []ValidationIssue `json:"issues,omitempty"`
}

// Valid returns true when no issues were reported.
func (r *ValidationResult) Valid() bool { return r != nil && len(r.Issues) == 0 }

// ValidationIssue is a flattened jsonschema error.
type ValidationIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// flattenValidationError walks the jsonschema causal tree into a flat list of
// (path, message) pairs that's easy to render in CLI output.
func flattenValidationError(ve *jsonschema.ValidationError) []ValidationIssue {
	var out []ValidationIssue
	var walk func(node *jsonschema.ValidationError)
	walk = func(node *jsonschema.ValidationError) {
		if len(node.Causes) == 0 {
			out = append(out, ValidationIssue{
				Path:    instancePath(node),
				Message: node.ErrorKind.LocalizedString(schemaMsgPrinter),
			})
			return
		}
		for _, c := range node.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, ValidationIssue{Message: ve.Error()})
	}
	return out
}

func instancePath(ve *jsonschema.ValidationError) string {
	if len(ve.InstanceLocation) == 0 {
		return "<root>"
	}
	return strings.Join(ve.InstanceLocation, ".")
}

// FormatGeneratedAt renders a schema's generatedAt for human display, or
// falls back to "(unknown)" when missing/unparseable.
func FormatGeneratedAt(s string) string {
	if s == "" {
		return "(unknown)"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
