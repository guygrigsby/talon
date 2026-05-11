// talon-mac-notify-plugin is a standalone binary wrapper for the macOS
// Notification Center plugin. The gateway can also run this via 'talon
// plugin run mac-notify' without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-mac-notify-plugin ./apps/talon-mac-notify-plugin
package main

import (
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	"github.com/guygrigsby/talon/internal/plugins/macnotify"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	srv, _ := macnotify.New()
	pluginrun.Serve("mac-notify", srv)
}
