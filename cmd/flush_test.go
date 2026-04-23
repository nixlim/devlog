package cmd

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/claude"
	"devlog/internal/devlog"
	"devlog/internal/state"
)

// stubRunner implements claudeRunnerIface with a scripted response.
type stubRunner struct {
	model      string
	prompt     string
	calls      int32
	wantCalled int

	response *claude.Response
	err      error
}

func (s *stubRunner) Run(ctx context.Context, model, promptText string, timeout time.Duration) (*claude.Response, error) {
	atomic.AddInt32(&s.calls, 1)
	s.model = model
	s.prompt = promptText
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func withRunner(t *testing.T, r *stubRunner) {
	t.Helper()
	prev := flushClaudeRunner
	flushClaudeRunner = func(_ *state.Config) claudeRunnerIface { return r }
	t.Cleanup(func() { flushClaudeRunner = prev })
}

// spawnRecorder replaces flushCompanionSpawner and records invocations.
type spawnRecorder struct {
	roots []string
	err   error
}

func withSpawnRecorder(t *testing.T) *spawnRecorder {
	t.Helper()
	rec := &spawnRecorder{}
	prev := flushCompanionSpawner
	flushCompanionSpawner = func(root string) error {
		rec.roots = append(rec.roots, root)
		return rec.err
	}
	t.Cleanup(func() { flushCompanionSpawner = prev })
	return rec
}

// fixedFlushTime pins flushNow for deterministic log entry timestamps.
func fixedFlushTime(t *testing.T, ts time.Time) {
	t.Helper()
	prev := flushNow
	flushNow = func() time.Time { return ts }
	t.Cleanup(func() { flushNow = prev })
}

// seedFlushProject sets up a .devlog/ layout with a given number of buffer
// entries and an initial state. Returns the project root.
func seedFlushProject(t *testing.T, bufferEntries int, initial *state.State) string {
	t.Helper()
	root := t.TempDir()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed config with a short timeout so failures surface fast.
	cfg := state.Default()
	cfg.SummarizerTimeoutSeconds = 5
	cfg.CompanionTimeoutSeconds = 5
	cfg.CompanionInterval = 3
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Seed state.
	s := initial
	if s == nil {
		s = &state.State{SessionID: "sess-flush", StartedAt: "2026-04-22T22:00:00Z"}
	}
	if err := state.Save(filepath.Join(devlogDir, "state.json"), s); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	// Seed buffer entries.
	for i := 0; i < bufferEntries; i++ {
		entry := buffer.Entry{
			Seq:       100 + i,
			TS:        "2026-04-22T22:15:00Z",
			SessionID: s.SessionID,
			Tool:      "Edit",
			File:      "src/api/handler.go",
			Detail:    "stub detail",
			DiffLines: 4,
			Changed:   true,
		}
		if err := buffer.Append(filepath.Join(devlogDir, "buffer.jsonl"), entry); err != nil {
			t.Fatalf("seed buffer %d: %v", i, err)
		}
	}
	if err := os.WriteFile(filepath.Join(devlogDir, "task.md"),
		[]byte("Fix the 500 error on /api/recommendations\n"), 0o644); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return root
}

// readLogEntries returns all decoded log.jsonl entries.
func readLogEntries(t *testing.T, path string) []devlog.Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []devlog.Entry
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e devlog.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func TestFlushSummarisesBufferAndArchives(t *testing.T) {
	root := seedFlushProject(t, 3, nil)
	fixedFlushTime(t, time.Date(2026, 4, 22, 22, 15, 30, 0, time.UTC))

	runner := &stubRunner{
		response: &claude.Response{
			Type:       "result",
			Subtype:    "success",
			Result:     "  Tweaking database timeouts to chase a 500 error.  ",
			Model:      "claude-haiku-4-5-20251001",
			DurationMS: 1150,
		},
	}
	withRunner(t, runner)
	withSpawnRecorder(t)
	withStreams(t)

	code := Flush([]string{"--project", root})
	if code != 0 {
		t.Fatalf("Flush exit = %d", code)
	}
	if atomic.LoadInt32(&runner.calls) != 1 {
		t.Errorf("claude runner called %d times, want 1", runner.calls)
	}
	if runner.model != "claude-haiku-4-5-20251001" {
		t.Errorf("runner model = %q", runner.model)
	}
	if !strings.Contains(runner.prompt, "Fix the 500 error") {
		t.Errorf("prompt missing task: %s", runner.prompt)
	}

	// log.jsonl should have a single trimmed entry covering seq 100..102.
	logPath := filepath.Join(root, ".devlog", "log.jsonl")
	entries := readLogEntries(t, logPath)
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Summary != "Tweaking database timeouts to chase a 500 error." {
		t.Errorf("summary not trimmed: %q", got.Summary)
	}
	if got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q", got.Model)
	}
	if got.DurationMS != 1150 {
		t.Errorf("duration = %d", got.DurationMS)
	}
	if got.Seq != 1 {
		t.Errorf("seq = %d, want 1", got.Seq)
	}
	if len(got.CoversSeqs) != 3 || got.CoversSeqs[0] != 100 || got.CoversSeqs[2] != 102 {
		t.Errorf("CoversSeqs = %v", got.CoversSeqs)
	}

	// Buffer archived and cleared.
	archivePath := filepath.Join(root, ".devlog", "buffer_archive.jsonl")
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if !strings.Contains(string(archiveData), `"seq":100`) {
		t.Errorf("archive missing seeded entries: %s", string(archiveData))
	}
	bufEntries, err := buffer.ReadAll(filepath.Join(root, ".devlog", "buffer.jsonl"))
	if err != nil {
		t.Fatalf("read buffer: %v", err)
	}
	if len(bufEntries) != 0 {
		t.Errorf("buffer not cleared: %d entries remain", len(bufEntries))
	}

	// State counters updated, flush guard released.
	s, err := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.FlushInProgress {
		t.Error("flush_in_progress should be cleared")
	}
	if s.LogCount != 1 || s.LogSeq != 1 {
		t.Errorf("log counters: count=%d seq=%d", s.LogCount, s.LogSeq)
	}
	if s.BufferCount != 0 {
		t.Errorf("buffer_count = %d, want 0", s.BufferCount)
	}
	if s.LogSinceCompanion != 1 {
		t.Errorf("log_since_companion = %d, want 1", s.LogSinceCompanion)
	}
}

