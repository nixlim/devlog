//go:build e2e

// Package cmd end-to-end test.
//
// Drives the full DevLog pipeline as a single Go test. Avoids exec'ing
// the `devlog` binary — every subcommand is called as a function. The
// claude CLI is simulated via the FakeClaude test shim compiled from
// internal/testutil, with the stub's response rewritten between phases
// so the summariser and companion each see an appropriate payload.
//
// Phases (see SPEC §"Session Lifecycle"):
//
//  1. init          →  creates .devlog/, writes state.json, records session
//  2. task-capture  →  first user prompt becomes .devlog/task.md
//  3. 10 × capture  →  buffer fills; threshold crossing triggers a flush
//  4. flush         →  Haiku summarises; log.jsonl grows; counter bumps
//  5. repeat 4 until companion threshold crossed
//  6. companion     →  Sonnet emits JSON verdict; feedback.md populated
//  7. check-feedback →  hook reads feedback.md, emits banner, truncates
//
// Each phase asserts the on-disk artefacts it owns before moving on, so a
// regression in any one stage surfaces here with its name attached.
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
	"devlog/internal/feedback"
	"devlog/internal/state"
	"devlog/internal/testutil"
)

func TestEndToEndPipeline(t *testing.T) {
	root := newE2EProject(t)
	fc := testutil.NewFakeClaude(t)

	// --- Phase 1: init ------------------------------------------------
	runInit(t, root)
	assertDevlogInitialized(t, root)

	// Point config at the fake claude binary and tune thresholds so the
	// test completes in a single flush and a single companion call.
	cfgPath := filepath.Join(root, ".devlog", "config.json")
	cfg, err := state.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.ClaudeCommand = fc.BinPath
	cfg.BufferSize = 10
	cfg.CompanionInterval = 1
	cfg.SummarizerTimeoutSeconds = 30
	cfg.CompanionTimeoutSeconds = 30
	if err := state.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// --- Phase 2: task-capture ---------------------------------------
	const userPrompt = "Fix the 500 error on /api/recommendations"
	runTaskCapture(t, root, userPrompt)
	assertTaskFile(t, root, userPrompt)

	// --- Phase 3: 10 captures -----------------------------------------
	// Intercept capture's spawner so we drive flush inline (ordering of
	// asserts matters more than genuine backgrounding in this test).
	var spawnedFlush bool
	withCaptureSpawner(t, func(cwd string) error {
		spawnedFlush = true
		return nil
	})
	for i := 0; i < 10; i++ {
		runCapture(t, root, fmt.Sprintf("src/handler_%d.go", i))
	}
	assertBufferHas(t, root, 10)
	if !spawnedFlush {
		t.Fatal("capture should have spawned flush on the 10th entry")
	}

	// --- Phase 4: flush ----------------------------------------------
	fc.SetResponse(e2eSummariserEnvelope(
		"Tuning database timeouts on /api/recommendations for the third time."), "", 0)

	runFlush(t, root)
	assertBufferHas(t, root, 0)
	assertLogEntries(t, root, 1)
	assertArchiveNonEmpty(t, root)

	// Because CompanionInterval=1, that single flush should have crossed
	// the companion threshold and spawned it. We stub the spawn so we
	// can run companion inline.
	withFlushSpawner(t, func(root string) error { return nil })

	// --- Phase 5 is folded into phase 4 (single-flush config). --------

	// --- Phase 6: companion -------------------------------------------
	fc.SetResponse(e2eCompanionEnvelope(spiralingResult), "", 0)

	runCompanion(t, root)
	assertFeedbackFile(t, root)
	assertLastCompanionStatus(t, root, feedback.StatusSpiraling)

	// --- Phase 7: check-feedback --------------------------------------
	banner := runCheckFeedback(t, root)
	if !strings.Contains(banner, "SPIRALING") {
		t.Errorf("check-feedback output missing SPIRALING banner: %q", banner)
	}
	if !strings.Contains(banner, "Repetition Lock") {
		t.Errorf("banner missing pattern text: %q", banner)
	}
	assertFeedbackArchived(t, root)
	assertFeedbackEmpty(t, root)
}

// --- Phase helpers ----------------------------------------------------

