// talon-telegram-plugin is a standalone binary wrapper for the Telegram
// channel plugin. The gateway can also run this via 'talon plugin run
// telegram' without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-telegram-plugin ./apps/talon-telegram-plugin
package main

import (
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	"github.com/guygrigsby/talon/internal/plugins/telegram"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	srv, _ := telegram.New()
	pluginrun.Serve("telegram", srv)
}
