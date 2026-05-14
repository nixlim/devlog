package opencode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	if err := h.Install(host.InstallOpts{PluginDir: pluginDir}); err != nil {
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
}

func TestInstallDoesNotCreateConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "opencode.json")
	h := &OpenCodeHost{}
	if err := h.Install(host.InstallOpts{PluginDir: filepath.Join(dir, "p")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("Install should not create opencode.json, got err=%v", err)
	}
}

func TestUninstallRemovesPlugin(t *testing.T) {
	dir := t.TempDir()
	h := &OpenCodeHost{}
	pluginDir := filepath.Join(dir, "plugins")
	if err := h.Install(host.InstallOpts{PluginDir: pluginDir}); err != nil {
		t.Fatal(err)
	}
	if err := h.Uninstall(host.InstallOpts{PluginDir: pluginDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "devlog.ts")); !os.IsNotExist(err) {
		t.Errorf("plugin file not removed: err=%v", err)
	}
}

func TestUninstallMissingPlugin(t *testing.T) {
	dir := t.TempDir()
	h := &OpenCodeHost{}
	if err := h.Uninstall(host.InstallOpts{
		PluginDir: filepath.Join(dir, "plugins"),
	}); err != nil {
		t.Errorf("Uninstall on missing plugin should be no-op, got %v", err)
	}
}

func TestRunLLMArgv(t *testing.T) {
	var capturedName string
	var capturedArgs []string
	origExec := execCommand
	defer func() { execCommand = origExec }()
	ndjson := `{"type":"step_start","timestamp":1000,"sessionID":"ses_abc","part":{}}
{"type":"text","timestamp":1050,"sessionID":"ses_abc","part":{"type":"text","text":"test"}}
{"type":"step_finish","timestamp":1100,"sessionID":"ses_abc","part":{"type":"step-finish","cost":0.001}}`
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "printf", "%s", ndjson)
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

func TestRunLLMEmptyResult(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	ndjson := `{"type":"step_start","timestamp":1000,"sessionID":"ses_abc","part":{}}
{"type":"text","timestamp":1050,"sessionID":"ses_abc","part":{"type":"text","text":"   "}}
{"type":"step_finish","timestamp":1100,"sessionID":"ses_abc","part":{"type":"step-finish"}}`
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", ndjson)
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "model", "prompt", time.Second)
	if !errors.Is(err, host.ErrEmptyResponse) {
		t.Errorf("expected ErrEmptyResponse, got %v", err)
	}
}

func TestRunLLMErrorEvent(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo",
			`{"type":"error","sessionID":"ses_abc","error":{"name":"UnknownError","data":{"message":"Model not found: anthropic/bad-model"}}}`)
	}
	h := &OpenCodeHost{Command: "opencode"}
	_, err := h.RunLLM(context.Background(), "bad-model", "prompt", time.Second)
	if !errors.Is(err, host.ErrNonZeroExit) {
		t.Errorf("expected ErrNonZeroExit, got %v", err)
	}
	var exitErr *host.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *host.ExitError, got %T", err)
	}
	if !strings.Contains(exitErr.Stderr, "Model not found") {
		t.Errorf("stderr = %q, want it to contain 'Model not found'", exitErr.Stderr)
	}
}