func TestFlushEmptyBufferIsNoop(t *testing.T) {
	root := seedFlushProject(t, 0, nil)
	runner := &stubRunner{}
	withRunner(t, runner)
	withSpawnRecorder(t)
	withStreams(t)

	code := Flush([]string{"--project", root})
	if code != 0 {
		t.Fatalf("Flush exit = %d", code)
	}
	if atomic.LoadInt32(&runner.calls) != 0 {
		t.Error("claude should not be invoked for an empty buffer")
	}
	// No log entries.
	if _, err := os.Stat(filepath.Join(root, ".devlog", "log.jsonl")); !os.IsNotExist(err) {
		t.Errorf("log.jsonl should not exist for empty-buffer flush: %v", err)
	}
}

func TestFlushProceedsWhenGuardPreSet(t *testing.T) {
	// Capture pre-sets flush_in_progress=true before spawning flush, so
	// the spawned flush MUST proceed past that flag rather than exit. Any
	// regression that makes flush short-circuit on the pre-set guard would
	// leave the capture→flush pipeline silently broken.
	root := seedFlushProject(t, 2, &state.State{
		SessionID:       "sess-flush",
		StartedAt:       "2026-04-22T22:00:00Z",
		FlushInProgress: true,
	})

	runner := &stubRunner{
		response: &claude.Response{
			Type:       "result",
			Result:     "proceeded through pre-set guard",
			Model:      "claude-haiku-4-5-20251001",
			DurationMS: 100,
		},
	}
	withRunner(t, runner)
	withSpawnRecorder(t)
	fixedFlushTime(t, time.Now().UTC())
	withStreams(t)

	code := Flush([]string{"--project", root})
	if code != 0 {
		t.Errorf("flush exit = %d", code)
	}
	if atomic.LoadInt32(&runner.calls) != 1 {
		t.Errorf("claude should have been invoked once, got %d", runner.calls)
	}
	// Guard released on exit.
	s, _ := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if s.FlushInProgress {
		t.Error("flush_in_progress should be cleared on exit")
	}
	// Buffer drained.
	entries, _ := buffer.ReadAll(filepath.Join(root, ".devlog", "buffer.jsonl"))
	if len(entries) != 0 {
		t.Errorf("buffer should be cleared: %d entries remain", len(entries))
	}
}

