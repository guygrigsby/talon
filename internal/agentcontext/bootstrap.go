package agentcontext

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// bootstrapName is the first-run onboarding sentinel. Its presence in a
// workspace means "not set up yet"; the agent removes it once onboarding
// completes (see ApplyOnboarding). Deliberately not in canonicalOrder so
// Build never renders it as a persona section and EnsureDefaults never
// seeds it.
const bootstrapName = "BOOTSTRAP.md"

//go:embed templates/BOOTSTRAP.md
var bootstrapTemplate embed.FS

// onboardingDirective leads the system prompt while onboarding is active.
// It's a hard instruction; the BOOTSTRAP.md body (user-editable) follows.
const onboardingDirective = `# FIRST-RUN ONBOARDING (ACTIVE)

You have not been set up yet. Before anything else, run your onboarding:
introduce yourself, then interview the user conversationally to learn who you
should be and who they are. When you have enough, call the ` + "`finish_onboarding`" + ` tool
with the collected values — that writes your IDENTITY.md and USER.md and clears
this onboarding. Do not invent answers, and do not skip the interview. Keep it
short and warm.

`

// IsFresh reports whether a workspace has no persona files yet — the
// signal that this is a genuinely first run worth arming onboarding for.
func IsFresh(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range canonicalOrder {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return false
		}
	}
	return true
}

// EnsureBootstrap writes the onboarding sentinel into dir when absent.
// Returns whether it wrote the file. Never rewrites an existing sentinel
// (so completed onboarding stays completed) and never recreates one the
// agent deleted. Callers gate this on IsFresh so it only arms on a
// genuinely new workspace.
func EnsureBootstrap(dir string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	dst := filepath.Join(dir, bootstrapName)
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", dst, err)
	}
	body, err := bootstrapTemplate.ReadFile("templates/" + bootstrapName)
	if err != nil {
		return false, fmt.Errorf("read embedded %s: %w", bootstrapName, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create workspace %s: %w", dir, err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	return true, nil
}

// BootstrapActive reports whether onboarding is in progress (the sentinel
// is present) for the given workspace.
func BootstrapActive(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, bootstrapName))
	return err == nil
}

// BootstrapPrompt returns the leading onboarding directive (the hard
// instruction plus the user-editable BOOTSTRAP.md body) when onboarding
// is active, else "". Composed ahead of persona context so the agent
// runs onboarding before anything else on the first turn.
func BootstrapPrompt(dir string) string {
	if !BootstrapActive(dir) {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(dir, bootstrapName))
	if err != nil {
		return strings.TrimRight(onboardingDirective, "\n")
	}
	return onboardingDirective + strings.TrimRight(string(body), "\n")
}

// PersonaFields carries the identity + user facts collected during
// onboarding. AgentName is required; the rest are best-effort.
type PersonaFields struct {
	AgentName string
	Creature  string
	Vibe      string
	Emoji     string
	Avatar    string

	UserName     string
	UserCall     string
	UserTimezone string
	UserNotes    string
}

// ApplyOnboarding writes IDENTITY.md and USER.md from collected fields,
// then removes the onboarding sentinel. SOUL.md (behavioral persona) is
// left at its default. On any write error the sentinel is preserved so
// the agent can retry. Requires a non-empty AgentName.
func ApplyOnboarding(dir string, f PersonaFields) error {
	if dir == "" {
		return fmt.Errorf("onboarding: empty workspace dir")
	}
	if strings.TrimSpace(f.AgentName) == "" {
		return fmt.Errorf("onboarding: agent name is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workspace %s: %w", dir, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte(renderIdentity(f)), 0o644); err != nil {
		return fmt.Errorf("write IDENTITY.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "USER.md"), []byte(renderUser(f)), 0o644); err != nil {
		return fmt.Errorf("write USER.md: %w", err)
	}

	// Clear the sentinel last — only once the files are safely written.
	if err := os.Remove(filepath.Join(dir, bootstrapName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", bootstrapName, err)
	}
	return nil
}

func renderIdentity(f PersonaFields) string {
	avatar := f.Avatar
	if strings.TrimSpace(avatar) == "" {
		avatar = "_(none yet)_"
	}
	return fmt.Sprintf(`# IDENTITY.md — Who Am I?

- **Name:** %s
- **Creature:** %s
- **Vibe:** %s
- **Emoji:** %s
- **Avatar:** %s

---

Set during first-run onboarding. Edit when something fundamental about who you
are changes.
`, orDash(f.AgentName), orDash(f.Creature), orDash(f.Vibe), orDash(f.Emoji), avatar)
}

func renderUser(f PersonaFields) string {
	return fmt.Sprintf(`# USER.md — About Your Human

- **Name:** %s
- **What to call them:** %s
- **Timezone:** %s
- **Notes:** %s

## Context

_(Build this over time.)_
`, orDash(f.UserName), orDash(f.UserCall), orDash(f.UserTimezone), orDash(f.UserNotes))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "_(unknown)_"
	}
	return s
}
