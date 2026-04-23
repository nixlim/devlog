package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"devlog/internal/host"
)

func TestName(t *testing.T) {
	h := &OpenCodeHost{}
	if got := h.Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestSetCommandDefault(t *testing.T) {
	h := &OpenCodeHost{}
	h.SetCommand("")
	if h.Command != "opencode" {
		t.Errorf("SetCommand(\"\") left Command = %q, want %q", h.Command, "opencode")
	}
	h.SetCommand("/usr/local/bin/opencode")
	if h.Command != "/usr/local/bin/opencode" {
		t.Errorf("SetCommand did not override, got %q", h.Command)
	}
}

func TestRegistered(t *testing.T) {
	h, ok := host.Lookup("opencode")
	if !ok {
		t.Fatal("opencode host not registered")
	}
	if h.Name() != "opencode" {
		t.Errorf("registered host name = %q", h.Name())
	}
}

func TestNormalizeModel(t *testing.T) {
	h := &OpenCodeHost{}
	cases := []struct{ in, want string }{
		{"claude-haiku-4-5-20251001", "anthropic/claude-haiku-4-5-20251001"},
		{"anthropic/claude-haiku-4-5-20251001", "anthropic/claude-haiku-4-5-20251001"},
		{"openrouter/anthropic/claude-sonnet-4-6", "openrouter/anthropic/claude-sonnet-4-6"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := h.NormalizeModel(tc.in); got != tc.want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInstallWritesPlugin(t *testing.T) {
	dir := t.TempDir()
	h := &OpenCodeHost{}
	pluginDir := filepath.Join(dir, "plugins")
	configPath := filepath.Join(dir, "opencode.json")
	if err := h.Install(host.InstallOpts{PluginDir: pluginDir, ConfigPath: configPath}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	pluginPath := filepath.Join(pluginDir, "devlog.ts")
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	if !bytes.Contains(data, []byte("tool.execute.before")) {
		t.Error("plugin file missing expected content")
	}
	cfgData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("parse opencode.json: %v", err)
	}
	plugins, ok := cfg["plugins"].(map[string]any)
	if !ok {
		t.Fatalf("plugins key missing or wrong type: %T", cfg["plugins"])
	}
	if plugins["devlog"] != pluginPath {
		t.Errorf("plugins.devlog = %v, want %q", plugins["devlog"], pluginPath)
	}
}

func TestInstallPreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	existing := map[string]any{
		"theme": "dark",
		"plugins": map[string]any{
			"other": "./plugins/other.ts",
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &OpenCodeHost{}
	if err := h.Install(host.InstallOpts{PluginDir: filepath.Join(dir, "p"), ConfigPath: configPath}); err != nil {
		t.Fatal(err)
	}
	cfgData, _ := os.ReadFile(configPath)
	var cfg map[string]any
	_ = json.Unmarshal(cfgData, &cfg)
	if cfg["theme"] != "dark" {
		t.Errorf("theme clobbered: %v", cfg["theme"])
	}
	plugins := cfg["plugins"].(map[string]any)
	if plugins["other"] != "./plugins/other.ts" {
		t.Errorf("other plugin dropped: %v", plugins["other"])
	}
	if _, ok := plugins["devlog"]; !ok {
		t.Error("devlog plugin not added")
	}
}

func TestUninstallRemovesPluginAndConfig(t *testing.T) {
	dir := t.TempDir()
	h := &OpenCodeHost{}
	pluginDir := filepath.Join(dir, "plugins")
	configPath := filepath.Join(dir, "opencode.json")
	if err := h.Install(host.InstallOpts{PluginDir: pluginDir, ConfigPath: configPath}); err != nil {
		t.Fatal(err)
	}
	if err := h.Uninstall(host.InstallOpts{PluginDir: pluginDir, ConfigPath: configPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "devlog.ts")); !os.IsNotExist(err) {
		t.Errorf("plugin file not removed: err=%v", err)
	}
	cfgData, _ := os.ReadFile(configPath)
	var cfg map[string]any
	_ = json.Unmarshal(cfgData, &cfg)
	if plugins, ok := cfg["plugins"].(map[string]any); ok {
		if _, has := plugins["devlog"]; has {
			t.Error("plugins.devlog still present after uninstall")
		}
	}
}

func TestUninstallMissingConfig(t *testing.T) {
	dir := t.TempDir()
	h := &OpenCodeHost{}
	if err := h.Uninstall(host.InstallOpts{
		PluginDir:  filepath.Join(dir, "plugins"),
		ConfigPath: filepath.Join(dir, "opencode.json"),
	}); err != nil {
		t.Errorf("Uninstall on missing config should be no-op, got %v", err)
	}
}

func TestRunLLMArgv(t *testing.T) {
	var capturedName string
	var capturedArgs []string
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"test","model":"anthropic/test"}`)
	}
	h := &OpenCodeHost{Command: "opencode"}
	resp, err := h.RunLLM(context.Background(), "claude-haiku-4-5-20251001", "summarize", 10*time.Second)
	if err != nil {
		t.Fatalf("RunLLM: %v", err)
	}
	if resp.Result != "test" {
		t.Errorf("Result = %q, want %q", resp.Result, "test")
	}
	if capturedName != "opencode" {
		t.Errorf("name = %q, want %q", capturedName, "opencode")
	}
	wantArgs := []string{"run", "--format", "json", "--model", "anthropic/claude-haiku-4-5-20251001", "summarize"}
	if !reflect.DeepEqual(capturedArgs, wantArgs) {
		t.Errorf("args = %v, want %v", capturedArgs, wantArgs)
	}
}

func TestRunLLMCommandNotFound(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/definitely/not/a/real/binary/devlog-xyz")
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "model", "prompt", time.Second)
	if !errors.Is(err, host.ErrCommandNotFound) {
		t.Errorf("expected ErrCommandNotFound, got %v", err)
	}
}

func TestRunLLMInvalidJSON(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "not json")
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "model", "prompt", time.Second)
	if !errors.Is(err, host.ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON, got %v", err)
	}
}

func TestRunLLMEmptyResult(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"   "}`)
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "model", "prompt", time.Second)
	if !errors.Is(err, host.ErrEmptyResponse) {
		t.Errorf("expected ErrEmptyResponse, got %v", err)
	}
}

func TestRunLLMTimeout(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "2")
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "model", "prompt", 50*time.Millisecond)
	if !errors.Is(err, host.ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestHostConfigurable(t *testing.T) {
	var _ host.Configurable = (*OpenCodeHost)(nil)
}
