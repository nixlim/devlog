package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withStdinFile writes stdinJSON to a temp file and arranges for
// TaskCapture to read from it. Cleanup is registered via t.Cleanup.
func withStdinFile(t *testing.T, stdinJSON string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin-*.json")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := f.WriteString(stdinJSON); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdin: %v", err)
	}
	prev := taskCaptureStdin
	taskCaptureStdin = func() *os.File { return f }
	t.Cleanup(func() {
		taskCaptureStdin = prev
		_ = f.Close()
	})
}

// withStreams swaps the package stdout/stderr sinks for buffers that the
// test can inspect. Returned cleanup restores the originals.
func withStreams(t *testing.T) (stdoutBuf, stderrBuf *bytes.Buffer) {
	t.Helper()
	stdoutBuf = &bytes.Buffer{}
	stderrBuf = &bytes.Buffer{}
	prevO, prevE := stdoutWriter, stderrWriter
	stdoutWriter = stdoutBuf
	stderrWriter = stderrBuf
	t.Cleanup(func() {
		stdoutWriter = prevO
		stderrWriter = prevE
	})
	return stdoutBuf, stderrBuf
}

// makePayload marshals a UserPromptSubmit hook payload with the given
// cwd/prompt/session fields.
func makePayload(t *testing.T, cwd, prompt, sessionID string) string {
	t.Helper()
	payload := map[string]any{
		"session_id":      sessionID,
		"transcript_path": "/tmp/fake.jsonl",
		"cwd":             cwd,
		"prompt":          prompt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(data)
}

func TestTaskCaptureFirstPromptWritesTaskMD(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}

	const prompt = "Fix the 500 error on /api/recommendations"
	withStdinFile(t, makePayload(t, root, prompt, "sess-abc"))
	stdout, stderr := withStreams(t)

	code := TaskCapture(nil)
	if code != 0 {
		t.Errorf("TaskCapture exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be silent on success: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be silent on success: %q", stdout.String())
	}

	data, err := os.ReadFile(filepath.Join(root, ".devlog", "task.md"))
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if !strings.HasPrefix(string(data), prompt) {
		t.Errorf("task.md = %q, want prefix %q", string(data), prompt)
	}

	// task_updates.jsonl should NOT exist on first invocation.
	if _, err := os.Stat(filepath.Join(root, ".devlog", "task_updates.jsonl")); !os.IsNotExist(err) {
		t.Errorf("task_updates.jsonl should not exist after first prompt (got err=%v)", err)
	}
}

func TestTaskCaptureSubsequentPromptAppendsToUpdates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}
	// Seed an existing task.md so this prompt is treated as a course
	// correction rather than the original task.
	taskPath := filepath.Join(root, ".devlog", "task.md")
	if err := os.WriteFile(taskPath, []byte("original task\n"), 0o644); err != nil {
		t.Fatalf("seed task.md: %v", err)
	}

	const prompt = "Actually, try the caching layer first"
	withStdinFile(t, makePayload(t, root, prompt, "sess-abc"))
	withStreams(t)

	code := TaskCapture(nil)
	if code != 0 {
		t.Errorf("TaskCapture exit code = %d, want 0", code)
	}

	// task.md should still contain only the original content.
	taskData, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if string(taskData) != "original task\n" {
		t.Errorf("task.md was overwritten: %q", string(taskData))
	}

	// task_updates.jsonl should have exactly one line with the new prompt.
	updatesPath := filepath.Join(root, ".devlog", "task_updates.jsonl")
	updatesData, err := os.ReadFile(updatesPath)
	if err != nil {
		t.Fatalf("read task_updates.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(updatesData), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 update line, got %d", len(lines))
	}
	var entry taskUpdateEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode update entry: %v", err)
	}
	if entry.Prompt != prompt {
		t.Errorf("entry.Prompt = %q, want %q", entry.Prompt, prompt)
	}
	if entry.SessionID != "sess-abc" {
		t.Errorf("entry.SessionID = %q", entry.SessionID)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.TS); err != nil {
		t.Errorf("entry.TS not RFC3339Nano: %q (%v)", entry.TS, err)
	}
}

