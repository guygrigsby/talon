package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Store persists the job set to ~/.talon/cron/jobs.json. One file
// holds all jobs; reads/writes are atomic via tmp+rename so a crash
// mid-save can never leave a half-written jobs.json.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store backed by path. The parent directory is
// created on first Save; Load on a missing file returns an empty
// slice without error.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path is exposed for callers that need to reason about the on-disk
// location (e.g. diagnostics export).
func (s *Store) Path() string { return s.path }

type jobsFile struct {
	Jobs []Job `json:"jobs"`
}

// Load returns the persisted job set. A missing file returns nil, nil
// — that's the normal first-run case, distinct from a parse error.
func (s *Store) Load() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f jobsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return f.Jobs, nil
}

// Save writes the job set atomically. mkdir is best-effort; the
// WriteFile call surfaces the real error if the directory can't be
// created (permission, disk full, etc.).
func (s *Store) Save(jobs []Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(jobsFile{Jobs: jobs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RunLog persists run history to a JSONL file. Append-only so a
// concurrent reader sees a consistent prefix at any moment.
type RunLog struct {
	path string
	mu   sync.Mutex
}

// NewRunLog returns a RunLog backed by path. The parent directory is
// created on first Append.
func NewRunLog(path string) *RunLog {
	return &RunLog{path: path}
}

// Path returns the on-disk location for diagnostics.
func (r *RunLog) Path() string { return r.path }

// Append writes one Run as a JSON line. Returns the underlying file
// error verbatim so caller logging captures the real cause.
func (r *RunLog) Append(run Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := json.Marshal(run)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(body, '\n')); err != nil {
		return err
	}
	return nil
}

// Read returns up to limit most-recent runs, newest first. jobID
// non-empty filters to that job; empty returns runs across all jobs.
// Implementation reads the entire file each call — fine for the
// expected scale (single-digit jobs, hundreds of runs/day at worst).
func (r *RunLog) Read(jobID string, limit int) ([]Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	all, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	out := []Run{}
	dec := json.NewDecoder(newLineSplit(all))
	for dec.More() {
		var run Run
		if err := dec.Decode(&run); err != nil {
			// Skip malformed lines rather than fail the whole read —
			// a corrupted line shouldn't poison every subsequent query.
			continue
		}
		if jobID != "" && run.JobID != jobID {
			continue
		}
		out = append(out, run)
	}
	// Newest first; reverse the natural append order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Latest returns the newest run for jobID, or an error when there is
// no recorded run for it. Used by Service.RunNow to echo the freshly-
// appended run record back to the RPC caller.
func (r *RunLog) Latest(jobID string) (Run, error) {
	runs, err := r.Read(jobID, 1)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, errors.New("cron: no runs for job")
	}
	return runs[0], nil
}

// newLineSplit returns an io.Reader that yields the raw bytes; the
// json.Decoder handles line boundaries on its own when streamed
// through Decode-loop. We just need a reader handle on the byte
// slice.
func newLineSplit(b []byte) *byteReader {
	return &byteReader{b: b}
}

type byteReader struct {
	b []byte
	i int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// randHex4 returns 4 random hex chars (16 bits) for run-id
// disambiguation. Cryptographic randomness is overkill for a
// disambiguator but `crypto/rand` is the path that always works
// under the project's lint rules; falls back to a fixed string on
// the impossible read failure.
func randHex4() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}
