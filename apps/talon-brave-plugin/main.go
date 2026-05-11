// talon-brave-plugin is a standalone binary wrapper for the Brave Search
// plugin. The gateway can also run this via 'talon plugin run brave'
// without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-brave-plugin ./apps/talon-brave-plugin
package main

import (
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	"github.com/guygrigsby/talon/internal/plugins/brave"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	srv, _ := brave.New()
	pluginrun.Serve("brave", srv)
}
