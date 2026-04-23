package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	derrors "devlog/internal/errors"
	"devlog/internal/state"
)

// Config implements `devlog config [key] [value]`.
//
// Zero args prints every effective key=value pair (defaults overlaid by
// .devlog/config.json). One arg prints the value of that single key.
// Two args set the key and persist the new config, running Validate
// before the write so obvious mistakes (negative timeouts, empty model
// names) are rejected with a structured error rather than silently
// accepted and only caught on the next hook invocation.
func Config(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), configUsage)
		return 0
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr(), "devlog: error: config: resolve working directory: %v\n", err)
		return 1
	}
	devlogDir := findDevlogDir(wd)
	configPath := filepath.Join(devlogDir, "config.json")

	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		printErr(err)
		return 1
	}

	switch len(args) {
	case 0:
		return runConfigList(cfg, stdout())
	case 1:
		return runConfigGet(cfg, args[0], stdout())
	case 2:
		return runConfigSet(cfg, args[0], args[1], configPath, stdout())
	default:
		fmt.Fprintln(stderr(), "devlog: error: config: expected at most 2 arguments (key, value)")
		fmt.Fprint(stderr(), configUsage)
		return 2
	}
}

const configUsage = `devlog config — get or set tunable parameters

Usage:
    devlog config                 # print every effective key=value pair
    devlog config <key>           # print a single value
    devlog config <key> <value>   # set and persist a value

Values are validated before the write — empty model names and
non-positive counters/timeouts are rejected with an explanatory error.
`

// runConfigList prints every config key in sorted order as `key = value`.
// Sorting keeps the output stable across Go map iteration orders, which
// matters for snapshot tests and user eye-diff.
func runConfigList(cfg *state.Config, w io.Writer) int {
	pairs := configPairs(cfg)
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s = %s\n", k, pairs[k])
	}
	return 0
}

// runConfigGet prints the single value for key.
func runConfigGet(cfg *state.Config, key string, w io.Writer) int {
	pairs := configPairs(cfg)
	val, ok := pairs[key]
	if !ok {
		printErr(unknownKeyError(key))
		return 1
	}
	fmt.Fprintln(w, val)
	return 0
}

// runConfigSet validates + persists the new value. It also runs
// cfg.Validate() across the whole struct so combinations that would be
// invalid (e.g. a bad buffer_size) are caught before we hit disk.
func runConfigSet(cfg *state.Config, key, raw, path string, w io.Writer) int {
	if err := applyConfigValue(cfg, key, raw); err != nil {
		printErr(err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		printErr(err)
		return 1
	}
	if err := state.SaveConfig(path, cfg); err != nil {
		printErr(err)
		return 1
	}
	fmt.Fprintf(w, "%s = %s\n", key, raw)
	return 0
}

// configPairs returns the current string-valued view of cfg. The result
// is used for both listing and single-key gets — keeping the two paths
// backed by one function guarantees they never disagree on formatting.
func configPairs(cfg *state.Config) map[string]string {
	enabled := "true"
	if !cfg.IsEnabled() {
		enabled = "false"
	}
	return map[string]string{
		"buffer_size":                strconv.Itoa(cfg.BufferSize),
		"companion_interval":         strconv.Itoa(cfg.CompanionInterval),
		"summarizer_model":           cfg.SummarizerModel,
		"companion_model":            cfg.CompanionModel,
		"summarizer_context_entries": strconv.Itoa(cfg.SummarizerContextEntries),
		"companion_log_entries":      strconv.Itoa(cfg.CompanionLogEntries),
		"companion_diff_entries":     strconv.Itoa(cfg.CompanionDiffEntries),
		"enabled":                    enabled,
		"max_diff_chars":             strconv.Itoa(cfg.MaxDiffChars),
		"max_detail_chars":           strconv.Itoa(cfg.MaxDetailChars),
		"claude_command":             cfg.ClaudeCommand,
		"summarizer_timeout_seconds": strconv.Itoa(cfg.SummarizerTimeoutSeconds),
		"companion_timeout_seconds":  strconv.Itoa(cfg.CompanionTimeoutSeconds),
	}
}

// applyConfigValue parses raw as the type expected by key and writes it
// back into cfg. Unknown keys and parse errors surface as DevlogErrors
// with clear remediation so operators see what went wrong in one line.
func applyConfigValue(cfg *state.Config, key, raw string) error {
	switch key {
	case "buffer_size":
		return setInt(&cfg.BufferSize, key, raw)
	case "companion_interval":
		return setInt(&cfg.CompanionInterval, key, raw)
	case "summarizer_context_entries":
		return setInt(&cfg.SummarizerContextEntries, key, raw)
	case "companion_log_entries":
		return setInt(&cfg.CompanionLogEntries, key, raw)
	case "companion_diff_entries":
		return setInt(&cfg.CompanionDiffEntries, key, raw)
	case "max_diff_chars":
		return setInt(&cfg.MaxDiffChars, key, raw)
	case "max_detail_chars":
		return setInt(&cfg.MaxDetailChars, key, raw)
	case "summarizer_timeout_seconds":
		return setInt(&cfg.SummarizerTimeoutSeconds, key, raw)
	case "companion_timeout_seconds":
		return setInt(&cfg.CompanionTimeoutSeconds, key, raw)
	case "summarizer_model":
		cfg.SummarizerModel = raw
		return nil
	case "companion_model":
		cfg.CompanionModel = raw
		return nil
	case "claude_command":
		cfg.ClaudeCommand = raw
		return nil
	case "enabled":
		return setBool(&cfg.Enabled, key, raw)
	default:
		return unknownKeyError(key)
	}
}

// setInt parses raw as a decimal integer and assigns it to *p.
func setInt(p *int, key, raw string) error {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return derrors.New("config",
			fmt.Sprintf("%s requires an integer (got %q)", key, raw)).
			WithRemediation(
				"Pass a plain integer, for example:\n\n" +
					"    devlog config " + key + " 10\n",
			)
	}
	*p = v
	return nil
}

// setBool parses raw as a bool and assigns it to **p (the Config.Enabled
// pointer shape). Accepts the usual Go literals plus "on"/"off" for
// ergonomics.
func setBool(p **bool, key, raw string) error {
	s := strings.ToLower(strings.TrimSpace(raw))
	var v bool
	switch s {
	case "true", "1", "yes", "on":
		v = true
	case "false", "0", "no", "off":
		v = false
	default:
		return derrors.New("config",
			fmt.Sprintf("%s requires a boolean (got %q)", key, raw)).
			WithRemediation(
				"Accepted values: true, false, on, off, yes, no, 1, 0.\n",
			)
	}
	*p = &v
	return nil
}

// unknownKeyError produces a DevlogError listing every valid key so the
// user can correct a typo from the error message alone.
func unknownKeyError(key string) error {
	valid := configPairs(state.Default())
	keys := make([]string, 0, len(valid))
	for k := range valid {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return derrors.New("config", fmt.Sprintf("unknown key %q", key)).
		WithRemediation(
			"Valid keys:\n\n    " + strings.Join(keys, "\n    ") + "\n",
		)
}
