package agentcontext

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkBuild_NoWorkspace is the floor: no workspace string, so
// Build returns "" without touching disk. Catches regressions where
// the early-return was lost.
func BenchmarkBuild_NoWorkspace(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Build("")
	}
}

// BenchmarkBuild_EmptyDir measures the dir-walk + canonical-file probe
// cost when a workspace exists but has no recognized content. Each
// iteration does N stat/ReadFile calls (one per canonical file +
// memory dir scan) — close to the talon-side cost of every chat.send
// for a fresh workspace.
func BenchmarkBuild_EmptyDir(b *testing.B) {
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Build(dir)
	}
}

// BenchmarkBuild_TypicalWorkspace simulates a real-world workspace
// with a CLAUDE.md (10 KB) and three memory entries. This is the
// path the user pays per chat.send when the workspace has been used
// for a while.
func BenchmarkBuild_TypicalWorkspace(b *testing.B) {
	dir := b.TempDir()
	writeFile(b, filepath.Join(dir, "CLAUDE.md"), repeat("Project context line.\n", 500))

	memDir := filepath.Join(dir, "memory")
	if err := os.Mkdir(memDir, 0o755); err != nil {
		b.Fatal(err)
	}
	for _, name := range []string{"2026-04-26.md", "2026-04-27.md", "2026-04-28.md"} {
		writeFile(b, filepath.Join(memDir, name), repeat("- something happened\n", 50))
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Build(dir)
	}
}

// BenchmarkBuild_MemoryHeavy: many memory entries, just under the
// budget. Linear in entry count up to the byte budget cap.
func BenchmarkBuild_MemoryHeavy(b *testing.B) {
	dir := b.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.Mkdir(memDir, 0o755); err != nil {
		b.Fatal(err)
	}
	// 50 small memory entries.
	for i := 0; i < 50; i++ {
		fname := filepath.Join(memDir, "entry-"+pad(i)+".md")
		writeFile(b, fname, repeat("- note\n", 20))
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Build(dir)
	}
}

func writeFile(b *testing.B, path, content string) {
	b.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func pad(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
