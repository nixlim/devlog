// Package state manages the .devlog/state.json session file.
//
// The file holds counters (buffer_count, log_count, etc.), in-progress
// flags, and the last companion verdict. It is read and written by every
// hook invocation — sometimes concurrently, because the capture hook can
// spawn a background `devlog flush` while the next PostToolUse hook is
// already running. Serialisation is therefore critical.
//
// Reads go through Load, writes through Save (atomic temp-file + rename).
// Read-modify-write sequences MUST go through Update, which takes a
// cross-process exclusive lock on a sidecar .lock file. A sidecar is used
// rather than locking state.json itself because os.Rename replaces the
// inode — any flock held on the old inode would be orphaned, breaking
// mutual exclusion for the next writer.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LastCompanion is the most recent companion verdict embedded in state.json.
type LastCompanion struct {
	TS         string  `json:"ts"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
}

// State mirrors the on-disk .devlog/state.json schema from SPEC.md.
type State struct {
	SessionID           string         `json:"session_id"`
	StartedAt           string         `json:"started_at"`
	BufferCount         int            `json:"buffer_count"`
	BufferSeq           int            `json:"buffer_seq"`
	LogCount            int            `json:"log_count"`
	LogSeq              int            `json:"log_seq"`
	LogSinceCompanion   int            `json:"log_since_companion"`
	LastCompanion       *LastCompanion `json:"last_companion,omitempty"`
	FlushInProgress     bool           `json:"flush_in_progress"`
	CompanionInProgress bool           `json:"companion_in_progress"`
}

// Load reads and decodes the state file at path. A missing file is
// surfaced as an os.ErrNotExist error so callers can distinguish
// "uninitialised" from "corrupt".
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state: decode %s: %w", path, err)
	}
	return &s, nil
}

// Save writes s to path atomically: it marshals pretty-printed JSON into
// a temp file in the same directory, fsyncs it, and renames over the
// target. A crash at any point leaves either the old file intact or the
// new one fully written — never a truncated mix.
func Save(path string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("state: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("state: rename %s -> %s: %w", tmpPath, path, err)
	}
	committed = true
	return nil
}

// Update performs an atomic read-modify-write on the state file. It
// acquires an exclusive cross-process flock on <path>.lock, loads the
// current state (starting from a zero State if the file does not yet
// exist), invokes fn, and saves the result. The lock is released when
// the function returns.
//
// fn MUST NOT spawn goroutines that outlive the call — the lock guards
// only the duration of fn. Mutations made after Update returns need a
// fresh Update.
func Update(path string, fn func(*State) error) error {
	if fn == nil {
		return fmt.Errorf("state: Update requires a non-nil fn")
	}

	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("state: ensure dir for %s: %w", lockPath, err)
	}
	lockF, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("state: open lock %s: %w", lockPath, err)
	}
	defer lockF.Close()

	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("state: flock %s: %w", lockPath, err)
	}
	defer syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN)

	var s State
	existing, err := Load(path)
	switch {
	case err == nil:
		s = *existing
	case os.IsNotExist(err):
		// First Update call — start from zero state.
	default:
		return err
	}

	if err := fn(&s); err != nil {
		return err
	}
	return Save(path, &s)
}
