package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlog/internal/state"
)

// withResetStdin injects in as the reader Reset will consume.
func withResetStdin(t *testing.T, in io.Reader) {
	t.Helper()
	prev := resetStdin
	resetStdin = in
	t.Cleanup(func() { resetStdin = prev })
}

// seedDevlogDir populates .devlog/ with non-empty copies of every file
// Reset is supposed to clear, plus a seeded state.json with non-zero
// counters. Returns the .devlog/ path.
func seedDevlogDir(t *testing.T, root string) string {
	t.Helper()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range resetFiles {
		if err := os.WriteFile(filepath.Join(devlogDir, name),
			[]byte("seed "+name+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	s := &state.State{
		SessionID:           "sess-keep-me",
		StartedAt:           "2026-04-22T22:00:00Z",
		BufferCount:         7,
		BufferSeq:           42,
		LogCount:            3,
		LogSeq:              3,
		LogSinceCompanion:   3,
		FlushInProgress:     true,
		CompanionInProgress: true,
		LastCompanion: &state.LastCompanion{
			TS:         "2026-04-22T22:10:00Z",
			Status:     "drifting",
			Confidence: 0.7,
		},
	}
	if err := state.Save(filepath.Join(devlogDir, "state.json"), s); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return devlogDir
}

func TestResetWithYesFlagSkipsPrompt(t *testing.T) {
	root := t.TempDir()
	devlogDir := seedDevlogDir(t, root)

	withStreams(t)
	withResetStdin(t, strings.NewReader("")) // empty; must not be consumed
	code := Reset([]string{"--yes", "--project", root})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	// Every target file should now be zero bytes (but still exist).
	for _, name := range resetFiles {
		info, err := os.Stat(filepath.Join(devlogDir, name))
		if err != nil {
			t.Errorf("%s missing after reset: %v", name, err)
			continue
		}
		if info.Size() != 0 {
			t.Errorf("%s not truncated: size=%d", name, info.Size())
		}
	}

	// Counters reset, identity preserved.
	s, err := state.Load(filepath.Join(devlogDir, "state.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.SessionID != "sess-keep-me" {
		t.Errorf("SessionID clobbered: %q", s.SessionID)
	}
	if s.StartedAt != "2026-04-22T22:00:00Z" {
		t.Errorf("StartedAt clobbered: %q", s.StartedAt)
	}
	if s.BufferCount != 0 || s.BufferSeq != 0 || s.LogCount != 0 ||
		s.LogSeq != 0 || s.LogSinceCompanion != 0 {
		t.Errorf("counters not reset: %+v", s)
	}
	if s.FlushInProgress || s.CompanionInProgress {
		t.Errorf("in-progress flags not cleared: %+v", s)
	}
}

func TestResetKeepLogPreservesLogFile(t *testing.T) {
	root := t.TempDir()
	devlogDir := seedDevlogDir(t, root)
	logPath := filepath.Join(devlogDir, "log.jsonl")

	withStreams(t)
	withResetStdin(t, strings.NewReader(""))
	if code := Reset([]string{"--yes", "--keep-log", "--project", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log.jsonl missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("log.jsonl should be preserved under --keep-log")
	}

	// Other files still cleared.
	bufPath := filepath.Join(devlogDir, "buffer.jsonl")
	if info, _ := os.Stat(bufPath); info == nil || info.Size() != 0 {
		t.Errorf("buffer.jsonl should still be truncated under --keep-log")
	}
}

func TestResetPromptAcceptsYes(t *testing.T) {
	root := t.TempDir()
	devlogDir := seedDevlogDir(t, root)

	stdout, _ := withStreams(t)
	withResetStdin(t, strings.NewReader("y\n"))
	if code := Reset([]string{"--project", root}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	// The prompt must have been shown.
	if !strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("confirmation prompt missing in stdout: %q", stdout.String())
	}
	info, _ := os.Stat(filepath.Join(devlogDir, "buffer.jsonl"))
	if info == nil || info.Size() != 0 {
		t.Error("buffer.jsonl should be truncated after 'y' confirmation")
	}
}

func TestResetPromptRejectsDefault(t *testing.T) {
	root := t.TempDir()
	devlogDir := seedDevlogDir(t, root)
	bufPath := filepath.Join(devlogDir, "buffer.jsonl")
	originalSize := int64(len("seed buffer.jsonl\n"))

	stdout, _ := withStreams(t)
	// Just a newline — operator pressed Enter on the default prompt.
	withResetStdin(t, strings.NewReader("\n"))
	code := Reset([]string{"--project", root})
	if code == 0 {
		t.Error("Reset should exit non-zero when user declines")
	}
	if !strings.Contains(stdout.String(), "aborted") {
		t.Errorf("expected 'aborted' confirmation in stdout: %q", stdout.String())
	}

	// Seed data must still be intact.
	info, err := os.Stat(bufPath)
	if err != nil {
		t.Fatalf("buffer.jsonl missing: %v", err)
	}
	if info.Size() != originalSize {
		t.Errorf("buffer.jsonl mutated: size=%d want=%d", info.Size(), originalSize)
	}
}

func TestResetPromptRejectsExplicitNo(t *testing.T) {
	root := t.TempDir()
	seedDevlogDir(t, root)

	withStreams(t)
	withResetStdin(t, strings.NewReader("N\n"))
	if code := Reset([]string{"--project", root}); code == 0 {
		t.Error("Reset should exit non-zero on 'N'")
	}
}

func TestResetMissingDevlogDirFails(t *testing.T) {
	root := t.TempDir()
	// No .devlog/ directory.
	_, stderr := withStreams(t)
	withResetStdin(t, strings.NewReader(""))
	code := Reset([]string{"--yes", "--project", root})
	if code == 0 {
		t.Error("Reset should fail when .devlog/ does not exist")
	}
	if !strings.Contains(stderr.String(), "no .devlog") {
		t.Errorf("stderr should mention missing .devlog: %q", stderr.String())
	}
}

func TestResetSkipsAbsentFilesGracefully(t *testing.T) {
	// Only create one of the reset targets — the rest should be treated
	// as already-clean, not as errors.
	root := t.TempDir()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devlogDir, "buffer.jsonl"),
		[]byte("content\n"), 0o644); err != nil {
		t.Fatalf("seed buffer: %v", err)
	}

	withStreams(t)
	withResetStdin(t, strings.NewReader(""))
	if code := Reset([]string{"--yes", "--project", root}); code != 0 {
		t.Errorf("Reset should succeed when only some files exist, got %d", code)
	}
}

func TestResetHandlesMissingStateFile(t *testing.T) {
	// .devlog/ exists but state.json is absent — reset shouldn't fail.
	root := t.TempDir()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devlogDir, "task.md"),
		[]byte("a task\n"), 0o644); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	withStreams(t)
	withResetStdin(t, bytes.NewReader(nil))
	if code := Reset([]string{"--yes", "--project", root}); code != 0 {
		t.Errorf("Reset should tolerate missing state.json, got %d", code)
	}
}

func TestResetUnknownFlagReturnsTwo(t *testing.T) {
	withStreams(t)
	if code := Reset([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag should return 2, got %d", code)
	}
}
