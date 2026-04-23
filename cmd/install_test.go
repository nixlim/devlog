package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
		if obj["matcher"] == matcher && obj["command"] == command {
			return true
		}
	}
	return false
}

func TestInstallCreatesFileWhenMissing(t *testing.T) {
	root := t.TempDir()
	settingsPath := filepath.Join(root, "nested", "sub", "settings.json")

	withStreams(t)
	code := Install([]string{"--settings", settingsPath})
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
	if code := Install([]string{"--settings", settingsPath}); code != 0 {
		t.Fatalf("first install exit = %d", code)
	}
	first := readSettings(t, settingsPath)

	withStreams(t)
	if code := Install([]string{"--settings", settingsPath}); code != 0 {
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
				map[string]any{"matcher": "", "command": "other-tool stuff"},
			},
			"Stop": []any{
				map[string]any{"matcher": "", "command": "notify-done"},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	withStreams(t)
	if code := Install([]string{"--settings", settingsPath}); code != 0 {
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
	if code := Install(nil); code != 0 {
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
	if code := Install([]string{"--settings", flagPath}); code != 0 {
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
	code := Install([]string{"--settings", settingsPath})
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
	code := Install([]string{"--settings", settingsPath})
	if code == 0 {
		t.Error("Install should fail when hooks is not an object")
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
	if code := Install([]string{"--settings", settingsPath}); code != 0 {
		t.Errorf("empty-file install exit = %d, want 0", code)
	}
	s := readSettings(t, settingsPath)
	if _, ok := s["hooks"].(map[string]any); !ok {
		t.Error("hooks block should be populated")
	}
}
