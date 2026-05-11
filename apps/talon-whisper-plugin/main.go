// talon-whisper-plugin is a standalone binary wrapper for the Whisper
// transcription plugin. The gateway can also run this via 'talon plugin
// run whisper' without a separate binary install.
//
// Build:
//
//	go build -o bin/talon-whisper-plugin ./apps/talon-whisper-plugin
package main

import (
	"os"

	talonlog "github.com/guygrigsby/talon/internal/log"
	"github.com/guygrigsby/talon/internal/pluginrun"
	"github.com/guygrigsby/talon/internal/plugins/whisper"
)

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	srv, _ := whisper.New()
	pluginrun.Serve("whisper", srv)
}
