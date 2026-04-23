package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlog/internal/devlog"
	"devlog/internal/feedback"
	"devlog/internal/state"
	"devlog/internal/testutil"
)

// seedProject writes a config.json, a task.md, and a log.jsonl with one
// entry into <root>/.devlog — the minimum the companion command needs.
// Returns the .devlog dir.
func seedProject(t *testing.T, root string) string {
	t.Helper()
	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := state.Default()
	cfg.ClaudeCommand = "claude" // resolved via PATH in tests
	if err := state.SaveConfig(filepath.Join(devlogDir, "config.json"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := os.WriteFile(filepath.Join(devlogDir, "task.md"),
		[]byte("Fix the 500 error on /api/recommendations"), 0o644); err != nil {
		t.Fatalf("task.md: %v", err)
	}

	if err := devlog.Append(filepath.Join(devlogDir, "log.jsonl"), devlog.Entry{
		Seq: 1, TS: time.Now().UTC(), SessionID: "test-session",
		CoversSeqs: []int{1, 2, 3},
		Summary:    "Increasing DB timeouts and pool size in a hunt for the 500 error.",
		Model:      "claude-haiku-4-5-20251001", DurationMS: 1000,
	}); err != nil {
		t.Fatalf("log append: %v", err)
	}
	return devlogDir
}

// companionEnvelope returns a byte string mimicking `claude -p --output-format json`
// output with the given result text (typically a JSON blob).
func companionEnvelope(resultText string) string {
	env := map[string]any{
		"type":       "result",
		"subtype":    "success",
		"result":     resultText,
		"session_id": "test",
		"model":      "claude-sonnet-4-6",
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func TestCompanion_DryRun_PrintsPromptAndSkipsClaude(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	stdoutBuf, _ := setupCmdStreams(t)
	// No fake claude setup — --dry-run must not call claude.

	code := Companion([]string{"--dry-run", "--project", root})
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	got := stdoutBuf.String()
	for _, section := range []string{
		"ORIGINAL TASK:",
		"DEV LOG:",
		"RAW DIFFS:",
		"Respond ONLY with a single JSON object",
	} {
		if !strings.Contains(got, section) {
			t.Errorf("prompt missing section %q:\n%s", section, got)
		}
	}
	// feedback.md must not exist after a dry run.
	if _, err := os.Stat(filepath.Join(devlogDir, "feedback.md")); !os.IsNotExist(err) {
		t.Errorf("feedback.md should not exist after --dry-run: %v", err)
	}
}

func TestCompanion_SpiralingResult_WritesFeedbackAndUpdatesState(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	// Pre-populate state with a non-zero log_since_companion counter so we
	// can verify the reset on success.
	statePath := filepath.Join(devlogDir, "state.json")
	if err := state.Save(statePath, &state.State{
		SessionID: "test-session", StartedAt: "2026-04-22T22:00:00Z",
		LogSinceCompanion: 5,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	fc := testutil.NewFakeClaude(t)
	fc.PrependPath(t)

	resultBody := `{"status":"spiraling","confidence":0.85,"pattern":"repetition_lock",` +
		`"evidence":["Log #1: increasing pool"],` +
		`"summary":"Six straight DB modifications, error unchanged.",` +
		`"intervention":"STOP. Examine the full stack trace, not just the DB layer.",` +
		`"reframe":"What else could cause the timeout?"}`
	if err := fc.SetResponse(companionEnvelope(resultBody), "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	setupCmdStreams(t)

	code := Companion([]string{"--project", root})
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	// feedback.md must contain the rendered banner.
	data, err := os.ReadFile(filepath.Join(devlogDir, "feedback.md"))
	if err != nil {
		t.Fatalf("read feedback.md: %v", err)
	}
	banner := string(data)
	for _, want := range []string{
		"STATUS: SPIRALING (confidence: 85%)",
		"PATTERN DETECTED: Repetition Lock",
		"EVIDENCE:",
		"REFRAME:",
		"ACTION:",
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("feedback.md missing %q:\n%s", want, banner)
		}
	}

	// state.last_companion must be updated; log_since_companion reset to 0.
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.LastCompanion == nil {
		t.Fatalf("last_companion not set")
	}
	if s.LastCompanion.Status != "spiraling" {
		t.Errorf("last_companion.status: got %q, want %q", s.LastCompanion.Status, "spiraling")
	}
	if s.LastCompanion.Confidence < 0.84 || s.LastCompanion.Confidence > 0.86 {
		t.Errorf("last_companion.confidence: got %v, want ~0.85", s.LastCompanion.Confidence)
	}
	if s.LogSinceCompanion != 0 {
		t.Errorf("log_since_companion: got %d, want 0", s.LogSinceCompanion)
	}
	if s.CompanionInProgress {
		t.Errorf("companion_in_progress left true — guard not released")
	}
}

func TestCompanion_OnTrackResult_SkipsFeedbackWrite(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	fc := testutil.NewFakeClaude(t)
	fc.PrependPath(t)
	resultBody := `{"status":"on_track","confidence":0.95,"pattern":"","evidence":[],` +
		`"summary":"Agent is making coherent progress.",` +
		`"intervention":"","reframe":""}`
	if err := fc.SetResponse(companionEnvelope(resultBody), "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	setupCmdStreams(t)

	if code := Companion([]string{"--project", root}); code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(devlogDir, "feedback.md")); !os.IsNotExist(err) {
		t.Errorf("feedback.md should not exist for on_track result: %v", err)
	}
	s, err := state.Load(filepath.Join(devlogDir, "state.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if s.LastCompanion == nil || s.LastCompanion.Status != "on_track" {
		t.Errorf("last_companion not recorded for on_track: %+v", s.LastCompanion)
	}
}

func TestCompanion_ClaudeNotFound_FailsCleanly(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	t.Setenv("PATH", "") // nothing resolvable
	_, stderrBuf := setupCmdStreams(t)

	code := Companion([]string{"--project", root})
	if code == 0 {
		t.Errorf("expected non-zero exit when claude is missing")
	}
	if !strings.Contains(stderrBuf.String(), "claude command not found") {
		t.Errorf("stderr should mention claude command not found:\n%s", stderrBuf.String())
	}
	// errors.log should have been written.
	data, err := os.ReadFile(filepath.Join(devlogDir, "errors.log"))
	if err != nil {
		t.Fatalf("errors.log not written: %v", err)
	}
	if !strings.Contains(string(data), "companion") {
		t.Errorf("errors.log missing companion entry:\n%s", data)
	}
	// Guard should have been released.
	s, err := state.Load(filepath.Join(devlogDir, "state.json"))
	if err == nil && s.CompanionInProgress {
		t.Errorf("companion_in_progress left true after failure")
	}
}

func TestCompanion_GuardAlreadyHeld_SkipsRun(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	statePath := filepath.Join(devlogDir, "state.json")
	if err := state.Save(statePath, &state.State{
		SessionID: "x", CompanionInProgress: true,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	// No fake claude — a second invocation must never reach it.
	_, stderrBuf := setupCmdStreams(t)

	code := Companion([]string{"--project", root})
	if code != 0 {
		t.Errorf("exit code: got %d, want 0 (guard-skip)", code)
	}
	if !strings.Contains(stderrBuf.String(), "already in progress") {
		t.Errorf("stderr should mention already-in-progress:\n%s", stderrBuf.String())
	}
	// Companion_in_progress MUST still be true — the previous holder still
	// owns it. We must NOT clear it from this skipped run.
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !s.CompanionInProgress {
		t.Errorf("guard-skip cleared companion_in_progress — race condition risk")
	}
}

func TestCompanion_MalformedModelJSON_FailsWithoutUpdatingState(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	devlogDir := seedProject(t, root)
	statePath := filepath.Join(devlogDir, "state.json")
	// Seed a prior counter value we expect to be preserved on failure.
	if err := state.Save(statePath, &state.State{
		SessionID: "x", LogSinceCompanion: 5,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	fc := testutil.NewFakeClaude(t)
	fc.PrependPath(t)
	if err := fc.SetResponse(companionEnvelope("this is not JSON"), "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	setupCmdStreams(t)

	if code := Companion([]string{"--project", root}); code == 0 {
		t.Errorf("expected non-zero exit when model returns junk")
	}
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	// log_since_companion must NOT be reset on failure.
	if s.LogSinceCompanion != 5 {
		t.Errorf("log_since_companion: got %d, want 5 (unchanged on failure)", s.LogSinceCompanion)
	}
	// Guard must have been released.
	if s.CompanionInProgress {
		t.Errorf("companion_in_progress left true after failure")
	}
}

func TestCompanion_MarkdownFencedJSON_IsParsed(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	seedProject(t, root)
	fc := testutil.NewFakeClaude(t)
	fc.PrependPath(t)
	resultBody := "```json\n" +
		`{"status":"drifting","confidence":0.5,"pattern":"","evidence":[],` +
		`"summary":"some drift","intervention":"","reframe":""}` + "\n```"
	if err := fc.SetResponse(companionEnvelope(resultBody), "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	setupCmdStreams(t)

	if code := Companion([]string{"--project", root}); code != 0 {
		t.Errorf("exit code: got %d, want 0 (fenced JSON should be accepted)", code)
	}
}

func TestCompanion_HelpFlag(t *testing.T) {
	stdoutBuf, _ := setupCmdStreams(t)
	for _, arg := range []string{"-h", "--help"} {
		stdoutBuf.Reset()
		if code := Companion([]string{arg}); code != 0 {
			t.Errorf("%q: exit %d, want 0", arg, code)
		}
		if !strings.Contains(stdoutBuf.String(), "Usage:") {
			t.Errorf("%q: missing Usage section:\n%s", arg, stdoutBuf.String())
		}
	}
}

func TestParseCompanionResult(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantErr  bool
		wantStat string
	}{
		{"plain json", `{"status":"on_track","confidence":0.9}`, false, "on_track"},
		{"with preamble",
			`Here is the assessment: {"status":"drifting","confidence":0.5}`, false, "drifting"},
		{"fenced json", "```json\n{\"status\":\"spiraling\",\"confidence\":0.8}\n```", false, "spiraling"},
		{"no object", "no json at all", true, ""},
		{"malformed", `{"status":"drifting", confidence:0.5}`, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseCompanionResult(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error, got result %+v", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got.Status != c.wantStat {
				t.Errorf("status: got %q, want %q", got.Status, c.wantStat)
			}
		})
	}
}

func TestCommitCompanionResult_ResetsCounter(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := state.Save(statePath, &state.State{
		SessionID: "x", LogSinceCompanion: 7,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := feedback.CompanionResult{Status: "spiraling", Confidence: 0.9}
	if err := commitCompanionResult(statePath, r); err != nil {
		t.Fatalf("commit: %v", err)
	}
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.LogSinceCompanion != 0 {
		t.Errorf("log_since_companion: got %d, want 0", s.LogSinceCompanion)
	}
	if s.LastCompanion == nil || s.LastCompanion.Status != "spiraling" {
		t.Errorf("last_companion not updated: %+v", s.LastCompanion)
	}
}
