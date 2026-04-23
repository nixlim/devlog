package feedback

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write atomically writes content to path. The parent directory must exist.
// An existing file at path is replaced.
//
// Atomicity matters because the PreToolUse hook reads feedback.md on every
// tool call. If the companion writes non-atomically while the hook is
// reading, the agent could see a partial banner. We write to a temp file
// in the same directory (so rename is a metadata op on the same filesystem)
// and then os.Rename, which is atomic on POSIX.
func Write(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".feedback.*.tmp")
	if err != nil {
		return fmt.Errorf("feedback: create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("feedback: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("feedback: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("feedback: rename temp: %w", err)
	}
	return nil
}
