//go:build darwin

package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// postNotification triggers a macOS notification via osascript's
// `display notification` command. To dodge AppleScript injection
// entirely the script is a fixed string and the user-supplied data
// is read inside the script via `system attribute "TALON_NOTIFY_*"`,
// which pulls from the environment we set on cmd.Env. Nothing the
// agent supplies is ever interpolated into AppleScript source.
//
// `display notification` accepts only title + subtitle + body +
// optional sound name; that's the entire customizable surface
// without dropping to a Notification Service Extension or a bundled
// helper app, both of which are out of scope for v1.
func postNotification(ctx context.Context, title, body, subtitle, sound string) error {
	const script = `
on run
	set theTitle to system attribute "TALON_NOTIFY_TITLE"
	set theBody to system attribute "TALON_NOTIFY_BODY"
	set theSubtitle to system attribute "TALON_NOTIFY_SUBTITLE"
	set theSound to system attribute "TALON_NOTIFY_SOUND"
	if theSubtitle is "" then
		if theSound is "" then
			display notification theBody with title theTitle
		else
			display notification theBody with title theTitle sound name theSound
		end if
	else
		if theSound is "" then
			display notification theBody with title theTitle subtitle theSubtitle
		else
			display notification theBody with title theTitle subtitle theSubtitle sound name theSound
		end if
	end if
end run
`
	// Cap the osascript invocation. display notification returns
	// nearly instantly; a 5s ceiling is just to avoid hanging if
	// something pathological is happening (locked screen, denied
	// notifications permission spinning, etc).
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "osascript", "-e", script)
	cmd.Env = append(cmd.Environ(),
		"TALON_NOTIFY_TITLE="+title,
		"TALON_NOTIFY_BODY="+body,
		"TALON_NOTIFY_SUBTITLE="+subtitle,
		"TALON_NOTIFY_SOUND="+sound,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w (output: %s)", err, truncate(string(out), 256))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