func TestFlushSummariserFailurePreservesBuffer(t *testing.T) {
	root := seedFlushProject(t, 2, nil)
	runner := &stubRunner{err: claude.ErrEmptyResponse}
	withRunner(t, runner)
	withSpawnRecorder(t)
	_, stderr := withStreams(t)

	code := Flush([]string{"--project", root})
	if code == 0 {
		t.Error("Flush should return non-zero on summariser failure")
	}
	if !strings.Contains(stderr.String(), "empty response") {
		t.Errorf("stderr missing diagnostic: %q", stderr.String())
	}

	// Buffer retained, archive not populated, no log entry written.
	entries, _ := buffer.ReadAll(filepath.Join(root, ".devlog", "buffer.jsonl"))
	if len(entries) != 2 {
		t.Errorf("buffer should be preserved: got %d entries", len(entries))
	}
	if _, err := os.Stat(filepath.Join(root, ".devlog", "buffer_archive.jsonl")); !os.IsNotExist(err) {
		t.Errorf("archive should not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".devlog", "log.jsonl")); !os.IsNotExist(err) {
		t.Errorf("log.jsonl should not exist: %v", err)
	}

	// Guard released.
	s, _ := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if s.FlushInProgress {
		t.Error("flush_in_progress should be cleared even after failure")
	}

	// Error logged.
	logBytes, err := os.ReadFile(filepath.Join(root, ".devlog", "errors.log"))
	if err != nil {
		t.Fatalf("errors.log missing: %v", err)
	}
	if !strings.Contains(string(logBytes), "flush") {
		t.Errorf("errors.log missing flush entry: %s", string(logBytes))
	}
}

func TestFlushDryRunPrintsPromptWithoutInvoking(t *testing.T) {
	root := seedFlushProject(t, 1, nil)
	runner := &stubRunner{}
	withRunner(t, runner)
	spawn := withSpawnRecorder(t)
	stdout, _ := withStreams(t)

	code := Flush([]string{"--project", root, "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}
	if atomic.LoadInt32(&runner.calls) != 0 {
		t.Error("claude should not be invoked during --dry-run")
	}
	if len(spawn.roots) != 0 {
		t.Errorf("companion should not spawn during --dry-run: %v", spawn.roots)
	}
	out := stdout.String()
	if !strings.Contains(out, "ORIGINAL TASK") ||
		!strings.Contains(out, "BUFFERED DIFFS") {
		t.Errorf("dry-run output missing prompt sections: %q", out)
	}
	// Buffer untouched.
	entries, _ := buffer.ReadAll(filepath.Join(root, ".devlog", "buffer.jsonl"))
	if len(entries) != 1 {
		t.Errorf("dry-run mutated buffer: %d entries", len(entries))
	}
}

func TestFlushSpawnsCompanionAtThreshold(t *testing.T) {
	// Companion interval = 3. Start with 2 already, so one flush bumps to 3.
	root := seedFlushProject(t, 2, &state.State{
		SessionID:         "sess-x",
		StartedAt:         "2026-04-22T22:00:00Z",
		LogSinceCompanion: 2,
	})
	runner := &stubRunner{
		response: &claude.Response{
			Type:       "result",
			Result:     "summary text",
			Model:      "claude-haiku-4-5-20251001",
			DurationMS: 900,
		},
	}
	withRunner(t, runner)
	spawn := withSpawnRecorder(t)
	fixedFlushTime(t, time.Date(2026, 4, 22, 22, 16, 0, 0, time.UTC))
	withStreams(t)

	code := Flush([]string{"--project", root})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(spawn.roots) != 1 {
		t.Fatalf("companion spawn count = %d, want 1", len(spawn.roots))
	}
	if spawn.roots[0] != root {
		t.Errorf("spawn root = %q, want %q", spawn.roots[0], root)
	}
	// log_since_companion should have been reset.
	s, _ := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if s.LogSinceCompanion != 0 {
		t.Errorf("log_since_companion = %d, want 0 after threshold spawn", s.LogSinceCompanion)
	}
}

func TestFlushBelowThresholdDoesNotSpawn(t *testing.T) {
	root := seedFlushProject(t, 1, nil)
	runner := &stubRunner{
		response: &claude.Response{
			Type:       "result",
			Result:     "summary",
			Model:      "claude-haiku-4-5-20251001",
			DurationMS: 500,
		},
	}
	withRunner(t, runner)
	spawn := withSpawnRecorder(t)
	fixedFlushTime(t, time.Now().UTC())
	withStreams(t)

	code := Flush([]string{"--project", root})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(spawn.roots) != 0 {
		t.Errorf("companion should not spawn before threshold: %v", spawn.roots)
	}
}

func TestFlushCommandNotFoundShowsRemediation(t *testing.T) {
	root := seedFlushProject(t, 1, nil)
	runner := &stubRunner{err: stderrors.New("some wrap: " + claude.ErrCommandNotFound.Error())}
	// Use a wrapped sentinel via fmt.Errorf for errors.Is to match.
	runner.err = wrapSentinel(claude.ErrCommandNotFound)
	withRunner(t, runner)
	withSpawnRecorder(t)
	_, stderr := withStreams(t)

	code := Flush([]string{"--project", root})
	if code == 0 {
		t.Error("exit should be non-zero")
	}
	if !strings.Contains(stderr.String(), "not in PATH") {
		t.Errorf("stderr missing install remediation: %q", stderr.String())
	}
}

// wrapSentinel returns an error whose errors.Is chain reaches sentinel.
func wrapSentinel(sentinel error) error {
	return wrapErr{sentinel}
}

type wrapErr struct{ inner error }

func (w wrapErr) Error() string { return "stub: " + w.inner.Error() }
func (w wrapErr) Unwrap() error { return w.inner }
