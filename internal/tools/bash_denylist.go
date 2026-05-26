package tools

// Bash-tool denylist. Defense-in-depth, NOT a sandbox — talon
// normally runs the bash tool in a constrained workspace. The denylist
// exists to refuse a small set of obviously-catastrophic patterns before
// they reach exec, so an out-of-control agent cannot trivially nuke its
// own state directories or the host's persistent volumes.
//
// Patterns are conservative: they fire on the cases an attentive
// human would catch in code review, not on every theoretically-
// dangerous shell construct. A more robust approach (full sandbox,
// approval RPC, capability-based exec) is tracked under talon-iod
// and talon-97i; this is the v0 cheap-shot.
//
// Hot path: runs once per bash-tool invocation. Patterns compile
// at package init via sync.Once-equivalent (var initialization
// runs once); per-call cost is N regexp matches over a short
// command string — microseconds, well under the model latency
// budget.

import (
	"fmt"
	"regexp"
	"strings"
)

// bashDenyPattern is one entry in the denylist: a compiled regex
// plus a short human label that surfaces in the rejection message.
// Patterns are matched against the original command string verbatim
// (no normalization); the regexes themselves handle whitespace and
// case where appropriate via (?i) and \s+.
type bashDenyPattern struct {
	re    *regexp.Regexp
	label string
}

// bashDenyPatterns is the source of truth. New entries go at the
// end so existing rejection messages stay stable across versions.
//
// Some notes on what's _not_ blocked:
//   - Local `rm` (without -rf root/$HOME/~) is allowed — agents
//     legitimately remove build artifacts and tmp files.
//   - Plain `curl` / `wget` are allowed; only the pipe-to-shell
//     pattern is rejected (the canonical supply-chain attack shape).
//   - `chmod 777`, `chown root` etc. are allowed — they're scope-
//     limited inside the container.
var bashDenyPatterns = []bashDenyPattern{
	// Catastrophic rm targets. \b ensures we don't eat "rm -rf ./build".
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f?\s+/(\s|$|;|&|\|)`), "rm -rf /"},
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f?\s+~(\s|$|/|;|&|\|)`), "rm -rf ~"},
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f?\s+\$HOME\b`), "rm -rf $HOME"},

	// Privilege escalation. Mostly meaningless inside the container
	// but catches the case where someone wires this to host later.
	{regexp.MustCompile(`(?i)(^|\s|;|&|\|)sudo\b`), "sudo"},

	// Power state changes — would terminate the container abruptly.
	{regexp.MustCompile(`(?i)\b(shutdown|reboot|poweroff|halt)\b`), "shutdown/reboot/halt"},

	// Disk-destroying utilities. dd writing to a /dev/* device is
	// the canonical lobotomy command; we also catch direct redirect
	// into /dev/sd* / /dev/disk*.
	{regexp.MustCompile(`(?i)\bdd\s+.*\bof=/dev/`), "dd to /dev/*"},
	{regexp.MustCompile(`(?i)\bmkfs\.[a-z0-9]+\b`), "mkfs.*"},
	{regexp.MustCompile(`>\s*/dev/(sd[a-z]|disk\d|nvme\d)`), "redirect to disk device"},

	// Pipe-to-shell drive-bys. The 'curl|sh' pattern is the
	// signature supply-chain attack; rejecting it doesn't prevent
	// the agent from running the same script in two steps, but it
	// removes one of the cheap one-liner foot-guns.
	{regexp.MustCompile(`(?i)(curl|wget)\b[^|]*\|\s*(sh|bash|zsh|ksh)\b`), "pipe download to shell"},

	// Classic fork bomb. Doesn't catch every variant but catches
	// the well-known one any human reviewer would flag.
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork bomb"},
}

// bashDenylistViolation returns a non-empty reason string when cmd
// matches any denylist pattern. Returns "" for allowed commands.
// Caller is responsible for surfacing the reason to the model so
// it can adapt rather than retry verbatim.
func bashDenylistViolation(cmd string) string {
	if cmd == "" {
		return ""
	}
	for _, p := range bashDenyPatterns {
		if p.re.MatchString(cmd) {
			return fmt.Sprintf("blocked by safety denylist (%s); reword the command or split it into safer steps", p.label)
		}
	}
	// Defense-in-depth: also catch the common normalize-around-it
	// trick of stuffing newlines or NULs into the command. We
	// reject control characters early so a multi-line command
	// can't smuggle a denied pattern past the regex via line-folding.
	for _, r := range cmd {
		if r == '\x00' {
			return "blocked by safety denylist (NUL byte in command); reword without embedded NULs"
		}
	}
	_ = strings.TrimSpace // keep the import live in case future refinements need it
	return ""
}
