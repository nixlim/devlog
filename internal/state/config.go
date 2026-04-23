package state

import (
	"encoding/json"
	"fmt"
	"os"

	devlogerrors "devlog/internal/errors"
)

// Config mirrors the tunable parameters table in SPEC.md. Every field is a
// 1:1 mapping to the on-disk `.devlog/config.json` schema; see the Default
// function for canonical values.
//
// The master `Enabled` switch is a *bool rather than a bool so a config
// file that omits the field inherits the default (true) instead of being
// silently flipped off by Go's zero value.
type Config struct {
	BufferSize               int    `json:"buffer_size"`
	CompanionInterval        int    `json:"companion_interval"`
	SummarizerModel          string `json:"summarizer_model"`
	CompanionModel           string `json:"companion_model"`
	SummarizerContextEntries int    `json:"summarizer_context_entries"`
	CompanionLogEntries      int    `json:"companion_log_entries"`
	CompanionDiffEntries     int    `json:"companion_diff_entries"`
	Enabled                  *bool  `json:"enabled,omitempty"`
	MaxDiffChars             int    `json:"max_diff_chars"`
	MaxDetailChars           int    `json:"max_detail_chars"`
	ClaudeCommand            string `json:"claude_command"`
	SummarizerTimeoutSeconds int    `json:"summarizer_timeout_seconds"`
	CompanionTimeoutSeconds  int    `json:"companion_timeout_seconds"`
}

// boolPtr returns a pointer to v. Used to populate *bool defaults.
func boolPtr(v bool) *bool { return &v }

// Default returns a Config populated with the canonical defaults defined
// in SPEC.md's "Tunable Parameters" table.
func Default() *Config {
	return &Config{
		BufferSize:               10,
		CompanionInterval:        5,
		SummarizerModel:          "claude-haiku-4-5-20251001",
		CompanionModel:           "claude-sonnet-4-6",
		SummarizerContextEntries: 5,
		CompanionLogEntries:      25,
		CompanionDiffEntries:     50,
		Enabled:                  boolPtr(true),
		MaxDiffChars:             2000,
		MaxDetailChars:           200,
		ClaudeCommand:            "claude",
		SummarizerTimeoutSeconds: 60,
		CompanionTimeoutSeconds:  120,
	}
}

// IsEnabled reports whether the master switch is on. It treats a nil
// Enabled pointer as "inherit default" (true) so callers don't have to
// remember the invariant.
func (c *Config) IsEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// LoadConfig reads path and returns a Config whose fields are the
// defaults overlaid by any fields present in the file. A missing file is
// not an error — the defaults are returned unchanged.
//
// Only fields explicitly present in the JSON override the defaults.
// Numeric zero-values written to disk are honoured (and will later be
// rejected by Validate) so operators see the same value they wrote.
func LoadConfig(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, devlogerrors.Wrap("config", fmt.Sprintf("read %s", path), err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, devlogerrors.Wrap("config", fmt.Sprintf("parse %s", path), err).
			WithRemediation(
				"The config file is not valid JSON. Open it in an editor and fix the\n" +
					"syntax, or delete the file to fall back to defaults:\n\n" +
					"    rm " + path + "\n",
			)
	}
	return cfg, nil
}

// SaveConfig writes cfg to path as pretty-printed JSON. The write is
// non-atomic — config.json is only touched by operators running
// `devlog config <key> <value>`, never from contended hook paths.
func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return devlogerrors.New("config", "SaveConfig received nil config")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return devlogerrors.Wrap("config", "encode", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return devlogerrors.Wrap("config", fmt.Sprintf("write %s", path), err)
	}
	return nil
}

// Validate enforces the invariants a DevLog installation must satisfy to
// operate safely. Counters and interval-style fields must be strictly
// positive — a buffer_size of 0 would mean "flush on every tool call" and
// starve the working agent; a companion_interval of 0 would thrash the
// Sonnet model. Timeouts and string commands are checked for sanity so
// misconfigurations surface at load time, not mid-flush.
func (c *Config) Validate() error {
	if c == nil {
		return devlogerrors.New("config", "Validate called on nil config")
	}
	if c.BufferSize <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("buffer_size must be > 0 (got %d)", c.BufferSize)).
			WithRemediation(
				"Edit .devlog/config.json and set buffer_size to a positive integer\n" +
					"(the default is 10). Run: devlog config buffer_size 10\n",
			)
	}
	if c.CompanionInterval <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("companion_interval must be > 0 (got %d)", c.CompanionInterval)).
			WithRemediation(
				"Edit .devlog/config.json and set companion_interval to a positive\n" +
					"integer (the default is 5). Run: devlog config companion_interval 5\n",
			)
	}
	if c.SummarizerContextEntries < 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("summarizer_context_entries must be >= 0 (got %d)", c.SummarizerContextEntries))
	}
	if c.CompanionLogEntries <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("companion_log_entries must be > 0 (got %d)", c.CompanionLogEntries))
	}
	if c.CompanionDiffEntries < 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("companion_diff_entries must be >= 0 (got %d)", c.CompanionDiffEntries))
	}
	if c.MaxDiffChars <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("max_diff_chars must be > 0 (got %d)", c.MaxDiffChars))
	}
	if c.MaxDetailChars <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("max_detail_chars must be > 0 (got %d)", c.MaxDetailChars))
	}
	if c.SummarizerTimeoutSeconds <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("summarizer_timeout_seconds must be > 0 (got %d)", c.SummarizerTimeoutSeconds))
	}
	if c.CompanionTimeoutSeconds <= 0 {
		return devlogerrors.New("config",
			fmt.Sprintf("companion_timeout_seconds must be > 0 (got %d)", c.CompanionTimeoutSeconds))
	}
	if c.ClaudeCommand == "" {
		return devlogerrors.New("config", "claude_command must not be empty").
			WithRemediation(
				"Set claude_command to the path of your claude CLI binary:\n\n" +
					"    devlog config claude_command claude\n",
			)
	}
	if c.SummarizerModel == "" {
		return devlogerrors.New("config", "summarizer_model must not be empty")
	}
	if c.CompanionModel == "" {
		return devlogerrors.New("config", "companion_model must not be empty")
	}
	return nil
}
