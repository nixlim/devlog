package cmd

import (
	"flag"
	"fmt"
	"os"
	"strings"

	derrors "devlog/internal/errors"
)

// Uninstall implements `devlog uninstall` — remove DevLog hook entries
// from Claude Code's settings.json.
//
// A "DevLog hook entry" is any entry object whose "command" field, after
// trimming leading whitespace, begins with "devlog " (or is exactly
// "devlog"). Unrelated hooks are preserved in place.
//
// Running uninstall is idempotent: when the settings file is absent or
// already clean, it is a successful no-op.
//
// Settings path resolution mirrors `devlog install`:
//  1. --settings flag, if provided
//  2. CLAUDE_SETTINGS_PATH environment variable, if set
//  3. $HOME/.claude/settings.json (the user-scoped default)
func Uninstall(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), uninstallUsage)
		return 0
	}

	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
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

	removed, err := uninstallHooks(path)
	if err != nil {
		printErr(err)
		return 1
	}

	if removed == 0 {
		fmt.Fprintf(stdout(), "devlog: no devlog hooks found in %s\n", path)
		return 0
	}
	fmt.Fprintf(stdout(), "devlog: removed %d devlog hook entr%s from %s\n",
		removed, plural(removed, "y", "ies"), path)
	return 0
}

const uninstallUsage = `devlog uninstall — remove DevLog hook entries from Claude Code settings.json

Usage:
    devlog uninstall [--settings PATH]

Flags:
    --settings   path to Claude Code settings.json (overrides CLAUDE_SETTINGS_PATH).

Only hook entries whose command begins with "devlog " are removed; unrelated
hooks are preserved. Re-running on a clean file is a no-op.
`

// uninstallHooks removes every DevLog hook entry from the settings file at
// path and writes the result back. Returns the count removed. A missing
// settings file is treated as "nothing to do" and returns (0, nil).
func uninstallHooks(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, derrors.Wrap("uninstall", fmt.Sprintf("stat %s", path), err)
	}

	// The load/extract/write helpers live in install.go and use "install" as
	// their internal error component. That's acceptable here: the inner
	// error surfaces the concrete cause (parse, read, write) and callers
	// printing DevlogError show the four-part message either way. We don't
	// rebrand because duplicating the helpers purely for cosmetics would
	// invite drift.
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

// filterDevlogHooks strips any entry whose command is a devlog invocation
// from every hook-kind array in hooks. Arrays that become empty remain
// present (as []any{}) so the file structure is preserved for other
// tooling that may expect those keys.
//
// Mutates hooks in place; returns the number of entries removed.
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
			if isDevlogCommand(asString(obj["command"])) {
				removed++
				continue
			}
			kept = append(kept, entry)
		}
		hooks[kind] = kept
	}
	return removed
}

// isDevlogCommand reports whether cmd is a devlog CLI invocation. We
// tolerate leading whitespace and accept both "devlog" bare and any
// "devlog <subcommand>" form; tab-separated commands work too.
func isDevlogCommand(cmd string) bool {
	trimmed := strings.TrimLeft(cmd, " \t")
	if trimmed == "devlog" {
		return true
	}
	return strings.HasPrefix(trimmed, "devlog ") || strings.HasPrefix(trimmed, "devlog\t")
}

// plural picks between a singular and plural word form based on n.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
