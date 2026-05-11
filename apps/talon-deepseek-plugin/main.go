// talon-deepseek-plugin is a standalone binary wrapper for the DeepSeek
// plugin. The gateway can also run this plugin via 'talon plugin run
// deepseek' without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-deepseek-plugin ./apps/talon-deepseek-plugin
package main

import (
	"fmt"
	"log/slog"
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	deepseekplug "github.com/guygrigsby/talon/internal/plugins/deepseek"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	log := slog.With("plugin", "deepseek")

	srv, err := deepseekplug.New()
	if err != nil {
		log.Error("init failed", "err", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pluginrun.Serve("deepseek", srv)
}
