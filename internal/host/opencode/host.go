// Package opencode implements host.Host for the OpenCode CLI. In addition
// to shelling out to `opencode run` for LLM invocations, it owns the
// installation / uninstallation of the embedded TypeScript plugin shim
// (see embed.go) into the user's OpenCode project directory.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"devlog/internal/host"
)

// OpenCodeHost implements host.Host against the `opencode` CLI. The CLI
// is the only integration point at runtime; install/uninstall additionally
// drop a TypeScript plugin file next to the user's opencode.json so the
// OpenCode runtime wires DevLog into its hook points.
type OpenCodeHost struct {
	// Command is the PATH-resolvable name (or absolute path) of the
	// opencode binary. Defaults to "opencode"; overridable via SetCommand.
	Command string
}

var _ host.Host = (*OpenCodeHost)(nil)

func init() {
	host.Register("opencode", func() host.Host {
		return &OpenCodeHost{Command: "opencode"}
	})
}

// Name returns the registry key for this host.
func (h *OpenCodeHost) Name() string { return "opencode" }

// SetCommand overrides the CLI command. Satisfies host.Configurable so the
// cmd layer can push Config.HostCommand into a looked-up host without
// type-asserting to *OpenCodeHost.
func (h *OpenCodeHost) SetCommand(cmd string) {
	if cmd == "" {
		cmd = "opencode"
	}
	h.Command = cmd
}

// Detect reports whether the opencode CLI is resolvable on PATH and, when
// it is, the version string reported by `opencode --version`. Mirrors the
// ClaudeHost implementation so autodetection can treat both backends
// uniformly.
func (h *OpenCodeHost) Detect() (bool, string, error) {
	cmd := h.Command
	if cmd == "" {
		cmd = "opencode"
	}
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return false, "", nil
		}
		return false, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, resolved, "--version")
	c.Stdout = &out
	c.Stderr = &out
	if err := c.Run(); err != nil {
		return true, "", nil
	}
	return true, strings.TrimSpace(out.String()), nil
}

// NormalizeModel prefixes bare model ids with "anthropic/" so they match
// the provider/model convention OpenCode's `--model` flag expects. Model
// strings that already contain a slash (e.g. "openrouter/anthropic/...")
// are passed through unchanged.
func (h *OpenCodeHost) NormalizeModel(s string) string {
	if s == "" {
		return s
	}
	if strings.Contains(s, "/") {
		return s
	}
	return "anthropic/" + s
}

// Install writes the embedded TypeScript plugin shim into the OpenCode
// plugin directory and merges a reference to it into opencode.json.
// PluginDir defaults to ".opencode/plugins" and ConfigPath to
// "opencode.json" relative to the current working directory.
func (h *OpenCodeHost) Install(opts host.InstallOpts) error {
	pluginDir := opts.PluginDir
	if pluginDir == "" {
		pluginDir = ".opencode/plugins"
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return fmt.Errorf("create plugin dir %s: %w", pluginDir, err)
	}

	pluginPath := filepath.Join(pluginDir, "devlog.ts")
	if err := os.WriteFile(pluginPath, PluginSource, 0o644); err != nil {
		return fmt.Errorf("write plugin %s: %w", pluginPath, err)
	}

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = "opencode.json"
	}
	return mergeOpenCodeConfig(configPath, pluginPath)
}

// mergeOpenCodeConfig reads opencode.json (treating missing as empty),
// sets plugins.devlog to pluginPath, and writes the file back. Any other
// top-level keys or plugin entries are preserved.
func mergeOpenCodeConfig(path, pluginPath string) error {
	var config map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			config = map[string]any{}
		} else {
			return fmt.Errorf("read %s: %w", path, err)
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if config == nil {
			config = map[string]any{}
		}
	}

	plugins, _ := config["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	plugins["devlog"] = pluginPath
	config["plugins"] = plugins

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o644)
}

// Uninstall is the inverse of Install: remove the plugin file and strip
// the plugins.devlog reference from opencode.json. Missing files are
// treated as nothing-to-undo rather than errors, so uninstall is
// idempotent on fresh machines.
func (h *OpenCodeHost) Uninstall(opts host.InstallOpts) error {
	pluginDir := opts.PluginDir
	if pluginDir == "" {
		pluginDir = ".opencode/plugins"
	}
	_ = os.Remove(filepath.Join(pluginDir, "devlog.ts"))

	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = "opencode.json"
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	if plugins, ok := config["plugins"].(map[string]any); ok {
		delete(plugins, "devlog")
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil
	}
	out = append(out, '\n')
	return os.WriteFile(configPath, out, 0o644)
}

// execCommand is indirected for tests. Production is exec.CommandContext.
var execCommand = exec.CommandContext

// RunLLM invokes `opencode run --format json --model <model> <prompt>`
// and maps the response / failure modes onto host.Response and the
// host-level sentinel errors. A zero timeout means "inherit the caller's
// context deadline, if any".
func (h *OpenCodeHost) RunLLM(ctx context.Context, model, prompt string, timeout time.Duration) (*host.Response, error) {
	cmd := h.Command
	if cmd == "" {
		cmd = "opencode"
	}

	normalized := h.NormalizeModel(model)

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	args := []string{"run", "--format", "json", "--model", normalized, prompt}
	c := execCommand(runCtx, cmd, args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()

	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%w after %s", host.ErrTimeout, timeout)
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", host.ErrCommandNotFound, cmd)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &host.ExitError{ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
		}
		return nil, err
	}

	raw := stdout.Bytes()
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: stdout was empty", host.ErrEmptyResponse)
	}
	var resp host.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", host.ErrInvalidJSON, err)
	}
	resp.Raw = append([]byte(nil), raw...)
	if strings.TrimSpace(resp.Result) == "" {
		return nil, fmt.Errorf("%w (stdout %d bytes)", host.ErrEmptyResponse, len(raw))
	}
	return &resp, nil
}
