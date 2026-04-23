package state

import (
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	devlogerrors "devlog/internal/errors"
)

func TestDefaultMatchesSpec(t *testing.T) {
	cfg := Default()

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"BufferSize", cfg.BufferSize, 10},
		{"CompanionInterval", cfg.CompanionInterval, 5},
		{"SummarizerModel", cfg.SummarizerModel, "claude-haiku-4-5-20251001"},
		{"CompanionModel", cfg.CompanionModel, "claude-sonnet-4-6"},
		{"SummarizerContextEntries", cfg.SummarizerContextEntries, 5},
		{"CompanionLogEntries", cfg.CompanionLogEntries, 25},
		{"CompanionDiffEntries", cfg.CompanionDiffEntries, 50},
		{"MaxDiffChars", cfg.MaxDiffChars, 2000},
		{"MaxDetailChars", cfg.MaxDetailChars, 200},
		{"ClaudeCommand", cfg.ClaudeCommand, "claude"},
		{"Host", cfg.Host, "claude"},
		{"HostCommand", cfg.HostCommand, "claude"},
		{"SummarizerTimeoutSeconds", cfg.SummarizerTimeoutSeconds, 60},
		{"CompanionTimeoutSeconds", cfg.CompanionTimeoutSeconds, 120},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Default().%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !cfg.IsEnabled() {
		t.Errorf("Default().IsEnabled() = false, want true")
	}
}

func TestIsEnabledNilSafe(t *testing.T) {
	var c *Config
	if !c.IsEnabled() {
		t.Errorf("nil.IsEnabled() = false, want true (nil means default)")
	}
	c = &Config{}
	if !c.IsEnabled() {
		t.Errorf("zero Config.IsEnabled() = false, want true")
	}
	f := false
	c.Enabled = &f
	if c.IsEnabled() {
		t.Errorf("Config{Enabled=&false}.IsEnabled() = true")
	}
}

func TestLoadConfigMissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(missing): %v", err)
	}
	if cfg.BufferSize != 10 || cfg.CompanionInterval != 5 {
		t.Errorf("missing file should yield defaults, got %+v", cfg)
	}
}

func TestLoadConfigMergesOverDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Only override two fields; everything else must retain its default.
	body := `{"buffer_size": 42, "claude_command": "/opt/claude"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BufferSize != 42 {
		t.Errorf("BufferSize = %d, want 42", cfg.BufferSize)
	}
	if cfg.ClaudeCommand != "/opt/claude" {
		t.Errorf("ClaudeCommand = %q, want /opt/claude", cfg.ClaudeCommand)
	}
	if cfg.CompanionInterval != 5 {
		t.Errorf("CompanionInterval = %d, want 5 (default preserved)", cfg.CompanionInterval)
	}
	if cfg.SummarizerModel != "claude-haiku-4-5-20251001" {
		t.Errorf("SummarizerModel not preserved: %q", cfg.SummarizerModel)
	}
	if !cfg.IsEnabled() {
		t.Errorf("missing enabled field should default to true")
	}
}

func TestLoadConfigRespectsExplicitDisable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"enabled": false}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.IsEnabled() {
		t.Errorf("enabled=false should disable; got IsEnabled=true")
	}
}

func TestLoadConfigMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected DevlogError, got %T: %v", err, err)
	}
	if de.Component != "config" {
		t.Errorf("component = %q, want config", de.Component)
	}
	if !strings.Contains(de.Remediation, path) {
		t.Errorf("remediation should point at the bad file: %q", de.Remediation)
	}
}

func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := Default()
	cfg.BufferSize = 7
	cfg.ClaudeCommand = "/usr/local/bin/claude"
	cfg.Host = "opencode"
	cfg.HostCommand = "/usr/local/bin/opencode"

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.BufferSize != 7 || got.ClaudeCommand != "/usr/local/bin/claude" {
		t.Errorf("round trip lost overrides: %+v", got)
	}
	if got.Host != "opencode" {
		t.Errorf("Host = %q, want opencode", got.Host)
	}
	if got.HostCommand != "/usr/local/bin/opencode" {
		t.Errorf("HostCommand = %q, want /usr/local/bin/opencode", got.HostCommand)
	}
	if !got.IsEnabled() {
		t.Errorf("round trip flipped enabled to false")
	}
}

func TestSaveConfigNil(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(filepath.Join(dir, "cfg.json"), nil); err == nil {
		t.Errorf("SaveConfig(nil) should error")
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("Default config should validate: %v", err)
	}
}

func TestValidateRejectsZeroBufferSize(t *testing.T) {
	cfg := Default()
	cfg.BufferSize = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected error for buffer_size=0")
	}
	if !strings.Contains(err.Error(), "buffer_size") {
		t.Errorf("error should mention buffer_size: %v", err)
	}
}

func TestValidateRejectsNegativeBufferSize(t *testing.T) {
	cfg := Default()
	cfg.BufferSize = -1
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for negative buffer_size")
	}
}

func TestValidateRejectsZeroCompanionInterval(t *testing.T) {
	cfg := Default()
	cfg.CompanionInterval = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected error for companion_interval=0")
	}
	if !strings.Contains(err.Error(), "companion_interval") {
		t.Errorf("error should mention companion_interval: %v", err)
	}
}

func TestValidateRejectsEmptyClaudeCommand(t *testing.T) {
	cfg := Default()
	cfg.ClaudeCommand = ""
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for empty claude_command")
	}
}

func TestValidateRejectsEmptyModels(t *testing.T) {
	for _, field := range []string{"summarizer", "companion"} {
		cfg := Default()
		if field == "summarizer" {
			cfg.SummarizerModel = ""
		} else {
			cfg.CompanionModel = ""
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected error when %s model is empty", field)
		}
	}
}

func TestValidateRejectsBadTimeouts(t *testing.T) {
	cfg := Default()
	cfg.SummarizerTimeoutSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for zero summarizer_timeout_seconds")
	}
	cfg = Default()
	cfg.CompanionTimeoutSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for negative companion_timeout_seconds")
	}
}

func TestValidateNilReceiver(t *testing.T) {
	var c *Config
	if err := c.Validate(); err == nil {
		t.Errorf("nil.Validate() should error")
	}
}