func TestTaskCaptureMultipleUpdatesAppendOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".devlog", "task.md"),
		[]byte("seeded\n"), 0o644); err != nil {
		t.Fatalf("seed task.md: %v", err)
	}

	prompts := []string{"first update", "second update", "third update"}
	for i, p := range prompts {
		withStdinFile(t, makePayload(t, root, p, fmt.Sprintf("sess-%d", i)))
		withStreams(t)
		if code := TaskCapture(nil); code != 0 {
			t.Fatalf("iteration %d: exit = %d", i, code)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, ".devlog", "task_updates.jsonl"))
	if err != nil {
		t.Fatalf("read updates: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != len(prompts) {
		t.Fatalf("expected %d update lines, got %d", len(prompts), len(lines))
	}
	for i, line := range lines {
		var e taskUpdateEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if e.Prompt != prompts[i] {
			t.Errorf("line %d: prompt = %q, want %q", i, e.Prompt, prompts[i])
		}
	}
}

func TestTaskCaptureCreatesDevlogDirIfMissing(t *testing.T) {
	root := t.TempDir()
	// Intentionally do NOT create .devlog/ — the hook should recover.

	withStdinFile(t, makePayload(t, root, "hello", "sess-abc"))
	withStreams(t)

	code := TaskCapture(nil)
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".devlog", "task.md")); err != nil {
		t.Errorf("task.md should exist: %v", err)
	}
}

func TestTaskCaptureEmptyStdinExitsZero(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	withStdinFile(t, "")
	withStreams(t)
	if code := TaskCapture(nil); code != 0 {
		t.Errorf("empty stdin should still exit 0, got %d", code)
	}
	// Error should be logged to .devlog/errors.log (directory may not exist
	// — log-write is best-effort and a missing dir is OK).
}

func TestTaskCaptureEmptyPromptLogsError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}
	withStdinFile(t, makePayload(t, root, "", "sess-abc"))
	withStreams(t)

	if code := TaskCapture(nil); code != 0 {
		t.Errorf("empty prompt should still exit 0, got %d", code)
	}
	// task.md should NOT have been created.
	if _, err := os.Stat(filepath.Join(root, ".devlog", "task.md")); !os.IsNotExist(err) {
		t.Errorf("task.md should not exist for empty prompt: %v", err)
	}
	// An error line should have been logged.
	logData, err := os.ReadFile(filepath.Join(root, ".devlog", "errors.log"))
	if err != nil {
		t.Fatalf("read errors.log: %v", err)
	}
	if !strings.Contains(string(logData), "task-capture") {
		t.Errorf("errors.log missing task-capture entry: %s", string(logData))
	}
}

func TestTaskCaptureZeroLengthTaskFileTreatedAsMissing(t *testing.T) {
	// A pre-existing zero-byte task.md should be overwritten rather than
	// quietly treated as "the original task" and redirecting the first real
	// prompt into task_updates.jsonl.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	taskPath := filepath.Join(root, ".devlog", "task.md")
	if err := os.WriteFile(taskPath, nil, 0o644); err != nil {
		t.Fatalf("create empty task.md: %v", err)
	}

	const prompt = "real task here"
	withStdinFile(t, makePayload(t, root, prompt, "sess-abc"))
	withStreams(t)
	if code := TaskCapture(nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if !strings.Contains(string(data), prompt) {
		t.Errorf("task.md should have been populated with %q, got %q", prompt, string(data))
	}
	// updates file should not exist.
	if _, err := os.Stat(filepath.Join(root, ".devlog", "task_updates.jsonl")); !os.IsNotExist(err) {
		t.Errorf("task_updates.jsonl should not exist: %v", err)
	}
}

func TestTaskCaptureMalformedJSONExitsZero(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	withStdinFile(t, "{not valid json")
	withStreams(t)
	if code := TaskCapture(nil); code != 0 {
		t.Errorf("malformed JSON should still exit 0, got %d", code)
	}
	// Should have logged an error to the fallback (cwd-rooted) errors.log.
	logPath := filepath.Join(root, ".devlog", "errors.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("errors.log should be written on malformed input: %v", err)
	}
}
