package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guygrigsby/talon/internal/talonconfig"
	"github.com/guygrigsby/talon/internal/talonpath"
	"github.com/tidwall/gjson"
)

// configBackupCount controls the .bak rotation depth on the Talon config.
const configBackupCount = 5

// writeOverlay performs:
//  1. Rotate <Config>.bak.N → .bak.N+1, .bak → .bak.1, drop .bak.4.
//  2. Write the new contents atomically (temp + rename).
//  3. Copy the new contents to <Config>.bak (so .bak always == latest).
//  4. Append a JSONL audit record to <Dir>/logs/config-audit.jsonl.
//
// previous and next are the overlay byte streams BEFORE any pretty-printing
// difference, used to compute hashes and gateway-mode-change for the audit
// record.
func writeOverlay(layer talonpath.Layer, previous, next []byte, operations []string) error {
	if err := os.MkdirAll(filepath.Dir(layer.Config), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := rotateBackups(layer); err != nil {
		return fmt.Errorf("rotate backups: %w", err)
	}
	if err := writeFile(layer.Config, next, 0o600); err != nil {
		return err
	}
	if err := writeFile(layer.ConfigBackupPath(0), next, 0o600); err != nil {
		// best-effort; write succeeded so don't fail the operation
		_ = err
	}
	if err := appendAudit(layer, auditRecord(layer, previous, next, operations, "rename")); err != nil {
		// best-effort; the write is the primary contract
		_ = err
	}
	return nil
}

// writeNativeFromRuntimeJSON converts the gateway's runtime JSON view back
// into Talon's native TOML before writing it to disk.
func writeNativeFromRuntimeJSON(layer talonpath.Layer, previousRuntime, nextRuntime []byte, operations []string) error {
	nextCfg, err := talonconfig.FromRuntimeJSON(nextRuntime)
	if err != nil {
		return fmt.Errorf("convert config to native TOML: %w", err)
	}
	next := talonconfig.MarshalTOML(nextCfg, talonconfig.MarshalOptions{})
	return writeOverlay(layer, previousRuntime, next, operations)
}

// rotateBackups performs a 5-deep rotation of <Config>.bak[.N]. Best-effort —
// missing siblings are ignored. Anything that fails permission-wise is left
// alone; we never want a backup-rotation failure to block the primary write.
func rotateBackups(layer talonpath.Layer) error {
	// drop .bak.4 (the oldest)
	_ = os.Remove(layer.ConfigBackupPath(configBackupCount - 1))
	// shift .bak.N → .bak.N+1, descending so we never collide
	for i := configBackupCount - 2; i >= 1; i-- {
		from := layer.ConfigBackupPath(i)
		to := layer.ConfigBackupPath(i + 1)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		_ = os.Rename(from, to)
	}
	// .bak → .bak.1
	if _, err := os.Stat(layer.ConfigBackupPath(0)); err == nil {
		_ = os.Rename(layer.ConfigBackupPath(0), layer.ConfigBackupPath(1))
	}
	return nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".talon-write-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// AuditRecord is the shape of a single JSONL line in the talon config audit
// log.
type AuditRecord struct {
	Ts                string   `json:"ts"`
	Source            string   `json:"source"`
	Event             string   `json:"event"`
	Result            string   `json:"result"`
	ConfigPath        string   `json:"configPath"`
	Pid               int      `json:"pid"`
	Ppid              int      `json:"ppid"`
	Cwd               string   `json:"cwd,omitempty"`
	Argv              []string `json:"argv,omitempty"`
	Operations        []string `json:"operations,omitempty"`
	PreviousHash      string   `json:"previousHash,omitempty"`
	NextHash          string   `json:"nextHash,omitempty"`
	PreviousBytes     int      `json:"previousBytes,omitempty"`
	NextBytes         int      `json:"nextBytes,omitempty"`
	GatewayModeBefore string   `json:"gatewayModeBefore,omitempty"`
	GatewayModeAfter  string   `json:"gatewayModeAfter,omitempty"`
}

func auditRecord(layer talonpath.Layer, previous, next []byte, operations []string, result string) AuditRecord {
	cwd, _ := os.Getwd()
	return AuditRecord{
		Ts:                time.Now().UTC().Format(time.RFC3339Nano),
		Source:            "talon-config-io",
		Event:             "config.write",
		Result:            result,
		ConfigPath:        layer.Config,
		Pid:               os.Getpid(),
		Ppid:              os.Getppid(),
		Cwd:               cwd,
		Argv:              os.Args,
		Operations:        operations,
		PreviousHash:      hashOrEmpty(previous),
		NextHash:          hashOrEmpty(next),
		PreviousBytes:     len(previous),
		NextBytes:         len(next),
		GatewayModeBefore: jsonString(previous, "gateway.auth.mode"),
		GatewayModeAfter:  jsonString(next, "gateway.auth.mode"),
	}
}

func appendAudit(layer talonpath.Layer, rec AuditRecord) error {
	if err := os.MkdirAll(layer.LogsDir(), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(layer.ConfigAuditLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return err
	}
	return nil
}

func hashOrEmpty(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func jsonString(raw []byte, path string) string {
	if !gjson.ValidBytes(raw) {
		return ""
	}
	r := gjson.GetBytes(raw, path)
	if !r.Exists() {
		return ""
	}
	return r.String()
}

// IsAuditLogReadable returns nil if the audit log exists and is readable.
// Used by callers that want to surface an "audit not initialized" hint.
func IsAuditLogReadable(layer talonpath.Layer) error {
	f, err := os.Open(layer.ConfigAuditLogPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("audit log not yet created: %s", layer.ConfigAuditLogPath())
		}
		return err
	}
	defer f.Close()
	if _, err := io.ReadAll(f); err != nil {
		return err
	}
	return nil
}

// formatPathForLog trims home prefixes and escapes "/" → "/" so audit lines
// stay readable when grep'd. Reserved for future use; not called yet.
func formatPathForLog(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

var _ = formatPathForLog // reserved
