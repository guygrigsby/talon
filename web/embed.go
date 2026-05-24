// Package web embeds the SvelteKit production build (`pnpm --dir web build`
// output) into the talon binary. The gateway's HTTP mux serves Assets when
// no `--web <dir>` override is set.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var rawAssets embed.FS

// Assets is the embedded build/ directory rooted at "build/index.html".
// Returns an empty fs.FS if Sub fails (shouldn't happen given the embed).
func Assets() fs.FS {
	sub, err := fs.Sub(rawAssets, "build")
	if err != nil {
		return rawAssets
	}
	return sub
}

// HasIndex reports whether build/index.html exists in the embedded assets.
// False when the frontend hasn't been built yet (only the .gitkeep ships).
func HasIndex() bool {
	_, err := fs.Stat(Assets(), "index.html")
	return err == nil
}
