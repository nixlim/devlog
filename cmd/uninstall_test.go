package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSettingsJSON marshals v to path. Helper for uninstall tests.
func writeSettingsJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readSettingsJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return out
}

func TestUninstall_MissingSettings_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json") // does not exist
	setupCmdStreams(t)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uninstall created a settings file: %v", err)
	}
}

func TestUninstall_OnlyDevlogHooks_AllRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{"matcher": "", "command": "devlog task-capture"},
			},
			"PostToolUse": []any{
				map[string]any{"matcher": "Edit|Write|Bash", "command": "devlog capture"},
				map[string]any{"matcher": "TaskCreate|TaskUpdate", "command": "devlog task-tool-capture"},
			},
			"PreToolUse": []any{
				map[string]any{"matcher": ".*", "command": "devlog check-feedback"},
			},
		},
	})
	setupCmdStreams(t)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	got := readSettingsJSON(t, path)
	hooks := got["hooks"].(map[string]any)
	for _, kind := range []string{"UserPromptSubmit", "PostToolUse", "PreToolUse"} {
		arr := hooks[kind].([]any)
		if len(arr) != 0 {
			t.Errorf("%s should be empty after uninstall, got %v", kind, arr)
		}
	}
}

func TestUninstall_MixedHooks_OnlyDevlogRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{"matcher": "", "command": "my-tool log-prompt"},
				map[string]any{"matcher": "", "command": "devlog task-capture"},
			},
			"PostToolUse": []any{
				map[string]any{"matcher": "Edit", "command": "other-tool"},
			},
			"PreToolUse": []any{
				map[string]any{"matcher": ".*", "command": "devlog check-feedback"},
				map[string]any{"matcher": ".*", "command": "linter"},
			},
		},
		"other_setting": "preserved",
	})
	setupCmdStreams(t)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	got := readSettingsJSON(t, path)

	if v, ok := got["other_setting"]; !ok || v != "preserved" {
		t.Errorf("other_setting lost: got %v", got)
	}

	hooks := got["hooks"].(map[string]any)
	ups := hooks["UserPromptSubmit"].([]any)
	if len(ups) != 1 || ups[0].(map[string]any)["command"] != "my-tool log-prompt" {
		t.Errorf("UserPromptSubmit mangled: %v", ups)
	}
	post := hooks["PostToolUse"].([]any)
	if len(post) != 1 || post[0].(map[string]any)["command"] != "other-tool" {
		t.Errorf("PostToolUse mangled: %v", post)
	}
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 || pre[0].(map[string]any)["command"] != "linter" {
		t.Errorf("PreToolUse mangled: %v", pre)
	}
}

func TestUninstall_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsJSON(t, path, map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": ".*", "command": "devlog check-feedback"},
				map[string]any{"matcher": ".*", "command": "linter"},
			},
		},
	})
	setupCmdStreams(t)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("first uninstall exit: %d", code)
	}
	first := readSettingsJSON(t, path)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("second uninstall exit: %d", code)
	}
	second := readSettingsJSON(t, path)

	firstData, _ := json.Marshal(first)
	secondData, _ := json.Marshal(second)
	if string(firstData) != string(secondData) {
		t.Errorf("idempotency broken:\nfirst=%s\nsecond=%s", firstData, secondData)
	}
}

func TestUninstall_NoHooksKey_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeSettingsJSON(t, path, map[string]any{
		"theme":           "dark",
		"model":           "claude-sonnet-4-6",
		"some_other_list": []any{"a", "b", "c"},
	})
	setupCmdStreams(t)

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("exit: %d", code)
	}
	got := readSettingsJSON(t, path)
	if got["theme"] != "dark" || got["model"] != "claude-sonnet-4-6" {
		t.Errorf("unrelated settings lost: %v", got)
	}
}

func TestUninstall_InstallThenUninstall_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	setupCmdStreams(t)

	if code := Install([]string{"--host", "claude", "--project", dir, "--settings", path}); code != 0 {
		t.Fatalf("install exit: %d", code)
	}
	afterInstall := readSettingsJSON(t, path)
	hooks := afterInstall["hooks"].(map[string]any)
	total := 0
	for _, raw := range hooks {
		total += len(raw.([]any))
	}
	if total != 4 {
		t.Fatalf("install should have added 4 entries, got %d: %+v", total, hooks)
	}

	if code := Uninstall([]string{"--host", "claude", "--settings", path}); code != 0 {
		t.Fatalf("uninstall exit: %d", code)
	}
	afterUninstall := readSettingsJSON(t, path)
	h2 := afterUninstall["hooks"].(map[string]any)
	total2 := 0
	for _, raw := range h2 {
		total2 += len(raw.([]any))
	}
	if total2 != 0 {
		t.Errorf("uninstall should have removed all 4 entries, %d remain: %+v", total2, h2)
	}
}

func TestUninstall_HelpFlag(t *testing.T) {
	stdoutBuf, _ := setupCmdStreams(t)
	for _, arg := range []string{"-h", "--help", "help"} {
		stdoutBuf.Reset()
		if code := Uninstall([]string{arg}); code != 0 {
			t.Errorf("%q: exit %d, want 0", arg, code)
		}
		if !strings.Contains(stdoutBuf.String(), "Usage:") {
			t.Errorf("%q: help missing Usage section: %q", arg, stdoutBuf.String())
		}
	}
}
