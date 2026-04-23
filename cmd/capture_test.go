package cmd

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/state"
	"devlog/internal/testutil"
)

// withCaptureStdin pipes stdinJSON through cmd.captureStdin for the
// duration of the test.
func withCaptureStdin(t *testing.T, stdinJSON string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "capture-stdin-*.json")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := f.WriteString(stdinJSON); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdin: %v", err)
	}
	prev := captureStdin
	captureStdin = func() *os.File { return f }
	t.Cleanup(func() {
		captureStdin = prev
		_ = f.Close()
	})
}

// noopFlushSpawner replaces the background flush spawner so tests don't
// actually fork/exec processes. The test can assert the call count via
// the returned closure.
func noopFlushSpawner(t *testing.T) (*int, func()) {
	t.Helper()
	count := 0
	prev := captureFlushSpawner
	captureFlushSpawner = func(cwd string) error {
		count++
		return nil
	}
	return &count, func() { captureFlushSpawner = prev }
}

// captureHookPayload builds a PostToolUse hook payload as a JSON string.
func captureHookPayload(t *testing.T, cwd, tool string, toolInput map[string]any) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"session_id":      "sess-xyz",
		"transcript_path": "/tmp/fake.jsonl",
		"cwd":             cwd,
		"tool_name":       tool,
		"tool_input":      toolInput,
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// initDevlogAt runs `devlog init` equivalent by creating the .devlog
// directory structure used by capture: state.json + default config.json.
// Returns the absolute devlog dir.
func initDevlogAt(t *testing.T, root string) string {
	t.Helper()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir devlog: %v", err)
	}
	s := &state.State{SessionID: "sess-init", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := state.Save(filepath.Join(devlogDir, "state.json"), s); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), state.Default()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return devlogDir
}

func readBuffer(t *testing.T, path string) []buffer.Entry {
	t.Helper()
	entries, err := buffer.ReadAll(path)
	if err != nil {
		t.Fatalf("read buffer: %v", err)
	}
	return entries
}

