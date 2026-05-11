// talon-bluebubbles-plugin is a standalone binary wrapper for the
// BlueBubbles iMessage channel plugin. The gateway can also run this
// via 'talon plugin run bluebubbles' without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-bluebubbles-plugin ./apps/talon-bluebubbles-plugin
package main

import (
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	"github.com/guygrigsby/talon/internal/plugins/bluebubbles"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	srv, _ := bluebubbles.New()
	pluginrun.Serve("bluebubbles", srv)
}
