package buffer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Append serializes e as one JSON line and atomically appends it to the
// buffer file at path. Cross-process mutual exclusion is provided by an
// exclusive flock held on <path>.lock — the same sidecar pattern used
// by internal/state — so concurrent Append and Archive calls never
// interleave or lose writes.
func Append(path string, e Entry) error {
	release, err := lockExclusive(path)
	if err != nil {
		return err
	}
	defer release()

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("buffer: encode entry: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("buffer: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("buffer: append to %s: %w", path, err)
	}
	return nil
}

// ReadAll returns every Entry in the buffer file at path in insertion
// order. A missing file is treated as empty (returns nil, nil). A
// shared lock serialises reads against in-flight Append/Archive writes.
func ReadAll(path string) ([]Entry, error) {
	release, err := lockShared(path)
	if err != nil {
		return nil, err
	}
	defer release()

	return readAllUnlocked(path)
}

// readAllUnlocked is the lock-free body of ReadAll. Archive holds the
// exclusive lock and reuses this helper to avoid reentrant locking.
func readAllUnlocked(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("buffer: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var entries []Entry
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var e Entry
		switch err := dec.Decode(&e); err {
		case nil:
			entries = append(entries, e)
		case io.EOF:
			return entries, nil
		default:
			return entries, fmt.Errorf("buffer: decode %s: %w", path, err)
		}
	}
}

// Archive moves every entry from bufferPath into archivePath and then
// truncates bufferPath.
//
// The operation is serialised against concurrent Append calls via the
// buffer's exclusive flock. It is NOT a crash-atomic transaction — if
// the process dies between the archive append and the buffer truncate,
// the next run will re-archive the same entries. Callers that need
// exact-once archival must add application-level de-duplication. For
// devlog's use this looseness is fine: the archive file is only used as
// contextual input for the companion, never as a source of truth for
// counters.
func Archive(bufferPath, archivePath string) error {
	release, err := lockExclusive(bufferPath)
	if err != nil {
		return err
	}
	defer release()

	data, err := os.ReadFile(bufferPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("buffer: read %s: %w", bufferPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return fmt.Errorf("buffer: ensure archive dir for %s: %w", archivePath, err)
	}

	archF, err := os.OpenFile(archivePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("buffer: open archive %s: %w", archivePath, err)
	}
	if _, err := archF.Write(data); err != nil {
		archF.Close()
		return fmt.Errorf("buffer: append archive %s: %w", archivePath, err)
	}
	if err := archF.Sync(); err != nil {
		archF.Close()
		return fmt.Errorf("buffer: sync archive %s: %w", archivePath, err)
	}
	if err := archF.Close(); err != nil {
		return fmt.Errorf("buffer: close archive %s: %w", archivePath, err)
	}

	if err := os.Truncate(bufferPath, 0); err != nil {
		return fmt.Errorf("buffer: truncate %s: %w", bufferPath, err)
	}
	return nil
}

// Clear truncates the buffer file to zero bytes under the exclusive
// lock. A missing file is treated as success.
func Clear(path string) error {
	release, err := lockExclusive(path)
	if err != nil {
		return err
	}
	defer release()

	if err := os.Truncate(path, 0); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("buffer: truncate %s: %w", path, err)
	}
	return nil
}

func lockExclusive(path string) (func(), error) {
	return lock(path, syscall.LOCK_EX)
}

func lockShared(path string) (func(), error) {
	return lock(path, syscall.LOCK_SH)
}

func lock(path string, mode int) (func(), error) {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("buffer: ensure dir for %s: %w", lockPath, err)
	}
	lockF, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("buffer: open lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lockF.Fd()), mode); err != nil {
		lockF.Close()
		return nil, fmt.Errorf("buffer: flock %s: %w", lockPath, err)
	}
	return func() {
		syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN)
		lockF.Close()
	}, nil
}
