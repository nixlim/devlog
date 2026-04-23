package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"devlog/internal/state"
)

// readSettings loads and JSON-decodes path; fails the test on any error.
func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

// hookArr returns settings.hooks[kind] as a []any, failing on type drift.
func hookArr(t *testing.T, settings map[string]any, kind string) []any {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("settings.hooks is not an object: %T", settings["hooks"])
	}
	arr, ok := hooks[kind].([]any)
	if !ok {
		t.Fatalf("settings.hooks.%s is not an array: %T", kind, hooks[kind])
	}
	return arr
}

func hasEntry(arr []any, matcher, command string) bool {
	for _, e := range arr {
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if asString(obj["matcher"]) != matcher {
			continue
		}
		hooksArr, ok := obj["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooksArr {
			hobj, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if asString(hobj["type"]) == "command" && asString(hobj["command"]) == command {
				return true
			}
		}
	}
	return false
}

func TestInstallCreatesFileWhenMissing(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "nested", "sub", "settings.json")

	withStreams(t)
	code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath})
	if code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}

	s := readSettings(t, settingsPath)
	if !hasEntry(hookArr(t, s, "UserPromptSubmit"), "", "devlog task-capture") {
		t.Error("UserPromptSubmit hook missing after fresh install")
	}
	if !hasEntry(hookArr(t, s, "PostToolUse"), "Edit|Write|Bash", "devlog capture") {
		t.Error("PostToolUse capture hook missing")
	}
	if !hasEntry(hookArr(t, s, "PostToolUse"), "TaskCreate|TaskUpdate", "devlog task-tool-capture") {
		t.Error("PostToolUse task-tool-capture hook missing")
	}
	if !hasEntry(hookArr(t, s, "PreToolUse"), ".*", "devlog check-feedback") {
		t.Error("PreToolUse check-feedback hook missing")
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("first install exit = %d", code)
	}
	first := readSettings(t, settingsPath)

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("second install exit = %d", code)
	}
	second := readSettings(t, settingsPath)

	for kind, want := range map[string]int{
		"UserPromptSubmit": 1,
		"PostToolUse":      2,
		"PreToolUse":       1,
	} {
		firstCount := len(hookArr(t, first, kind))
		secondCount := len(hookArr(t, second, kind))
		if firstCount != want {
			t.Errorf("first run: %s entries = %d, want %d", kind, firstCount, want)
		}
		if secondCount != want {
			t.Errorf("second run: %s entries = %d, want %d (duplicates introduced)",
				kind, secondCount, want)
		}
	}
}

func TestInstallPreservesExistingUnrelatedHooks(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")

	existing := map[string]any{
		"theme": "dark",
		"model": "claude-opus-4-7",
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "other-tool stuff"},
					},
				},
			},
			"Stop": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "notify-done"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("Install exit = %d", code)
	}

	s := readSettings(t, settingsPath)

	// Top-level unrelated keys preserved.
	if s["theme"] != "dark" {
		t.Errorf("theme clobbered: %v", s["theme"])
	}
	if s["model"] != "claude-opus-4-7" {
		t.Errorf("model clobbered: %v", s["model"])
	}

	// Pre-existing UserPromptSubmit entry preserved.
	ups := hookArr(t, s, "UserPromptSubmit")
	if !hasEntry(ups, "", "other-tool stuff") {
		t.Error("existing UserPromptSubmit entry dropped")
	}
	// DevLog's entry added alongside.
	if !hasEntry(ups, "", "devlog task-capture") {
		t.Error("devlog task-capture entry missing")
	}
	if len(ups) != 2 {
		t.Errorf("UserPromptSubmit should have 2 entries, got %d", len(ups))
	}

	// Unrelated Stop hook preserved untouched.
	stopArr, ok := s["hooks"].(map[string]any)["Stop"].([]any)
	if !ok {
		t.Fatalf("Stop hooks missing or wrong type")
	}
	if !hasEntry(stopArr, "", "notify-done") {
		t.Error("existing Stop entry dropped")
	}
}

func TestInstallRespectsEnvVar(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "env-settings.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", settingsPath)
	// Also un-set HOME so a stray default can't mask a regression.
	t.Setenv("HOME", root)

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root}); code != 0 {
		t.Fatalf("Install exit = %d", code)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Errorf("expected %s to be created via env var: %v", settingsPath, err)
	}
}

func TestInstallFlagBeatsEnvVar(t *testing.T) {
	root := t.TempDir()
	envPath := filepath.Join(root, "env-path.json")
	flagPath := filepath.Join(root, "flag-path.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", envPath)

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", flagPath}); code != 0 {
		t.Fatalf("Install exit = %d", code)
	}

	if _, err := os.Stat(flagPath); err != nil {
		t.Errorf("--settings target missing: %v", err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env var target should not have been written: %v", err)
	}
}