func TestRunLLMMultipleTextParts(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()
	ndjson := `{"type":"step_start","timestamp":1000,"sessionID":"ses_abc","part":{}}
{"type":"text","timestamp":1050,"sessionID":"ses_abc","part":{"type":"text","text":"hello "}}
{"type":"text","timestamp":1060,"sessionID":"ses_abc","part":{"type":"text","text":"world"}}
{"type":"step_finish","timestamp":1100,"sessionID":"ses_abc","part":{"type":"step-finish","cost":0.002}}`
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", ndjson)
	}
	h := &OpenCodeHost{Command: "opencode"}
	resp, err := h.RunLLM(context.Background(), "model", "prompt", time.Second)
	if err != nil {
		t.Fatalf("RunLLM: %v", err)
	}
	if resp.Result != "hello world" {
		t.Errorf("Result = %q, want %q", resp.Result, "hello world")
	}
	if resp.DurationMS != 100 {
		t.Errorf("DurationMS = %d, want 100", resp.DurationMS)
	}
	if resp.TotalCostUSD != 0.002 {
		t.Errorf("TotalCostUSD = %f, want 0.002", resp.TotalCostUSD)
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

func TestHostModelDiscoverer(t *testing.T) {
	var _ host.ModelDiscoverer = (*OpenCodeHost)(nil)
}

func TestDiscoverModels(t *testing.T) {
	origList := listModelsCommand
	defer func() { listModelsCommand = origList }()
	var capturedArgs []string
	listModelsCommand = func(cmd string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.Command("printf", "%s",
			"tesco-anthropic/haiku-4.5\ntesco-anthropic/opus-4.6\ntesco-anthropic/sonnet-4.6\ntesco-openai/gpt-5.4\n")
	}
	h := &OpenCodeHost{Command: "opencode"}
	sum, comp, err := h.DiscoverModels()
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if sum != "tesco-anthropic/haiku-4.5" {
		t.Errorf("summarizer = %q, want tesco-anthropic/haiku-4.5", sum)
	}
	if comp != "tesco-anthropic/sonnet-4.6" {
		t.Errorf("companion = %q, want tesco-anthropic/sonnet-4.6", comp)
	}
	if !reflect.DeepEqual(capturedArgs, []string{"models"}) {
		t.Errorf("list command args = %v, want [models]", capturedArgs)
	}
}

func TestDiscoverModelsNoMatch(t *testing.T) {
	origList := listModelsCommand
	defer func() { listModelsCommand = origList }()
	listModelsCommand = func(cmd string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "tesco-openai/gpt-5.4\ntesco-other/kimi-k2.5\n")
	}
	h := &OpenCodeHost{Command: "opencode"}
	sum, comp, err := h.DiscoverModels()
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if sum != "" {
		t.Errorf("summarizer should be empty when no haiku, got %q", sum)
	}
	if comp != "" {
		t.Errorf("companion should be empty when no sonnet, got %q", comp)
	}
}

func TestDiscoverModelsFallsBackToLegacyCommand(t *testing.T) {
	origList := listModelsCommand
	defer func() { listModelsCommand = origList }()
	var calls [][]string
	listModelsCommand = func(cmd string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"models"}) {
			return exec.Command("sh", "-c", "exit 2")
		}
		return exec.Command("printf", "%s", "legacy/haiku\nlegacy/sonnet\n")
	}

	h := &OpenCodeHost{Command: "opencode"}
	sum, comp, err := h.DiscoverModels()
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if sum != "legacy/haiku" {
		t.Errorf("summarizer = %q, want legacy/haiku", sum)
	}
	if comp != "legacy/sonnet" {
		t.Errorf("companion = %q, want legacy/sonnet", comp)
	}
	if !reflect.DeepEqual(calls, [][]string{{"models"}, {"model", "ls"}}) {
		t.Errorf("calls = %v, want [[models] [model ls]]", calls)
	}
}

func TestMatchModel(t *testing.T) {
	models := []string{
		"tesco-anthropic/haiku-4.5",
		"tesco-anthropic/opus-4.6",
		"tesco-anthropic/sonnet-4.6",
		"anthropic/claude-haiku-4-5-20251001",
	}
	cases := []struct {
		patterns []string
		want     string
	}{
		{[]string{"haiku"}, "tesco-anthropic/haiku-4.5"},
		{[]string{"sonnet"}, "tesco-anthropic/sonnet-4.6"},
		{[]string{"opus"}, "tesco-anthropic/opus-4.6"},
		{[]string{"nonexistent"}, ""},
		{[]string{"nonexistent", "haiku"}, "tesco-anthropic/haiku-4.5"},
	}
	for _, tc := range cases {
		got := matchModel(models, tc.patterns)
		if got != tc.want {
			t.Errorf("matchModel(%v) = %q, want %q", tc.patterns, got, tc.want)
		}
	}
}
