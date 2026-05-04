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

func TestStatus_FullyInitialized_AllHealthy(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{
		SessionID:         "abc123",
		StartedAt:         "2026-04-22T22:00:00Z",
		BufferCount:       3,
		BufferSeq:         45,
		LogCount:          8,
		LogSinceCompanion: 3,
		LastCompanion: &state.LastCompanion{
			TS:         "2026-04-22T22:14:00Z",
			Status:     "on_track",
			Confidence: 0.92,
		},
	})
	t.Setenv("NO_COLOR", "1")
	// Put a fake claude on PATH so the health check reports OK deterministically.
	fc := testutil.NewFakeClaude(t)
	fc.PrependPath(t)

	var buf bytes.Buffer
	code := writeStatus(root, &buf)
	got := buf.String()

	if code != 0 {
		t.Errorf("expected exit 0 (all healthy), got %d\nOutput:\n%s", code, got)
	}

	for _, want := range []string{
		"Session:        abc123",
		"Started:        2026-04-22T22:00:00Z",
		"Buffer:         3 entries (next seq: 45)",
		"Log:            8 entries (3 since last companion)",
		"Last companion: on_track @ 2026-04-22T22:14:00Z (confidence: 92%)",
		"Health:",
		"git:      OK",
		"claude:   OK", // default config uses host=claude
		".devlog:  OK",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, got)
		}
	}
}

func TestStatus_Uninitialized_ReturnsFailure(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	// Remove .devlog to simulate an uninitialized project.
	if err := os.RemoveAll(filepath.Join(root, ".devlog")); err != nil {
		t.Fatalf("remove .devlog: %v", err)
	}
	t.Setenv("NO_COLOR", "1")
	t.Setenv("PATH", "") // ensure claude LookPath fails

	var buf bytes.Buffer
	code := writeStatus(root, &buf)
	got := buf.String()

	if code != 1 {
		t.Errorf("expected exit 1 with unhealthy items, got %d\nOutput:\n%s", code, got)
	}
	wantSubs := []string{
		"not initialized",
		"FAIL",
		"not found in PATH",
		".devlog:  FAIL",
		"run 'devlog init'",
	}
	for _, want := range wantSubs {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s\n---", want, got)
		}
	}
}

func TestStatus_NoGitRepo_GitHealthFails(t *testing.T) {
	// A plain temp dir (no .git) — git.CheckRepo will walk to filesystem
	// root without finding a repo and return the "no git repository found"
	// error. Health git line must be FAIL.
	root := t.TempDir()
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	code := writeStatus(root, &buf)
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(buf.String(), "git:      FAIL") {
		t.Errorf("expected git FAIL line:\n%s", buf.String())
	}
}

func TestStatus_NeverHadCompanion(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{
		SessionID: "xyz",
		StartedAt: "2026-04-22T22:00:00Z",
		// no LastCompanion, counts zero
	})
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	writeStatus(root, &buf)
	got := buf.String()
	if !strings.Contains(got, "Last companion: never") {
		t.Errorf("expected 'Last companion: never':\n%s", got)
	}
}

func TestStatus_NoColorStripsAnsiEscapes(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{SessionID: "x"})
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	writeStatus(root, &buf)
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("NO_COLOR=1 but output contains ANSI escapes:\n%q", buf.String())
	}
}

func TestStatus_ColorEmittedByDefault(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{SessionID: "x"})
	t.Setenv("NO_COLOR", "") // empty = colors on, per NO_COLOR spec

	var buf bytes.Buffer
	writeStatus(root, &buf)
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("NO_COLOR empty but no ANSI escapes present:\n%q", buf.String())
	}
}

func TestStatus_HelpFlag(t *testing.T) {
	oldOut := stdoutWriter
	oldErr := stderrWriter
	var out, errBuf bytes.Buffer
	stdoutWriter = &out
	stderrWriter = &errBuf
	t.Cleanup(func() {
		stdoutWriter = oldOut
		stderrWriter = oldErr
	})

	for _, arg := range []string{"-h", "--help", "help"} {
		out.Reset()
		errBuf.Reset()
		code := Status([]string{arg})
		if code != 0 {
			t.Errorf("%q: exit %d, want 0", arg, code)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%q: help output missing Usage section: %q", arg, out.String())
		}
	}
}

func TestHasHelpFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"-h"}, true},
		{[]string{"--help"}, true},
		{[]string{"help"}, true},
		{[]string{"status"}, false},
		{[]string{"foo", "-h"}, true},
	}
	for _, c := range cases {
		if got := hasHelpFlag(c.args); got != c.want {
			t.Errorf("hasHelpFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestPercentOf(t *testing.T) {
	cases := []struct {
		c    float64
		want int
	}{
		{0.0, 0},
		{0.5, 50},
		{0.85, 85},
		{0.999, 100},
		{1.0, 100},
		{-0.1, 0},
		{1.5, 100},
	}
	for _, c := range cases {
		if got := percentOf(c.c); got != c.want {
			t.Errorf("percentOf(%v) = %d, want %d", c.c, got, c.want)
		}
	}
}

func TestStatus_OpenCodeHost_ChecksOpenCodeCommand(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{SessionID: "oc1"})
	writeTestConfig(t, root, &state.Config{
		Host:        "opencode",
		HostCommand: "opencode",
	})
	t.Setenv("NO_COLOR", "1")
	t.Setenv("PATH", "") // opencode won't be found

	var buf bytes.Buffer
	code := writeStatus(root, &buf)
	got := buf.String()

	if code != 1 {
		t.Errorf("expected exit 1 (opencode not in PATH), got %d", code)
	}
	if !strings.Contains(got, "opencode:") {
		t.Errorf("expected host label 'opencode:', got:\n%s", got)
	}
	if !strings.Contains(got, "opencode not found in PATH") {
		t.Errorf("expected 'opencode not found in PATH', got:\n%s", got)
	}
}

func TestStatus_OpenCodeHost_OK(t *testing.T) {
	root := testutil.NewTempDevlogDir(t)
	writeTestState(t, root, &state.State{SessionID: "oc2"})
	writeTestConfig(t, root, &state.Config{
		Host:        "opencode",
		HostCommand: "opencode",
	})
	t.Setenv("NO_COLOR", "1")
	// Create a fake opencode binary on PATH
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	var buf bytes.Buffer
	writeStatus(root, &buf)
	got := buf.String()

	if !strings.Contains(got, "opencode:") {
		t.Errorf("expected host label 'opencode:', got:\n%s", got)
	}
	if !strings.Contains(got, "OK") {
		t.Errorf("expected OK for opencode, got:\n%s", got)
	}
}

// writeTestConfig writes cfg to <root>/.devlog/config.json.
func writeTestConfig(t *testing.T, root string, cfg *state.Config) {
	t.Helper()
	dir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// writeTestState writes s to <root>/.devlog/state.json, creating .devlog if
// needed. Fails the test on any error.
func writeTestState(t *testing.T, root string, s *state.State) {
	t.Helper()
	dir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}
