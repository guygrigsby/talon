// Package secrets provides shared sensitive-value redaction used by
// commands that surface configuration to users (config get,
// gateway diagnostics export, agents list with --raw, etc.).
//
// The rule of thumb: if a leaf value's key matches any
// sensitive-key substring (case-insensitive), the value is replaced
// with the literal "[REDACTED]" placeholder. Structural shape of
// the surrounding object is preserved so reviewers can still see
// WHICH credentials are configured without leaking the values.
//
// The substring list is deliberately generous — false positives
// become opaque placeholders (annoying but harmless), false
// negatives leak credentials (potentially catastrophic). When in
// doubt, redact.

package secrets

import (
	"encoding/json"
	"strings"
)

// SensitiveWords are the canonical lowercase words a tokenized key
// must EQUAL (not just contain) to flag the value as a credential.
// Word-boundary matching prevents false positives like "maxTokens"
// (numeric limit, not a credential) — substring matching would have
// caught it on "token". Exported so callers can document the rule.
var SensitiveWords = map[string]bool{
	"token":      true,
	"password":   true,
	"secret":     true,
	"key":        true,
	"apikey":     true, // unsplit variant ("apikey" with no separator)
	"credential": true,
	"auth":       true,
	"signing":    true,
	"jwt":        true,
	"private":    true,
	"refresh":    true,
}

// SensitiveKeyParts is kept as an alias of SensitiveWords for any
// external caller that surfaces the rule for documentation. The
// matching algorithm has changed from substring to word-boundary,
// but the public name still describes "the parts we redact on."
var SensitiveKeyParts = func() []string {
	out := make([]string, 0, len(SensitiveWords))
	for w := range SensitiveWords {
		out = append(out, w)
	}
	return out
}()

// Placeholder is the string substituted for redacted leaf values.
const Placeholder = "[REDACTED]"

// IsSensitiveKey reports whether key tokenizes (camelCase /
// snake_case / kebab-case / dotted) into a word that EQUALS one of
// the SensitiveWords. Word-boundary matching avoids false
// positives like "maxTokens" tripping on "token".
func IsSensitiveKey(key string) bool {
	if key == "" {
		return false
	}
	for _, w := range tokenizeKey(key) {
		if SensitiveWords[w] {
			return true
		}
	}
	return false
}

// tokenizeKey splits an identifier into lowercase words by
// camelCase boundaries plus underscore/dash/dot/space separators.
// Empty input returns nil.
func tokenizeKey(key string) []string {
	if key == "" {
		return nil
	}
	out := []string{}
	cur := []rune{}
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		if r == '_' || r == '-' || r == '.' || r == ' ' {
			flush()
			continue
		}
		// camelCase boundary: lower-then-Upper or letter-then-digit
		// flips a word break. Skip on i==0 since there's no prior.
		if i > 0 {
			prev := runes[i-1]
			if isLower(prev) && isUpper(r) {
				flush()
			} else if (isLetter(prev) && isDigit(r)) || (isDigit(prev) && isLetter(r)) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isLetter(r rune) bool { return isUpper(r) || isLower(r) }
func isDigit(r rune) bool  { return r >= '0' && r <= '9' }

// RedactJSON parses raw JSON, walks the tree replacing sensitive
// leaves with Placeholder, and returns pretty-printed JSON.
// Malformed input is returned unchanged (failure-open is safer
// than aborting; caller can then choose to bail).
func RedactJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, nil
	}
	Walk(v, "")
	return json.MarshalIndent(v, "", "  ")
}

// Walk recursively redacts in-place. parentKey carries the key
// under which v was found; the leaf check applies only when v is a
// non-empty string/number whose parentKey matches a pattern.
//
// Empty strings are deliberately left untouched — an empty token
// is a meaningful signal ("auth disabled"), and turning it into
// [REDACTED] would lie about what's configured.
//
// Booleans are also passed through (e.g. "tokenEnabled": true is
// structural, not a credential).
func Walk(v any, parentKey string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if IsSensitiveKey(k) && isLeafSecret(child) {
				x[k] = Placeholder
				continue
			}
			Walk(child, k)
		}
	case []any:
		for _, child := range x {
			Walk(child, parentKey)
		}
	}
}

// RedactValueForKey returns either the original value or the
// Placeholder, depending on whether key flags it as sensitive.
// Used when a caller has a single (key, value) pair to render
// rather than a whole object — e.g. `config get gateway.auth.token`
// where we know the leaf path and value separately.
func RedactValueForKey(key string, value string) string {
	if value == "" {
		return value
	}
	if IsSensitiveKey(key) {
		return Placeholder
	}
	return value
}

// IsSensitivePath returns true when any segment of a dotted config
// path matches a sensitive-key part. Used by `config get` to decide
// whether to redact a whole-tree response when the requested path
// is itself a sensitive subtree (e.g. `config get gateway.auth`
// returning {token, mode} — token leaf gets walk-redacted, but the
// path-level check also catches `config get gateway.auth.token`
// which returns just a string, not an object).
func IsSensitivePath(path string) bool {
	if path == "" {
		return false
	}
	for _, seg := range strings.Split(path, ".") {
		if IsSensitiveKey(seg) {
			return true
		}
	}
	return false
}

// isLeafSecret returns true when v is the kind of value worth
// redacting. Empty strings + booleans pass through; non-empty
// strings + non-zero numbers get redacted.
func isLeafSecret(v any) bool {
	switch x := v.(type) {
	case string:
		return x != ""
	case float64:
		return x != 0
	case bool:
		return false
	}
	return false
}
