package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlog/internal/state"
	"devlog/internal/testutil"
)

// captureStreams redirects cmd.stdoutWriter and cmd.stderrWriter to
// buffers for the duration of the test. Returns the two buffers and a
// restore function that the caller must defer.
func captureStreams(t *testing.T) (*bytes.Buffer, *bytes.Buffer, func()) {
	t.Helper()
	var stdoutBuf, stderrBuf bytes.Buffer
	origOut, origErr := stdoutWriter, stderrWriter
	stdoutWriter = &stdoutBuf
	stderrWriter = &stderrBuf
	return &stdoutBuf, &stderrBuf, func() {
		stdoutWriter = origOut
		stderrWriter = origErr
	}
}

// nonRepoDir returns a tempdir that is guaranteed NOT to be inside a git
// repo. If the OS tempdir happens to sit under a user checkout the test
// skips rather than pretending — we want to exercise the error path.
func nonRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Verify no ancestor has a .git — walk upward ourselves.
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			t.Skipf("tempdir %s is under a git repo; skipping non-repo test", dir)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return dir
}

func TestInitFailsWithoutGitRepo(t *testing.T) {
	dir := nonRepoDir(t)

	_, stderrBuf, restore := captureStreams(t)
	defer restore()

	rc := Init([]string{"--project", dir})
	if rc == 0 {
		t.Fatalf("expected non-zero rc when repo is missing, got 0")
	}
	msg := stderrBuf.String()
	if !strings.Contains(msg, "no git repository found") {
		t.Errorf("stderr should mention 'no git repository found', got:\n%s", msg)
	}
	if !strings.Contains(msg, "git init") {
		t.Errorf("stderr should reference 'git init', got:\n%s", msg)
	}
}

func TestInitCreatesDevlogDirAndStateOnFreshRepo(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	// Remove the pre-made .devlog so init has to create it itself.
	if err := os.RemoveAll(filepath.Join(root, ".devlog")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	_, _, restore := captureStreams(t)
	defer restore()

	rc := Init([]string{"--project", root})
	if rc != 0 {
		t.Fatalf("Init rc = %d, want 0", rc)
	}

	devlogDir := filepath.Join(root, ".devlog")
	if _, err := os.Stat(devlogDir); err != nil {
		t.Fatalf("expected .devlog dir: %v", err)
	}

	s, err := state.Load(filepath.Join(devlogDir, "state.json"))
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if len(s.SessionID) != 16 {
		t.Errorf("SessionID should be 16 hex chars, got %q", s.SessionID)
	}
	// verify lowercase hex
	for _, r := range s.SessionID {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("SessionID contains non-hex char %q: %q", r, s.SessionID)
			break
		}
	}
	if s.StartedAt == "" {
		t.Errorf("StartedAt should be non-empty")
	}
}

func TestInitWritesDefaultConfigIfMissing(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	if err := os.RemoveAll(filepath.Join(root, ".devlog")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	_, _, restore := captureStreams(t)
	defer restore()
	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}

	configPath := filepath.Join(root, ".devlog", "config.json")
	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BufferSize != 10 || cfg.CompanionInterval != 5 {
		t.Errorf("config missing defaults: %+v", cfg)
	}
}

func TestInitPreservesExistingSession(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)

	_, _, restore := captureStreams(t)
	defer restore()

	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("first Init rc = %d", rc)
	}
	first, err := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if err != nil {
		t.Fatalf("load first: %v", err)
	}

	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("second Init rc = %d", rc)
	}
	second, err := state.Load(filepath.Join(root, ".devlog", "state.json"))
	if err != nil {
		t.Fatalf("load second: %v", err)
	}

	if second.SessionID != first.SessionID {
		t.Errorf("SessionID changed on re-init: %q -> %q", first.SessionID, second.SessionID)
	}
}

func TestInitForceRegeneratesSession(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)

	_, _, restore := captureStreams(t)
	defer restore()

	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("first Init rc = %d", rc)
	}
	first, _ := state.Load(filepath.Join(root, ".devlog", "state.json"))

	if rc := Init([]string{"--project", root, "--force"}); rc != 0 {
		t.Fatalf("force Init rc = %d", rc)
	}
	second, _ := state.Load(filepath.Join(root, ".devlog", "state.json"))

	if second.SessionID == first.SessionID {
		t.Errorf("--force should regenerate SessionID, got same: %q", first.SessionID)
	}
}

func TestInitDoesNotOverwriteExistingConfig(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	configPath := filepath.Join(root, ".devlog", "config.json")

	// Seed a custom config.
	custom := state.Default()
	custom.BufferSize = 99
	if err := state.SaveConfig(configPath, custom); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	_, _, restore := captureStreams(t)
	defer restore()
	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("Init rc = %d", rc)
	}
	if rc := Init([]string{"--project", root, "--force"}); rc != 0 {
		t.Fatalf("force Init rc = %d", rc)
	}

	got, err := state.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.BufferSize != 99 {
		t.Errorf("Init overwrote user config: BufferSize = %d, want 99", got.BufferSize)
	}
}

func TestInitPrintsHumanReadableStatus(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	if err := os.RemoveAll(filepath.Join(root, ".devlog")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	stdoutBuf, _, restore := captureStreams(t)
	defer restore()

	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	out := stdoutBuf.String()
	if !strings.Contains(out, "initialized") {
		t.Errorf("stdout missing 'initialized': %q", out)
	}
	if !strings.Contains(out, "new session") {
		t.Errorf("fresh init should say 'new session', got: %q", out)
	}
}

func TestInitPrintsResumedOnRerun(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)

	_, _, restore := captureStreams(t)
	defer restore()
	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("first Init rc = %d", rc)
	}

	var stdoutBuf bytes.Buffer
	origOut := stdoutWriter
	stdoutWriter = &stdoutBuf
	defer func() { stdoutWriter = origOut }()

	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("second Init rc = %d", rc)
	}
	if !strings.Contains(stdoutBuf.String(), "resumed") {
		t.Errorf("second Init should say 'resumed', got: %q", stdoutBuf.String())
	}
}

func TestInitFlagHelpReturnsNonZero(t *testing.T) {
	_, _, restore := captureStreams(t)
	defer restore()

	// flag.ContinueOnError with an unknown flag writes usage to the
	// flagset output and returns an error — Init reports 2.
	rc := Init([]string{"--bogus-flag"})
	if rc != 2 {
		t.Errorf("expected rc=2 for unknown flag, got %d", rc)
	}
}

func TestInitStateJSONSchema(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	if err := os.RemoveAll(filepath.Join(root, ".devlog")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	_, _, restore := captureStreams(t)
	defer restore()
	if rc := Init([]string{"--project", root}); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".devlog", "state.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"session_id", "started_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("state.json missing key %q: %s", key, string(raw))
		}
	}
}
