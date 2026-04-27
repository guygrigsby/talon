package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParsePath splits a config path into segments, mirroring openclaw's parser.
// Supported syntax:
//
//	a.b.c           -> ["a","b","c"]
//	a.b[0].c        -> ["a","b","0","c"]
//	a["b.c"].d      -> ["a","b.c","d"]
//	a.b\.c          -> ["a","b.c"]            (escaped dot)
//	channels.telegram.groups["*"].requireMention
//
// Bare segments inside brackets are accepted as-is (so [0] and [foo] both
// work). Quoted bracket segments must be JSON-string-quoted ("..."); single
// quotes are not supported (openclaw uses JSON5 here, talon doesn't pull a
// JSON5 dep).
func ParsePath(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var (
		out     []string
		current strings.Builder
	)
	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			out = append(out, s)
		}
		current.Reset()
	}
	for i := 0; i < len(trimmed); {
		ch := trimmed[i]
		switch ch {
		case '\\':
			if i+1 < len(trimmed) {
				current.WriteByte(trimmed[i+1])
				i += 2
				continue
			}
			i++
		case '.':
			flush()
			i++
		case '[':
			flush()
			close := strings.IndexByte(trimmed[i+1:], ']')
			if close == -1 {
				return nil, fmt.Errorf("invalid path (missing %q): %s", "]", raw)
			}
			inside := strings.TrimSpace(trimmed[i+1 : i+1+close])
			if inside == "" {
				return nil, fmt.Errorf("invalid path (empty %q): %s", "[]", raw)
			}
			seg, err := parseBracketSegment(inside)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", err, raw)
			}
			out = append(out, seg)
			i += close + 2
		default:
			current.WriteByte(ch)
			i++
		}
	}
	flush()
	if err := validatePathSegments(out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseBracketSegment(inside string) (string, error) {
	if strings.HasPrefix(inside, `"`) {
		var s string
		if err := json.Unmarshal([]byte(inside), &s); err != nil {
			return "", fmt.Errorf("invalid quoted bracket segment %q: %w", inside, err)
		}
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("invalid quoted bracket segment %q (empty)", inside)
		}
		return s, nil
	}
	if strings.HasPrefix(inside, `'`) {
		return "", fmt.Errorf("single-quoted bracket segments are not supported, use double quotes: %q", inside)
	}
	return inside, nil
}

// blockedObjectKeys defends against prototype-pollution-style keys. Mirrors
// openclaw's isBlockedObjectKey.
var blockedObjectKeys = map[string]struct{}{
	"__proto__":   {},
	"prototype":   {},
	"constructor": {},
}

func validatePathSegments(segments []string) error {
	for _, s := range segments {
		if isAllDigits(s) {
			continue
		}
		if _, blocked := blockedObjectKeys[s]; blocked {
			return fmt.Errorf("invalid path segment: %s", s)
		}
	}
	return nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ToSjsonPath renders parsed segments as an sjson/gjson dot path. Numeric
// segments are emitted bare (so they index arrays); other segments have
// sjson metacharacters escaped.
//
// sjson treats these as metacharacters in path strings: '\', '.', '*', '?',
// '#'. We escape them with a backslash so a literal key like "*" becomes
// "\*" in the rendered path.
func ToSjsonPath(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	parts := make([]string, len(segments))
	for i, s := range segments {
		if isAllDigits(s) {
			parts[i] = s
			continue
		}
		parts[i] = escapeSjsonSegment(s)
	}
	return strings.Join(parts, ".")
}

func escapeSjsonSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '.', '*', '?', '#':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SegPath joins segments with dots for human-readable error messages and the
// updated-paths log. Unlike ToSjsonPath, it does not escape — it's not safe
// to feed back into sjson when segments contain metacharacters.
func SegPath(segments []string) string {
	return strings.Join(segments, ".")
}
