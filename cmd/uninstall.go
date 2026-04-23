package cmd

import (
	"flag"
	"fmt"
	"path/filepath"

	derrors "devlog/internal/errors"
	"devlog/internal/host"
	"devlog/internal/state"
)

// Uninstall implements `devlog uninstall` — remove DevLog hooks for the
// configured host backend.
//
// Host resolution:
//  1. --host flag, if provided.
//  2. host field from .devlog/config.json (set during install).
//  3. Default to "claude" for backward compatibility.
func Uninstall(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), uninstallUsage)
		return 0
	}

	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr())
	hostFlag := fs.String("host", "", "host backend: claude or opencode (read from config when empty)")
	settingsFlag := fs.String("settings", "", "path to Claude Code settings.json (overrides CLAUDE_SETTINGS_PATH)")
	pluginDirFlag := fs.String("plugin-dir", "", "OpenCode plugin directory (default .opencode/plugins)")
	configFileFlag := fs.String("opencode-config", "", "OpenCode config path (default opencode.json)")
	projectFlag := fs.String("project", "", "project root (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	hostName := *hostFlag
	if hostName == "" {
		hostName = resolveUninstallHost(*projectFlag)
	}

	h, ok := host.Lookup(hostName)
	if !ok {
		printErr(derrors.New("uninstall",
			fmt.Sprintf("unknown host %q", hostName)).
			WithRemediation(
				"Supported hosts: claude, opencode.\n"+
					"Run `devlog uninstall --host claude` or `--host opencode`.\n",
			))
		return 1
	}

	opts := host.InstallOpts{
		PluginDir:  *pluginDirFlag,
		ConfigPath: *configFileFlag,
	}
	if hostName == "claude" {
		path, err := resolveSettingsPath(*settingsFlag)
		if err != nil {
			printErr(err)
			return 1
		}
		opts.SettingsPath = path
	}

	if err := h.Uninstall(opts); err != nil {
		printErr(derrors.Wrap("uninstall", fmt.Sprintf("%s uninstall", hostName), err))
		return 1
	}
	fmt.Fprintf(stdout(), "devlog: uninstalled %s hooks\n", hostName)
	return 0
}

const uninstallUsage = `devlog uninstall — remove DevLog hooks for the configured host

Usage:
    devlog uninstall [--host claude|opencode] [--settings PATH]
                     [--plugin-dir DIR] [--opencode-config PATH]

Flags:
    --host             host backend (read from .devlog/config.json when empty)
    --settings         path to Claude Code settings.json (overrides CLAUDE_SETTINGS_PATH)
    --plugin-dir       OpenCode plugin directory (default .opencode/plugins)
    --opencode-config  OpenCode config path (default opencode.json)
    --project          project root (defaults to cwd)

Only hook entries whose command begins with "devlog " are removed; unrelated
hooks are preserved. Re-running on a clean file is a no-op.
`

func resolveUninstallHost(projectRoot string) string {
	root, err := resolveProjectRoot(projectRoot)
	if err != nil {
		return "claude"
	}
	devlogDir := findDevlogDir(root)
	cfg, err := state.LoadConfig(filepath.Join(devlogDir, "config.json"))
	if err != nil {
		return "claude"
	}
	if cfg.Host != "" {
		return cfg.Host
	}
	return "claude"
}

// plural picks between a singular and plural word form based on n.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
