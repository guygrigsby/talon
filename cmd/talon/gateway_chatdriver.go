package main

import (
	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/audit"
	"github.com/guygrigsby/talon/internal/chatdriver"
	"github.com/guygrigsby/talon/internal/server"
	"github.com/guygrigsby/talon/internal/talonpath"
)

func buildChatRunner(paths talonpath.Paths, mem *server.MemoryConfig, rec audit.Recorder) server.ChatRunFn {
	// ADR 0013: Claude-memory resolver runs per-turn so the index
	// reflects the live MEMORY.md (no restart needed after edits).
	claudeMem := chatdriver.ClaudeMemoryResolver(func() (string, tool.Tool, bool) {
		return buildClaudeMemory(paths)
	})
	var opts []chatdriver.RunnerOption
	// ADR 0017 Phase 5: record tool_gate verdicts to the same audit recorder.
	if rec != nil {
		opts = append(opts, chatdriver.WithAuditRecorder(rec))
	}
	return chatdriver.NewChatRunner(paths, mem, claudeMem, opts...)
}
