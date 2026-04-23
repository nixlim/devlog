package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlog/internal/state"
	"devlog/internal/testutil"
)

// configChdir switches to dir for the duration of the test. Needed
// because Config resolves the config path via os.Getwd() (matching
// every other devlog subcommand's behaviour).
func configChdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func setupConfigProject(t *testing.T) (root, configPath string) {
	t.Helper()
	root = testutil.NewTempDevlogDir(t)
	devlogDir := filepath.Join(root, ".devlog")
	configPath = filepath.Join(devlogDir, "config.json")
	if err := state.SaveConfig(configPath, state.Default()); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return root, configPath
}

func TestConfigNoArgsListsEverything(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	stdoutBuf, _ := withStreams(t)
	rc := Config(nil)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	out := stdoutBuf.String()

	wantKeys := []string{
		"buffer_size", "companion_interval", "summarizer_model",
		"companion_model", "enabled", "claude_command",
		"summarizer_timeout_seconds", "companion_timeout_seconds",
		"max_diff_chars", "max_detail_chars",
		"summarizer_context_entries", "companion_log_entries", "companion_diff_entries",
	}
	for _, k := range wantKeys {
		if !strings.Contains(out, k+" =") {
			t.Errorf("list output missing %q:\n%s", k, out)
		}
	}
	if !strings.Contains(out, "buffer_size = 10") {
		t.Errorf("defaults should show buffer_size = 10:\n%s", out)
	}
}

func TestConfigGetOneValue(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	stdoutBuf, _ := withStreams(t)
	rc := Config([]string{"buffer_size"})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got := strings.TrimSpace(stdoutBuf.String()); got != "10" {
		t.Errorf("got %q, want 10", got)
	}
}

func TestConfigGetEnabled(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	stdoutBuf, _ := withStreams(t)
	rc := Config([]string{"enabled"})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got := strings.TrimSpace(stdoutBuf.String()); got != "true" {
		t.Errorf("got %q, want true", got)
	}
}

func TestConfigGetUnknownKeyFails(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	rc := Config([]string{"not_a_real_key"})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(stderrBuf.String(), "unknown key") {
		t.Errorf("stderr should mention 'unknown key', got:\n%s", stderrBuf.String())
	}
}

func TestConfigSetIntPersists(t *testing.T) {
	root, configPath := setupConfigProject(t)
	configChdir(t, root)

	stdoutBuf, _ := withStreams(t)
	rc := Config([]string{"buffer_size", "42"})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdoutBuf.String(), "buffer_size = 42") {
		t.Errorf("stdout should echo new value, got %q", stdoutBuf.String())
	}

	// Confirm persistence.
	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BufferSize != 42 {
		t.Errorf("persisted BufferSize = %d, want 42", cfg.BufferSize)
	}
}

func TestConfigSetStringPersists(t *testing.T) {
	root, configPath := setupConfigProject(t)
	configChdir(t, root)

	_, _ = withStreams(t)
	rc := Config([]string{"claude_command", "/opt/custom/claude"})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}

	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ClaudeCommand != "/opt/custom/claude" {
		t.Errorf("ClaudeCommand = %q, want /opt/custom/claude", cfg.ClaudeCommand)
	}
}

func TestConfigSetBoolAcceptsVariants(t *testing.T) {
	root, configPath := setupConfigProject(t)
	configChdir(t, root)

	for _, v := range []string{"false", "off", "no", "0"} {
		_, _ = withStreams(t)
		if rc := Config([]string{"enabled", v}); rc != 0 {
			t.Fatalf("rc = %d for value %q", rc, v)
		}
		cfg, err := state.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.IsEnabled() {
			t.Errorf("value %q should disable but IsEnabled=true", v)
		}
	}
	for _, v := range []string{"true", "on", "yes", "1"} {
		_, _ = withStreams(t)
		if rc := Config([]string{"enabled", v}); rc != 0 {
			t.Fatalf("rc = %d for value %q", rc, v)
		}
		cfg, err := state.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if !cfg.IsEnabled() {
			t.Errorf("value %q should enable but IsEnabled=false", v)
		}
	}
}

func TestConfigSetRejectsNonIntegerForIntField(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	rc := Config([]string{"buffer_size", "banana"})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(stderrBuf.String(), "integer") {
		t.Errorf("stderr should mention 'integer', got:\n%s", stderrBuf.String())
	}
}

func TestConfigSetRejectsNonBoolForEnabled(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	rc := Config([]string{"enabled", "maybe"})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(stderrBuf.String(), "boolean") {
		t.Errorf("stderr should mention 'boolean', got:\n%s", stderrBuf.String())
	}
}

func TestConfigSetRunsValidate(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	// buffer_size must be > 0; Validate should reject.
	rc := Config([]string{"buffer_size", "0"})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(stderrBuf.String(), "buffer_size") {
		t.Errorf("stderr should reference buffer_size: %s", stderrBuf.String())
	}
}

func TestConfigSetNegativeBufferSizeRejected(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	rc := Config([]string{"buffer_size", "-3"})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if !strings.Contains(stderrBuf.String(), "buffer_size") {
		t.Errorf("stderr should reference buffer_size: %s", stderrBuf.String())
	}
}

func TestConfigTooManyArgsRejected(t *testing.T) {
	root, _ := setupConfigProject(t)
	configChdir(t, root)

	_, stderrBuf := withStreams(t)
	rc := Config([]string{"buffer_size", "10", "extra"})
	if rc != 2 {
		t.Fatalf("rc = %d, want 2", rc)
	}
	if !strings.Contains(stderrBuf.String(), "expected at most 2") {
		t.Errorf("stderr should explain argument count: %s", stderrBuf.String())
	}
}

func TestConfigHelpFlag(t *testing.T) {
	stdoutBuf, _ := withStreams(t)
	rc := Config([]string{"--help"})
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdoutBuf.String(), "Usage:") {
		t.Errorf("help output missing Usage:\n%s", stdoutBuf.String())
	}
}

func TestConfigListWithoutConfigFileShowsDefaults(t *testing.T) {
	// No config.json present; LoadConfig should hand back defaults.
	root := testutil.NewTempDevlogDir(t)
	// testutil's helper pre-creates .devlog — remove any stray config.
	_ = os.Remove(filepath.Join(root, ".devlog", "config.json"))
	configChdir(t, root)

	stdoutBuf, _ := withStreams(t)
	rc := Config(nil)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdoutBuf.String(), "buffer_size = 10") {
		t.Errorf("missing config.json should show defaults: %q", stdoutBuf.String())
	}
}