func TestCaptureEditRecordsFileAndTruncatedDetail(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	payload := captureHookPayload(t, root, "Edit", map[string]any{
		"file_path":  "src/api/handler.go",
		"old_string": "Timeout: 30 * time.Second",
		"new_string": "Timeout: 60 * time.Second",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	rc := Capture(nil)
	if rc != 0 {
		t.Fatalf("Capture rc = %d, want 0 (hooks always exit 0)", rc)
	}

	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("expected 1 buffer entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "Edit" {
		t.Errorf("Tool = %q, want Edit", e.Tool)
	}
	if e.File != "src/api/handler.go" {
		t.Errorf("File = %q", e.File)
	}
	if !strings.Contains(e.Detail, "old:") || !strings.Contains(e.Detail, "new:") {
		t.Errorf("Detail should contain old: / new:, got %q", e.Detail)
	}
	if !e.Changed {
		t.Errorf("Edit should be Changed=true")
	}
	if e.Seq == 0 {
		t.Errorf("Seq should have been assigned")
	}
	if e.SessionID != "sess-init" {
		t.Errorf("SessionID = %q, want sess-init", e.SessionID)
	}
}

func TestCaptureEditTruncatesLongStringsTo200Chars(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	longStr := strings.Repeat("x", 500)
	payload := captureHookPayload(t, root, "Edit", map[string]any{
		"file_path":  "huge.go",
		"old_string": longStr,
		"new_string": longStr + "y",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	// With cfg.MaxDetailChars=200 and the old/new strings being 500 and
	// 501 chars each, the total detail should be roughly 2*200 plus
	// formatting overhead but never contain the full 500-char run.
	if strings.Contains(entries[0].Detail, longStr) {
		t.Errorf("Detail should have been truncated, but full 500-char string is present")
	}
}

func TestCaptureWriteRecordsFileAndByteCount(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	body := strings.Repeat("line\n", 10) // 50 bytes, 10 lines
	payload := captureHookPayload(t, root, "Write", map[string]any{
		"file_path": "out.txt",
		"content":   body,
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "Write" {
		t.Errorf("Tool = %q", e.Tool)
	}
	if !strings.Contains(e.Detail, "50 bytes") {
		t.Errorf("Detail = %q, want it to contain '50 bytes'", e.Detail)
	}
	if e.DiffLines != 10 {
		t.Errorf("DiffLines = %d, want 10", e.DiffLines)
	}
}

func TestCaptureBashWithCleanTreeMarksUnchanged(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	// Create initial commit so HEAD exists.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")
	devlogDir := initDevlogAt(t, root)

	payload := captureHookPayload(t, root, "Bash", map[string]any{
		"command": "go test ./...",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "Bash" {
		t.Errorf("Tool = %q", e.Tool)
	}
	if e.Changed {
		t.Errorf("clean tree should give Changed=false")
	}
	if !strings.Contains(e.Detail, "go test") {
		t.Errorf("Detail = %q, want it to contain 'go test'", e.Detail)
	}
}

func TestCaptureBashWithTreeChangesRecordsDiff(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	filePath := filepath.Join(root, "a.txt")
	if err := os.WriteFile(filePath, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "init")
	// Modify the file so `git diff --stat HEAD` sees a change.
	if err := os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	devlogDir := initDevlogAt(t, root)

	payload := captureHookPayload(t, root, "Bash", map[string]any{
		"command": "echo done",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.Changed {
		t.Errorf("tree-mutating Bash should give Changed=true")
	}
}

func TestCaptureSpawnsFlushAtThreshold(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	// Drop buffer_size to 3 for a quick threshold.
	cfg := state.Default()
	cfg.BufferSize = 3
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	count, restore := noopFlushSpawner(t)
	defer restore()

	for i := 0; i < 3; i++ {
		payload := captureHookPayload(t, root, "Edit", map[string]any{
			"file_path":  "x.go",
			"old_string": "a",
			"new_string": "b",
		})
		withCaptureStdin(t, payload)
		if rc := Capture(nil); rc != 0 {
			t.Fatalf("rc = %d (iter %d)", rc, i)
		}
	}

	if *count != 1 {
		t.Errorf("expected flush spawn exactly once, got %d", *count)
	}

	// After the threshold crossing the buffer_count should be back to 0
	// and flush_in_progress should have been set.
	s, err := state.Load(filepath.Join(devlogDir, "state.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.BufferCount != 0 {
		t.Errorf("BufferCount after threshold = %d, want 0", s.BufferCount)
	}
	if !s.FlushInProgress {
		t.Errorf("FlushInProgress should be true after threshold")
	}
}

func TestCaptureDoesNotSpawnFlushBelowThreshold(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	cfg := state.Default()
	cfg.BufferSize = 10
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	count, restore := noopFlushSpawner(t)
	defer restore()

	for i := 0; i < 5; i++ {
		payload := captureHookPayload(t, root, "Edit", map[string]any{
			"file_path":  "y.go",
			"old_string": "x",
			"new_string": "y",
		})
		withCaptureStdin(t, payload)
		if rc := Capture(nil); rc != 0 {
			t.Fatalf("rc = %d", rc)
		}
	}
	if *count != 0 {
		t.Errorf("expected NO flush spawn, got %d", *count)
	}
}

func TestCaptureWhenDisabledSkipsEverything(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	cfg := state.Default()
	disabled := false
	cfg.Enabled = &disabled
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	payload := captureHookPayload(t, root, "Edit", map[string]any{
		"file_path":  "z.go",
		"old_string": "a",
		"new_string": "b",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	// Buffer must be untouched.
	if _, err := os.Stat(filepath.Join(devlogDir, "buffer.jsonl")); err == nil {
		t.Errorf("disabled capture should not have created buffer.jsonl")
	}
}

func TestCaptureExitsZeroOnMissingStdin(t *testing.T) {
	// When stdin cannot be parsed, capture must still exit 0 — the
	// working agent must never be blocked by a broken hook.
	f, err := os.CreateTemp(t.TempDir(), "empty-*.json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	prev := captureStdin
	captureStdin = func() *os.File { return f }
	defer func() {
		captureStdin = prev
		_ = f.Close()
	}()

	if rc := Capture(nil); rc != 0 {
		t.Errorf("rc = %d, want 0 even on bad stdin", rc)
	}
}

func TestCaptureIgnoresUnsupportedTool(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	payload := captureHookPayload(t, root, "SomeOtherTool", map[string]any{
		"whatever": "val",
	})
	withCaptureStdin(t, payload)
	_, restore := noopFlushSpawner(t)
	defer restore()

	if rc := Capture(nil); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	// Buffer file should NOT be created.
	if _, err := os.Stat(filepath.Join(devlogDir, "buffer.jsonl")); err == nil {
		t.Errorf("unsupported tool should not produce a buffer entry")
	}
}

func TestCaptureIncrementsSeqAcrossCalls(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := initDevlogAt(t, root)

	_, restore := noopFlushSpawner(t)
	defer restore()

	for i := 0; i < 3; i++ {
		payload := captureHookPayload(t, root, "Edit", map[string]any{
			"file_path":  "x.go",
			"old_string": "a",
			"new_string": "b",
		})
		withCaptureStdin(t, payload)
		if rc := Capture(nil); rc != 0 {
			t.Fatalf("rc = %d", rc)
		}
	}
	entries := readBuffer(t, filepath.Join(devlogDir, "buffer.jsonl"))
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Seq != i+1 {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

// runGit shells out to git for test setup. Kept tiny — we only use it
// to seed test repos with known content.
func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
}
