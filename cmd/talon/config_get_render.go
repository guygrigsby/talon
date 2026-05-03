package main

// renderConfigGetValue is the redact-aware renderer for `config
// get <path>`. Splits the existing inline switch out into its own
// function so the test suite can drive it without spinning up
// Cobra. Redacts sensitive values by default — the user has to
// pass --reveal to opt back into cleartext display, which is the
// right inversion of defaults given how easy it is to paste config
// output into a chat or screenshot.

import (
	"encoding/json"

	"github.com/guygrigsby/talon/internal/secrets"
	"github.com/tidwall/gjson"
)

// renderConfigGetValue returns the string to print and a flag
// indicating whether redaction was applied (so the caller can
// emit a hint about --reveal). raw=true prints the gjson raw
// value verbatim; reveal=true bypasses redaction.
func renderConfigGetValue(path string, res gjson.Result, raw, reveal bool) (string, bool) {
	if reveal {
		return rawConfigValue(res, raw), false
	}
	pathSensitive := secrets.IsSensitivePath(path)

	switch res.Type {
	case gjson.False, gjson.Number, gjson.True:
		return rawConfigValue(res, raw), false
	case gjson.String:
		if pathSensitive && res.Str != "" {
			return secrets.Placeholder, true
		}
		if raw {
			return res.Raw, false
		}
		return res.Str, false
	default:
		// Object / array — redact in-place via secrets.RedactJSON,
		// which preserves structure but replaces sensitive leaves.
		// `redacted` reflects whether anything was actually masked
		// vs. just structurally-walked, so plain objects don't
		// trigger the --reveal hint.
		redactedBytes, err := secrets.RedactJSON([]byte(res.Raw))
		if err != nil {
			return res.Raw, false
		}
		// Detect whether the placeholder appeared anywhere in the
		// redacted output (cheap and accurate; the placeholder
		// never appears in plain config). If so, flag redacted.
		redacted := false
		for i := 0; i+len(secrets.Placeholder) <= len(redactedBytes); i++ {
			if string(redactedBytes[i:i+len(secrets.Placeholder)]) == secrets.Placeholder {
				redacted = true
				break
			}
		}
		return string(redactedBytes), redacted
	}
}

// rawConfigValue replicates the original switch's leaf-rendering
// for non-string values: when --raw is set, print res.Raw; else
// print the type-appropriate display (numbers / bools as-is,
// strings unwrapped, objects pretty-printed).
func rawConfigValue(res gjson.Result, raw bool) string {
	if raw {
		return res.Raw
	}
	switch res.Type {
	case gjson.False, gjson.Number:
		return res.Raw
	case gjson.True:
		return "true"
	case gjson.String:
		return res.Str
	default:
		var v any
		if err := json.Unmarshal([]byte(res.Raw), &v); err != nil {
			return res.Raw
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}
}