func newE2EProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Initialise a real git repo — init validates it before proceeding.
	cmd := exec.Command("git", "init", "--quiet", root)
	cmd.Env = append(os.Environ(), "GIT_TEMPLATE_DIR=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, args := range [][]string{
		{"-C", root, "config", "user.email", "e2e@devlog.local"},
		{"-C", root, "config", "user.name", "DevLog E2E"},
		{"-C", root, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

func runInit(t *testing.T, root string) {
	t.Helper()
	withStreams(t)
	if code := Init([]string{"--project", root}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
}

func runTaskCapture(t *testing.T, root, prompt string) {
	t.Helper()
	withStreams(t)
	writeStdinPayload(t, taskCapturePayload(root, prompt, "sess-e2e"),
		func(f *os.File) { taskCaptureStdin = func() *os.File { return f } },
		func() { taskCaptureStdin = func() *os.File { return os.Stdin } })
	if code := TaskCapture(nil); code != 0 {
		t.Fatalf("task-capture exit = %d", code)
	}
}

func runCapture(t *testing.T, root, filePath string) {
	t.Helper()
	withStreams(t)
	payload := editCapturePayload(root, filePath)
	writeStdinPayload(t, payload,
		func(f *os.File) { captureStdin = func() *os.File { return f } },
		func() { captureStdin = func() *os.File { return os.Stdin } })
	if code := Capture(nil); code != 0 {
		t.Fatalf("capture exit = %d", code)
	}
}

func runFlush(t *testing.T, root string) {
	t.Helper()
	withStreams(t)
	if code := Flush([]string{"--project", root}); code != 0 {
		t.Fatalf("flush exit = %d", code)
	}
}

func runCompanion(t *testing.T, root string) {
	t.Helper()
	withStreams(t)
	if code := Companion([]string{"--project", root}); code != 0 {
		t.Fatalf("companion exit = %d", code)
	}
}

func runCheckFeedback(t *testing.T, root string) string {
	t.Helper()
	stdout, _ := withStreams(t)
	// check-feedback resolves its .devlog dir from the process cwd, so
	// switch to the project root for the duration of the call.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if code := CheckFeedback(nil); code != 0 {
		t.Fatalf("check-feedback exit = %d", code)
	}
	return stdout.String()
}

// --- Stdin plumbing ---------------------------------------------------

func writeStdinPayload(t *testing.T, body string, set func(*os.File), restore func()) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin-*.json")
	if err != nil {
		t.Fatalf("create stdin: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdin: %v", err)
	}
	set(f)
	t.Cleanup(func() {
		restore()
		_ = f.Close()
	})
}

// withCaptureSpawner installs a recorder for the capture→flush handoff.
func withCaptureSpawner(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := captureFlushSpawner
	captureFlushSpawner = fn
	t.Cleanup(func() { captureFlushSpawner = prev })
}

// withFlushSpawner overrides the flush→companion handoff. Tests drive
// companion inline rather than spawning it.
func withFlushSpawner(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := flushCompanionSpawner
	flushCompanionSpawner = fn
	t.Cleanup(func() { flushCompanionSpawner = prev })
}

// --- Payload construction ---------------------------------------------

func taskCapturePayload(cwd, prompt, sessionID string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"transcript_path": "/tmp/fake.jsonl",
		"cwd":             cwd,
		"prompt":          prompt,
	})
	return string(b)
}

func editCapturePayload(cwd, filePath string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id":      "sess-e2e",
		"transcript_path": "/tmp/fake.jsonl",
		"cwd":             cwd,
		"tool_name":       "Edit",
		"tool_input": map[string]any{
			"file_path":  filePath,
			"old_string": "Timeout: 30 * time.Second",
			"new_string": "Timeout: 60 * time.Second",
		},
	})
	return string(b)
}

// --- Claude envelope builders -----------------------------------------

func e2eSummariserEnvelope(summary string) string {
	b, _ := json.Marshal(map[string]any{
		"type":        "result",
		"subtype":     "success",
		"result":      summary,
		"model":       "claude-haiku-4-5-20251001",
		"duration_ms": 1200,
		"session_id":  "fc-sess",
		"num_turns":   1,
		"is_error":    false,
	})
	return string(b)
}

