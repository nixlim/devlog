package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	derrors "devlog/internal/errors"
)

type hookInner struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookEntry struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookInner `json:"hooks"`
}

var desiredHooks = map[string][]hookEntry{
	"UserPromptSubmit": {
		{Matcher: "", Hooks: []hookInner{{Type: "command", Command: "devlog task-capture"}}},
	},
	"PostToolUse": {
		{Matcher: "Edit|Write|Bash", Hooks: []hookInner{{Type: "command", Command: "devlog capture"}}},
		{Matcher: "TaskCreate|TaskUpdate", Hooks: []hookInner{{Type: "command", Command: "devlog task-tool-capture"}}},
	},
	"PreToolUse": {
		{Matcher: ".*", Hooks: []hookInner{{Type: "command", Command: "devlog check-feedback"}}},
	},
}

func installHooks(path string) error {
	settings, err := loadSettings(path)
	if err != nil {
		return err
	}

	hooks, err := extractHooksMap(settings)
	if err != nil {
		return err
	}

	mergeDesiredHooks(hooks)

	settings["hooks"] = hooks
	return writeSettings(path, settings)
}

func loadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, derrors.Wrap("install",
			fmt.Sprintf("read %s", path), err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, derrors.Wrap("install",
			fmt.Sprintf("parse %s", path), err).
			WithRemediation(
				"The settings file is not valid JSON. Back it up and re-run\n" +
					"`devlog install`, or fix the syntax manually.",
			)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func extractHooksMap(settings map[string]any) (map[string]any, error) {
	raw, ok := settings["hooks"]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, derrors.New("install",
			"settings.json has a 'hooks' field that is not an object").
			WithRemediation(
				"DevLog expects settings.hooks to be a JSON object keyed by\n" +
					"hook kind (UserPromptSubmit, PostToolUse, PreToolUse).\n" +
					"Fix the file manually and re-run `devlog install`.",
			)
	}
	return m, nil
}

func mergeDesiredHooks(hooks map[string]any) {
	for kind, want := range desiredHooks {
		existing := asEntrySlice(hooks[kind])
		for _, entry := range want {
			if hookAlreadyPresent(existing, entry) {
				continue
			}
			existing = append(existing, entry)
		}
		hooks[kind] = existing
	}
}

func asEntrySlice(v any) []any {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func hookAlreadyPresent(entries []any, want hookEntry) bool {
	for _, e := range entries {
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if asString(obj["matcher"]) != want.Matcher {
			continue
		}
		hooksRaw, ok := obj["hooks"].([]any)
		if !ok {
			continue
		}
		if innerCommandsMatch(hooksRaw, want.Hooks) {
			return true
		}
	}
	return false
}

func innerCommandsMatch(existing []any, want []hookInner) bool {
	if len(want) == 0 {
		return false
	}
	for _, w := range want {
		found := false
		for _, e := range existing {
			obj, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if asString(obj["type"]) == w.Type && asString(obj["command"]) == w.Command {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func writeSettings(path string, settings map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return derrors.Wrap("install",
			fmt.Sprintf("create %s", dir), err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return derrors.Wrap("install", "encode settings", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return derrors.Wrap("install",
			fmt.Sprintf("create temp in %s", dir), err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return derrors.Wrap("install",
			fmt.Sprintf("write %s", tmpPath), err)
	}
	if err := tmp.Close(); err != nil {
		return derrors.Wrap("install",
			fmt.Sprintf("close %s", tmpPath), err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return derrors.Wrap("install",
			fmt.Sprintf("rename %s -> %s", tmpPath, path), err)
	}
	committed = true
	return nil
}

func uninstallHooks(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}

	settings, err := loadSettings(path)
	if err != nil {
		return 0, err
	}
	hooks, err := extractHooksMap(settings)
	if err != nil {
		return 0, err
	}

	removed := filterDevlogHooks(hooks)
	if removed == 0 {
		return 0, nil
	}

	settings["hooks"] = hooks
	if err := writeSettings(path, settings); err != nil {
		return 0, err
	}
	return removed, nil
}

func filterDevlogHooks(hooks map[string]any) int {
	removed := 0
	for kind, raw := range hooks {
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, entry := range arr {
			obj, ok := entry.(map[string]any)
			if !ok {
				kept = append(kept, entry)
				continue
			}
			if entryIsDevlog(obj) {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		hooks[kind] = kept
	}
	return removed
}

func entryIsDevlog(obj map[string]any) bool {
	if hooksArr, ok := obj["hooks"].([]any); ok {
		for _, h := range hooksArr {
			hobj, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if isDevlogCommand(asString(hobj["command"])) {
				return true
			}
		}
	}
	if isDevlogCommand(asString(obj["command"])) {
		return true
	}
	return false
}

func isDevlogCommand(cmd string) bool {
	trimmed := strings.TrimLeft(cmd, " \t")
	if trimmed == "devlog" {
		return true
	}
	return strings.HasPrefix(trimmed, "devlog ") || strings.HasPrefix(trimmed, "devlog\t")
}
