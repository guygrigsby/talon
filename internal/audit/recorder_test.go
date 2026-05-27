package audit

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLRecorder_WritesRedactedJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-audit.jsonl")
	r, err := NewJSONLRecorder(Options{Path: path, MaxSizeMB: 10, Keep: 3})
	if err != nil {
		t.Fatal(err)
	}
	r.Record(Event{Kind: KindToolCall, Session: "s", Run: "r", Seq: 1, Tool: "bash",
		Args: `{"cmd":"x","token":"sk-secret-123"}`})
	if err := r.Close(); err != nil { // Close flushes
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("sk-secret-123")) {
		t.Fatal("secret leaked into audit log")
	}
	if !bytes.Contains(b, []byte(`"tool":"bash"`)) {
		t.Fatalf("record not written: %s", b)
	}
}

func TestJSONLRecorder_RotatesAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-audit.jsonl")
	// 1 MB cap; each record carries a ~4KB Output so a few dozen
	// records cross the threshold and force a rotation.
	r, err := NewJSONLRecorder(Options{Path: path, MaxSizeMB: 1, Keep: 3, MaxField: 8192})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", 4096)
	for i := 0; i < 400; i++ {
		r.Record(Event{Kind: KindToolResult, Session: "s", Run: "r", Seq: int64(i), Tool: "bash", Output: big})
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated file %s.1 to exist: %v", path, err)
	}
}

func TestJSONLRecorder_NeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-audit.jsonl")
	r, err := NewJSONLRecorder(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}

	leaks := []string{
		"op://Personal/openai/api-key-VALUE",
		"keychain://talon-gateway-token-VALUE",
		"sk-proj-ABCDEF0123456789abcdef0123456789",
		"sk-ant-api03-ZYXWVUT9876543210ZYXWVUT9876543210",
		"Bearer eyJhbGciOiJIUzI1Ni012345.payloadpart.signaturepart",
	}

	// JSON args (RedactJSON handles these by sensitive key).
	r.Record(Event{Kind: KindToolCall, Session: "s", Run: "r", Seq: 1, Tool: "bash",
		Args: `{"cmd":"login","token":"` + leaks[2] + `","apiKey":"` + leaks[2] + `"}`})
	// Non-JSON output + text carrying literal secret references.
	r.Record(Event{Kind: KindToolResult, Session: "s", Run: "r", Seq: 2, Tool: "bash",
		Output: "resolved " + strings.Join(leaks, " and ") + " ok"})
	r.Record(Event{Kind: KindMessage, Session: "s", Run: "r", Seq: 3,
		Text: "using " + strings.Join(leaks, " | ")})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	for _, leak := range leaks {
		if bytes.Contains(b, []byte(leak)) {
			t.Fatalf("secret leaked into audit log: %q\n%s", leak, b)
		}
	}
	// Sanity: the records were actually written.
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("expected 3 records written, got %d:\n%s", n, b)
	}
}