func e2eCompanionEnvelope(resultJSON string) string {
	b, _ := json.Marshal(map[string]any{
		"type":        "result",
		"subtype":     "success",
		"result":      resultJSON,
		"model":       "claude-sonnet-4-6",
		"duration_ms": 2200,
		"session_id":  "fc-sess",
		"num_turns":   1,
		"is_error":    false,
	})
	return string(b)
}

// spiralingResult is the JSON payload Sonnet would emit for a SPIRALING
// trajectory. The exact shape matches feedback.CompanionResult.
const spiralingResult = `{
  "status": "spiraling",
  "confidence": 0.85,
  "pattern": "Repetition Lock",
  "evidence": [
    "Log #1: 'Tuning database timeouts on /api/recommendations for the third time.'"
  ],
  "summary": "Agent has made 10 consecutive database-layer edits targeting the same 500 error.",
  "intervention": "STOP. You have made 10 database-related changes and the error is unchanged. Reconsider the assumption that the problem is database-related.",
  "reframe": "Ask: what ELSE in the request path could produce a timeout?"
}`

// --- Assertions -------------------------------------------------------

func assertDevlogInitialized(t *testing.T, root string) {
	t.Helper()
	statePath := filepath.Join(root, ".devlog", "state.json")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("state.json not readable after init: %v", err)
	}
	if s.SessionID == "" {
		t.Error("init did not set session_id")
	}
	if _, err := os.Stat(filepath.Join(root, ".devlog", "config.json")); err != nil {
		t.Errorf("config.json missing: %v", err)
	}
}

func assertTaskFile(t *testing.T, root, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "task.md"))
	if err != nil {
		t.Fatalf("task.md missing: %v", err)
	}
	if !strings.HasPrefix(string(data), want) {
		t.Errorf("task.md prefix mismatch: got %q", string(data))
	}
}

func assertBufferHas(t *testing.T, root string, want int) {
	t.Helper()
	entries, err := buffer.ReadAll(filepath.Join(root, ".devlog", "buffer.jsonl"))
	if err != nil {
		t.Fatalf("read buffer: %v", err)
	}
	if len(entries) != want {
		t.Errorf("buffer entry count = %d, want %d", len(entries), want)
	}
}

func assertLogEntries(t *testing.T, root string, want int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "log.jsonl"))
	if err != nil {
		t.Fatalf("log.jsonl missing: %v", err)
	}
	lines := 0
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines++
		}
	}
	if lines != want {
		t.Errorf("log.jsonl entries = %d, want %d", lines, want)
	}
	// Also cross-check via the typed reader.
	typed, err := devlog.ReadLastN(filepath.Join(root, ".devlog", "log.jsonl"), want+5)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(typed) != want {
		t.Errorf("devlog.ReadLastN = %d entries, want %d", len(typed), want)
	}
}

func assertArchiveNonEmpty(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "buffer_archive.jsonl"))
	if err != nil {
		t.Fatalf("buffer_archive.jsonl missing: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Error("buffer_archive.jsonl should contain flushed entries")
	}
}

func assertFeedbackFile(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "feedback.md"))
	if err != nil {
		t.Fatalf("feedback.md missing: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Error("feedback.md should contain a formatted intervention")
	}
}

func assertLastCompanionStatus(t *testing.T, root, want string) {
	t.Helper()
	s, err := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if s.LastCompanion == nil {
		t.Fatal("state.LastCompanion not recorded")
	}
	if s.LastCompanion.Status != want {
		t.Errorf("LastCompanion.Status = %q, want %q", s.LastCompanion.Status, want)
	}
	if s.CompanionInProgress {
		t.Error("companion_in_progress should be cleared after companion returns")
	}
}

func assertFeedbackArchived(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "feedback_archive.jsonl"))
	if err != nil {
		t.Fatalf("feedback_archive.jsonl missing after check-feedback: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Error("feedback_archive.jsonl should contain the consumed intervention")
	}
}

func assertFeedbackEmpty(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "feedback.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("stat feedback.md: %v", err)
	}
	if len(bytes.TrimSpace(data)) != 0 {
		t.Errorf("feedback.md should be truncated after consumption, got: %q", string(data))
	}
}
