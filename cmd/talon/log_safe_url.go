package main

// logSafeURL strips the value of any `token=...` parameter (in
// either the query string or the fragment) so the URL can be
// printed to gateway stdout without leaking credentials into
// Docker logs / terminal scrollback. Empty tokens are left as-is
// — that's a meaningful diagnostic (auth=none) the operator
// should still see.
//
// Conservative implementation: regex-based replacement on the
// raw string, no URL parsing. Catches the canonical token=...
// shape regardless of percent-encoding around it; doesn't try
// to parse and rebuild the URL (which would be lossy across
// platforms' subtly-different escape rules).

import "regexp"

// tokenParamRe matches a `token=<non-empty-value>` segment.
// Value runs up to the next & or # or end-of-string. The
// (=)([^&#]+) capture lets the replacement keep the equals
// sign and substitute only the value.
var tokenParamRe = regexp.MustCompile(`(?i)(\btoken)(=)([^&#]+)`)

func logSafeURL(u string) string {
	return tokenParamRe.ReplaceAllString(u, "${1}${2}[set]")
}
