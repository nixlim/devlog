package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlog/internal/testutil"
)

// setupCmdStreams swaps cmd's package-level stdout/stderr for test buffers
// and restores them via t.Cleanup. Returns the two buffers so tests can
// inspect what was written.
func setupCmdStreams(t *testing.T) (stdoutBuf, stderrBuf *bytes.Buffer) {
	t.Helper()
	oldOut, oldErr := stdoutWriter, stderrWriter
	stdoutBuf = &bytes.Buffer{}
	stderrBuf = &bytes.Buffer{}
	stdoutWriter = stdoutBuf
	stderrWriter = stderrBuf
	t.Cleanup(func() {
		stdoutWriter = oldOut
		stderrWriter = oldErr
	})
	return stdoutBuf, stderrBuf
}

// chdir replaces os.Chdir + t.Cleanup. Go 1.24 has t.Chdir but we target
// 1.21. Skipping the old-cwd capture guarantees we always try to restore.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
	})
}

func TestCheckFeedback_MissingFile_Silent(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	chdir(t, root)
	stdoutBuf, stderrBuf := setupCmdStreams(t)

	if code := CheckFeedback(nil); code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdoutBuf.Len() != 0 {
		t.Errorf("stdout should be empty when no feedback, got %q", stdoutBuf.String())
	}
	if stderrBuf.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderrBuf.String())
	}
}

func TestCheckFeedback_EmptyFile_Silent(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	if err := os.WriteFile(filepath.Join(root, ".devlog", "feedback.md"), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	chdir(t, root)
	stdoutBuf, _ := setupCmdStreams(t)

	if code := CheckFeedback(nil); code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdoutBuf.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdoutBuf.String())
	}
}

func TestCheckFeedback_NonEmpty_PrintsArchivesTruncates(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	feedbackPath := filepath.Join(root, ".devlog", "feedback.md")
	archivePath := filepath.Join(root, ".devlog", "feedback_archive.jsonl")
	const payload = "━━━\n[DevLog Companion — Trajectory Assessment]\nSTATUS: DRIFTING\n━━━\n"
	if err := os.WriteFile(feedbackPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	chdir(t, root)
	stdoutBuf, _ := setupCmdStreams(t)

	if code := CheckFeedback(nil); code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}

	// stdout must be exactly the payload (no trailing newline added).
	if got := stdoutBuf.String(); got != payload {
		t.Errorf("stdout: got %q, want %q", got, payload)
	}

	// feedback.md must exist and be empty.
	info, err := os.Stat(feedbackPath)
	if err != nil {
		t.Fatalf("stat feedback.md after run: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("feedback.md size: got %d, want 0", info.Size())
	}

	// Archive must contain one JSONL entry with the payload.
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var entry struct {
		TS      string `json:"ts"`
		Content string `json:"content"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		t.Fatalf("archive empty; raw=%s", data)
	}
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("parse archive line: %v", err)
	}
	if entry.Content != payload {
		t.Errorf("archived content: got %q, want %q", entry.Content, payload)
	}
	if entry.TS == "" {
		t.Errorf("archived ts should not be empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.TS); err != nil {
		t.Errorf("archived ts %q not RFC3339Nano: %v", entry.TS, err)
	}
}

func TestCheckFeedback_HelpFlag(t *testing.T) {
	stdoutBuf, _ := setupCmdStreams(t)
	for _, arg := range []string{"-h", "--help", "help"} {
		stdoutBuf.Reset()
		if code := CheckFeedback([]string{arg}); code != 0 {
			t.Errorf("%q: exit %d, want 0", arg, code)
		}
		if !strings.Contains(stdoutBuf.String(), "Usage:") {
			t.Errorf("%q: help missing Usage section: %q", arg, stdoutBuf.String())
		}
	}
}

func TestCheckFeedback_SubsequentCallHasNothingToDo(t *testing.T) {
	// After one successful consumption, a second invocation with no new
	// feedback must be silent and produce no new archive entry.
	root := testutil.NewTempDevlogDir(t)
	feedbackPath := filepath.Join(root, ".devlog", "feedback.md")
	archivePath := filepath.Join(root, ".devlog", "feedback_archive.jsonl")
	if err := os.WriteFile(feedbackPath, []byte("first round"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	chdir(t, root)
	stdoutBuf, _ := setupCmdStreams(t)

	// First call drains feedback.
	if code := CheckFeedback(nil); code != 0 {
		t.Fatalf("first call exit: %d", code)
	}
	firstArchiveSize := archiveSize(t, archivePath)

	// Second call should be a no-op.
	stdoutBuf.Reset()
	if code := CheckFeedback(nil); code != 0 {
		t.Errorf("second call exit: got %d, want 0", code)
	}
	if stdoutBuf.Len() != 0 {
		t.Errorf("second call stdout should be empty, got %q", stdoutBuf.String())
	}
	if got := archiveSize(t, archivePath); got != firstArchiveSize {
		t.Errorf("archive size changed: %d → %d", firstArchiveSize, got)
	}
}

func TestCheckFeedback_NoDevlogDirectory_Silent(t *testing.T) {
	// Command run from a directory without .devlog/ — must be silent, not
	// scatter errors.log around the filesystem, and not panic.
	root := t.TempDir() // no .git, no .devlog
	chdir(t, root)
	stdoutBuf, stderrBuf := setupCmdStreams(t)

	if code := CheckFeedback(nil); code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdoutBuf.Len() != 0 || stderrBuf.Len() != 0 {
		t.Errorf("should be silent; stdout=%q stderr=%q", stdoutBuf.String(), stderrBuf.String())
	}
}

// archiveSize returns the size in bytes of path, or 0 if missing.
func archiveSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
