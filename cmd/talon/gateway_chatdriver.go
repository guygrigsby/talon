package main

import (
	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildChatRunner(paths talonpath.Paths, mem *server.MemoryConfig) server.ChatRunFn {
	// ADR 0013: Claude-memory resolver runs per-turn so the index
	// reflects the live MEMORY.md (no restart needed after edits).
	claudeMem := chatdriver.ClaudeMemoryResolver(func() (string, tool.Tool, bool) {
		return buildClaudeMemory(paths)
	})
	return chatdriver.NewChatRunner(paths, mem, claudeMem)
}
