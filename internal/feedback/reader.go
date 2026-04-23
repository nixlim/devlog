package feedback

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Read returns the current contents of the feedback file at path.
//
// A missing file or a zero-byte file both return ("", nil) — the PreToolUse
// hook uses this result to short-circuit when there is no pending feedback,
// and erroring on "not exist" would bury the common case in noise.
func Read(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("feedback: open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("feedback: stat: %w", err)
	}
	if info.Size() == 0 {
		return "", nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("feedback: read: %w", err)
	}
	return string(data), nil
}

// archiveEntry is one line in feedback_archive.jsonl. Kept private so the
// on-disk shape can evolve without leaking into the feedback package API.
type archiveEntry struct {
	TS      string `json:"ts"`
	Content string `json:"content"`
}

// Truncate archives the current contents of path (as a JSONL entry in
// archivePath) and then empties path. Returns the content that was
// archived, which the PreToolUse hook writes to stdout so Claude Code can
// inject it into the agent's context.
//
// If path is missing or empty, returns ("", nil) with no side effects —
// this is the zero-feedback fast path.
//
// If appending to the archive fails, path is left untouched. That way a
// transient archive error results in the same feedback being re-injected
// on the next hook invocation instead of being silently lost.
func Truncate(path, archivePath string) (string, error) {
	content, err := Read(path)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", nil
	}

	entry := archiveEntry{
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Content: content,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("feedback: marshal archive: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(archivePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("feedback: open archive: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return "", fmt.Errorf("feedback: write archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("feedback: close archive: %w", err)
	}

	if err := os.Truncate(path, 0); err != nil {
		return "", fmt.Errorf("feedback: truncate: %w", err)
	}
	return content, nil
}
