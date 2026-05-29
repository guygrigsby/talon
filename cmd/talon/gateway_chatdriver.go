package main

import (
	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildChatRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.ChatRunFn {
	return chatdriver.NewChatRunner(paths, mem)
}
