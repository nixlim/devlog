package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	derrors "devlog/internal/errors"
	"devlog/internal/host"
	"devlog/internal/state"
)

// Install implements `devlog install`. It installs DevLog hooks for the
// selected host backend (Claude Code or OpenCode) and persists the chosen
// host plus optional model overrides to .devlog/config.json.
//
// Host selection order:
//  1. --host claude|opencode, if given.
//  2. Auto-detect: call Detect() on every registered host; when exactly one
//     is found, use it; when both are found, default to claude and print
//     a hint about --host opencode for backward compatibility.
//  3. If neither host is detected, fail with install links.
//
// For the claude path, the settings.json resolution from earlier versions
// of this command is preserved:
//  1. --settings flag
//  2. CLAUDE_SETTINGS_PATH env var
//  3. $HOME/.claude/settings.json
func Install(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr())
	hostFlag := fs.String("host", "", "host backend: claude or opencode (auto-detected when empty)")
	summarizerModel := fs.String("summarizer-model", "", "summarizer model id (overrides config default)")
	companionModel := fs.String("companion-model", "", "companion model id (overrides config default)")
	hostCommand := fs.String("host-command", "", "path to host CLI binary (overrides default)")
	claudeCommand := fs.String("claude-command", "", "deprecated alias for --host-command")
	settingsFlag := fs.String("settings", "", "path to Claude Code settings.json (overrides CLAUDE_SETTINGS_PATH)")
	pluginDirFlag := fs.String("plugin-dir", "", "OpenCode plugin directory (default .opencode/plugins)")
	configFileFlag := fs.String("opencode-config", "", "OpenCode config path (default opencode.json)")
	projectFlag := fs.String("project", "", "project root (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// --claude-command is a backward-compat alias; --host-command wins.
	resolvedHostCommand := *hostCommand
	if resolvedHostCommand == "" && *claudeCommand != "" {
		resolvedHostCommand = *claudeCommand
	}

	hostName := *hostFlag
	if hostName == "" {
		detected, err := autoDetectHost()
		if err != nil {
			printErr(err)
			return 1
		}
		hostName = detected
	}

	h, ok := host.Lookup(hostName)
	if !ok {
		printErr(derrors.New("install",
			fmt.Sprintf("unknown host %q", hostName)).
			WithRemediation(
				"Supported hosts: claude, opencode.\n" +
					"Run `devlog install --host claude` or `--host opencode`.\n",
			))
		return 1
	}
	if c, ok := h.(host.Configurable); ok && resolvedHostCommand != "" {
		c.SetCommand(resolvedHostCommand)
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
	if err := h.Install(opts); err != nil {
		printErr(derrors.Wrap("install", fmt.Sprintf("%s install", hostName), err))
		return 1
	}
	fmt.Fprintf(stdout(), "devlog: installed %s hooks\n", hostName)

	if err := persistInstallConfig(*projectFlag, hostName, resolvedHostCommand, *summarizerModel, *companionModel); err != nil {
		printErr(err)
		return 1
	}

	return 0
}

// autoDetectHost probes every registered host's Detect() and returns the
// chosen name. With exactly one match, it picks that host and prints
// which. With both, it prefers claude for backward compat and prints a
// hint about --host opencode. With none, it returns an error carrying
// install pointers for both backends.
func autoDetectHost() (string, error) {
	var detected []string
	for _, name := range host.RegisteredNames() {
		h, ok := host.Lookup(name)
		if !ok {
			continue
		}
		found, _, err := h.Detect()
		if err != nil {
			continue
		}
		if found {
			detected = append(detected, name)
		}
	}

	claudeSeen := false
	opencodeSeen := false
	for _, n := range detected {
		switch n {
		case "claude":
			claudeSeen = true
		case "opencode":
			opencodeSeen = true
		}
	}

	switch {
	case claudeSeen && opencodeSeen:
		fmt.Fprintln(stdout(),
			"devlog: detected both claude and opencode; defaulting to claude "+
				"(use --host opencode to override)")
		return "claude", nil
	case claudeSeen:
		fmt.Fprintln(stdout(), "devlog: detected claude")
		return "claude", nil
	case opencodeSeen:
		fmt.Fprintln(stdout(), "devlog: detected opencode")
		return "opencode", nil
	case len(detected) == 1:
		// Exactly one registered host detected and it is neither of the
		// two we know by name — honour it anyway.
		fmt.Fprintf(stdout(), "devlog: detected %s\n", detected[0])
		return detected[0], nil
	default:
		return "", derrors.New("install",
			"no supported host CLI found on PATH").
			WithRemediation(
				"Install one of:\n\n" +
					"  Claude Code: https://docs.anthropic.com/en/docs/claude-code\n" +
					"  OpenCode:    https://opencode.ai\n\n" +
					"Or pass --host claude / --host opencode explicitly.\n",
			)
	}
}

// persistInstallConfig writes the selected host, host_command, and optional
// model overrides into .devlog/config.json relative to the project root.
// Missing fields inherit existing values; a missing config file is created
// from defaults overlaid with the chosen host. When projectRoot is empty
// the current working directory is used and findDevlogDir walks up to
// locate the nearest .git ancestor.
func persistInstallConfig(projectRoot, hostName, hostCommand, summarizerModel, companionModel string) error {
	root, err := resolveProjectRoot(projectRoot)
	if err != nil {
		return err
	}
	devlogDir := findDevlogDir(root)
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		return derrors.Wrap("install",
			fmt.Sprintf("create %s", devlogDir), err)
	}
	configPath := filepath.Join(devlogDir, "config.json")

	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		return err
	}

	cfg.Host = hostName
	if hostCommand != "" {
		cfg.HostCommand = hostCommand
	} else if cfg.HostCommand == "" {
		cfg.HostCommand = hostName
	}
	if summarizerModel != "" {
		cfg.SummarizerModel = summarizerModel
	}
	if companionModel != "" {
		cfg.CompanionModel = companionModel
	}

	return state.SaveConfig(configPath, cfg)
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

// asString returns v as a string when v is a string, else the empty
// string. Used for defensive comparison against JSON-decoded maps where
// numeric or boolean matcher values would otherwise panic.
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