func TestInstallRejectsCorruptSettings(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	withStreams(t)
	code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath})
	if code == 0 {
		t.Error("Install should fail on corrupt JSON")
	}
	// File should be unchanged (atomic rename never happened).
	data, err := os.ReadFile(settingsPath)
	if err != nil || string(data) != "{broken" {
		t.Errorf("settings file mutated after failed install: %q (err=%v)", data, err)
	}
}

func TestInstallRejectsNonObjectHooks(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	// hooks is an array, not an object — we should refuse to write.
	if err := os.WriteFile(settingsPath,
		[]byte(`{"hooks": ["weird"]}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	withStreams(t)
	code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath})
	if code == 0 {
		t.Error("Install should fail when hooks is not an object")
	}
}

// TestInstallWritesCorrectClaudeCodeSchema verifies that hook entries use
// the nested format Claude Code requires:
//
//	{"matcher": "...", "hooks": [{"type": "command", "command": "..."}]}
//
// NOT the flat format:
//
//	{"matcher": "...", "command": "..."}
func TestInstallWritesCorrectClaudeCodeSchema(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")

	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}

	s := readSettings(t, settingsPath)

	for _, tc := range []struct {
		kind    string
		matcher string
		command string
	}{
		{"PostToolUse", "Edit|Write|Bash", "devlog capture"},
		{"PostToolUse", "TaskCreate|TaskUpdate", "devlog task-tool-capture"},
		{"PreToolUse", ".*", "devlog check-feedback"},
		{"UserPromptSubmit", "", "devlog task-capture"},
	} {
		arr := hookArr(t, s, tc.kind)
		found := false
		for _, e := range arr {
			obj, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if obj["matcher"] != tc.matcher {
				continue
			}
			// Must NOT have a top-level "command" key.
			if _, hasCmd := obj["command"]; hasCmd {
				t.Errorf("%s[matcher=%q]: has top-level 'command' field — "+
					"Claude Code requires hooks wrapped in a 'hooks' array",
					tc.kind, tc.matcher)
			}
			// Must have a "hooks" array with the command inside.
			hooksArr, ok := obj["hooks"].([]any)
			if !ok {
				t.Errorf("%s[matcher=%q]: missing 'hooks' array", tc.kind, tc.matcher)
				continue
			}
			for _, h := range hooksArr {
				hobj, ok := h.(map[string]any)
				if !ok {
					continue
				}
				if hobj["type"] == "command" && hobj["command"] == tc.command {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s[matcher=%q]: expected hook with command %q not found in correct nested format",
				tc.kind, tc.matcher, tc.command)
		}
	}
}

// writeFakeBinary creates a trivial executable in dir named `name` that
// prints "v1.0.0\n" and exits 0. Tests point PATH at dir to make the
// host.Detect() lookup succeed without a real binary.
func writeFakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary technique is posix-only")
	}
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\necho v1.0.0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readInstalledConfig(t *testing.T, projectRoot string) *state.Config {
	t.Helper()
	cfg, err := state.LoadConfig(filepath.Join(projectRoot, ".devlog", "config.json"))
	if err != nil {
		t.Fatalf("load installed config: %v", err)
	}
	return cfg
}

func TestInstallAutoDetectClaudeOnly(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "claude")
	t.Setenv("PATH", binDir)

	settingsPath := filepath.Join(root, "settings.json")
	stdoutBuf, _ := withStreams(t)
	if code := Install([]string{"--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}
	if !strings.Contains(stdoutBuf.String(), "detected claude") {
		t.Errorf("stdout should report auto-detected claude: %q", stdoutBuf.String())
	}
	cfg := readInstalledConfig(t, root)
	if cfg.Host != "claude" {
		t.Errorf("config.Host = %q, want claude", cfg.Host)
	}
}

func TestInstallAutoDetectOpenCodeOnly(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "opencode")
	t.Setenv("PATH", binDir)

	stdoutBuf, _ := withStreams(t)
	if code := Install([]string{
		"--project", root,
		"--plugin-dir", filepath.Join(root, "plugins"),
		"--opencode-config", filepath.Join(root, "opencode.json"),
	}); code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}
	if !strings.Contains(stdoutBuf.String(), "detected opencode") {
		t.Errorf("stdout should report auto-detected opencode: %q", stdoutBuf.String())
	}
	cfg := readInstalledConfig(t, root)
	if cfg.Host != "opencode" {
		t.Errorf("config.Host = %q, want opencode", cfg.Host)
	}
	// Plugin file written.
	if _, err := os.Stat(filepath.Join(root, "plugins", "devlog.ts")); err != nil {
		t.Errorf("plugin file missing: %v", err)
	}
}

func TestInstallOpenCodeSetsHostCommand(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "opencode")
	t.Setenv("PATH", binDir)

	withStreams(t)
	code := Install([]string{
		"--host", "opencode",
		"--project", root,
		"--plugin-dir", filepath.Join(root, "plugins"),
		"--opencode-config", filepath.Join(root, "opencode.json"),
	})
	if code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}
	cfg := readInstalledConfig(t, root)
	if cfg.HostCommand != "opencode" {
		t.Errorf("HostCommand = %q, want opencode", cfg.HostCommand)
	}
}

func TestInstallBothDetectedPrefersClaude(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	writeFakeBinary(t, binDir, "claude")
	writeFakeBinary(t, binDir, "opencode")
	t.Setenv("PATH", binDir)

	settingsPath := filepath.Join(root, "settings.json")
	stdoutBuf, _ := withStreams(t)
	if code := Install([]string{"--project", root, "--settings", settingsPath}); code != 0 {
		t.Fatalf("Install exit = %d, want 0", code)
	}
	out := stdoutBuf.String()
	if !strings.Contains(out, "detected both") {
		t.Errorf("stdout should mention both hosts detected: %q", out)
	}
	if !strings.Contains(out, "--host opencode") {
		t.Errorf("stdout should hint at --host opencode: %q", out)
	}
	cfg := readInstalledConfig(t, root)
	if cfg.Host != "claude" {
		t.Errorf("config.Host = %q, want claude (backward-compat default)", cfg.Host)
	}
}

func TestInstallNeitherDetected(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("PATH", binDir) // empty dir — nothing to detect

	_, stderrBuf := withStreams(t)
	code := Install([]string{"--project", root, "--settings", filepath.Join(root, "settings.json")})
	if code == 0 {
		t.Fatalf("Install should fail when no host is detected")
	}
	errOut := stderrBuf.String()
	for _, want := range []string{"Claude Code", "OpenCode", "--host"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q: %q", want, errOut)
		}
	}
}

func TestInstallFlagsPersistModelOverrides(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	withStreams(t)
	code := Install([]string{
		"--host", "claude",
		"--project", root,
		"--settings", settingsPath,
		"--summarizer-model", "claude-haiku-test",
		"--companion-model", "claude-sonnet-test",
		"--host-command", "/opt/bin/claude",
	})
	if code != 0 {
		t.Fatalf("Install exit = %d", code)
	}
	cfg := readInstalledConfig(t, root)
	if cfg.SummarizerModel != "claude-haiku-test" {
		t.Errorf("SummarizerModel = %q, want claude-haiku-test", cfg.SummarizerModel)
	}
	if cfg.CompanionModel != "claude-sonnet-test" {
		t.Errorf("CompanionModel = %q, want claude-sonnet-test", cfg.CompanionModel)
	}
	if cfg.HostCommand != "/opt/bin/claude" {
		t.Errorf("HostCommand = %q, want /opt/bin/claude", cfg.HostCommand)
	}
}

func TestInstallClaudeCommandAlias(t *testing.T) {
	// --claude-command is a deprecated alias for --host-command. Providing
	// only --claude-command should populate HostCommand.
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	withStreams(t)
	code := Install([]string{
		"--host", "claude",
		"--project", root,
		"--settings", settingsPath,
		"--claude-command", "/usr/local/bin/claude",
	})
	if code != 0 {
		t.Fatalf("Install exit = %d", code)
	}
	cfg := readInstalledConfig(t, root)
	if cfg.HostCommand != "/usr/local/bin/claude" {
		t.Errorf("HostCommand = %q, want the --claude-command value", cfg.HostCommand)
	}
}

func TestInstallUnknownHostRejected(t *testing.T) {
	root := t.TempDir()
	_, stderrBuf := withStreams(t)
	code := Install([]string{"--host", "bogus", "--project", root})
	if code == 0 {
		t.Fatal("Install should reject unknown host")
	}
	if !strings.Contains(stderrBuf.String(), "bogus") {
		t.Errorf("stderr should mention the bad host name: %q", stderrBuf.String())
	}
}

func TestInstallEmptyFileTreatedAsEmptyObject(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "settings.json")
	// A zero-byte settings.json should not be treated as a JSON error.
	if err := os.WriteFile(settingsPath, nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	withStreams(t)
	if code := Install([]string{"--host", "claude", "--project", root, "--settings", settingsPath}); code != 0 {
		t.Errorf("empty-file install exit = %d, want 0", code)
	}
	s := readSettings(t, settingsPath)
	if _, ok := s["hooks"].(map[string]any); !ok {
		t.Error("hooks block should be populated")
	}
}
