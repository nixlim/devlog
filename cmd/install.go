package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	derrors "devlog/internal/errors"
)

// hookEntry matches the shape SPEC.md defines under "Hook Configuration":
// each entry has a matcher (pattern string) and a command to run.
type hookEntry struct {
	Matcher string `json:"matcher"`
	Command string `json:"command"`
}

// desiredHooks is the set of entries `devlog install` must (idempotently)
// ensure are present in settings.json. Keyed by Claude Code hook kind.
// The ordering of entries within each kind is the order new entries are
// appended in — unrelated pre-existing entries keep their original slots.
var desiredHooks = map[string][]hookEntry{
	"UserPromptSubmit": {
		{Matcher: "", Command: "devlog task-capture"},
	},
	"PostToolUse": {
		{Matcher: "Edit|Write|Bash", Command: "devlog capture"},
		{Matcher: "TaskCreate|TaskUpdate", Command: "devlog task-tool-capture"},
	},
	"PreToolUse": {
		{Matcher: ".*", Command: "devlog check-feedback"},
	},
}

// Install implements `devlog install`. It writes the four DevLog hook
// entries into Claude Code's settings.json, creating the file (and its
// parent directory) if necessary. Running install repeatedly is a no-op:
// existing entries that exactly match a desired (matcher, command) pair
// are never duplicated.
//
// The settings location resolution order is:
//  1. --settings flag, if provided
//  2. CLAUDE_SETTINGS_PATH environment variable, if set
//  3. $HOME/.claude/settings.json (Claude Code's user-scoped default)
func Install(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr())
	settingsFlag := fs.String("settings", "", "path to Claude Code settings.json (overrides CLAUDE_SETTINGS_PATH)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path, err := resolveSettingsPath(*settingsFlag)
	if err != nil {
		printErr(err)
		return 1
	}

	if err := installHooks(path); err != nil {
		printErr(err)
		return 1
	}

	fmt.Fprintf(stdout(), "devlog: installed hooks into %s\n", path)
	return 0
}

// resolveSettingsPath picks the settings.json location from the explicit
// flag, the env var, or the user-scoped default. Returns an absolute path.
func resolveSettingsPath(flagVal string) (string, error) {
	var path string
	switch {
	case flagVal != "":
		path = flagVal
	case os.Getenv("CLAUDE_SETTINGS_PATH") != "":
		path = os.Getenv("CLAUDE_SETTINGS_PATH")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", derrors.Wrap("install", "resolve $HOME", err).
				WithRemediation(
					"Set CLAUDE_SETTINGS_PATH or use --settings to point at\n" +
						"your Claude Code settings.json explicitly.",
				)
		}
		path = filepath.Join(home, ".claude", "settings.json")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", derrors.Wrap("install",
			fmt.Sprintf("resolve absolute path for %s", path), err)
	}
	return abs, nil
}

// installHooks loads (or initialises) the settings file at path, merges
// in every desired DevLog hook entry that isn't already present, and
// writes the result atomically.
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

// loadSettings reads path and returns its parsed JSON object. A missing
// file yields an empty map (install will create the file). A corrupt
// file is surfaced to the caller so they don't silently overwrite an
// operator's hand-edits.
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

// extractHooksMap returns the "hooks" object from settings, coercing any
// present-but-wrong-typed value into an error rather than silently
// overwriting it. A missing "hooks" key yields an empty map.
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

// mergeDesiredHooks walks desiredHooks and ensures every entry is present
// in hooks. Pre-existing entries for each hook kind are preserved in
// place; only missing (matcher, command) pairs are appended.
//
// Entries whose shape we don't understand (e.g. Claude Code's nested
// format) are treated as opaque and left untouched — DevLog may add a
// duplicate-looking entry for its own flat schema, but that's preferable
// to clobbering an operator's intentional override.
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

// asEntrySlice coerces the value at hooks[kind] into a slice suitable for
// append. Anything we can't interpret as an array is treated as "start
// fresh" — but mergeDesiredHooks only overwrites when the result changes,
// so this path never destroys data that extractHooksMap has validated.
func asEntrySlice(v any) []any {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

// hookAlreadyPresent reports whether an entry with the same matcher and
// command as want already lives in entries. Comparison is strict on both
// fields so an existing, but different, command for the same matcher
// still triggers an append — operators may intentionally stack multiple
// hooks per matcher.
func hookAlreadyPresent(entries []any, want hookEntry) bool {
	for _, e := range entries {
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if asString(obj["matcher"]) == want.Matcher &&
			asString(obj["command"]) == want.Command {
			return true
		}
	}
	return false
}

// asString returns v as a string when v is a string, else the empty
// string. Used for defensive comparison against JSON-decoded maps where
// numeric or boolean matcher values would otherwise panic.
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// writeSettings writes settings to path atomically (temp file + rename).
// The parent directory is created if missing so `devlog install` works
// on a fresh machine where ~/.claude/ doesn't exist yet.
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
