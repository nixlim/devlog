// Package claude adapts internal/claude to the host.Host interface. It's
// registered under the name "claude" so host.Lookup returns a ClaudeHost
// configured with the default CLI command.
package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"devlog/internal/claude"
	"devlog/internal/host"
)

// ClaudeHost implements host.Host by delegating to the `claude` CLI via
// the existing internal/claude runner. Install / Uninstall remain thin
// stubs for now; cmd/install.go still performs the settings.json mutation
// directly and will be routed through here in a later task.
type ClaudeHost struct {
	// Command is the PATH-resolvable name (or absolute path) of the
	// claude binary. Defaults to "claude"; overridable via SetCommand.
	Command string
}

var _ host.Host = (*ClaudeHost)(nil)

func init() {
	host.Register("claude", func() host.Host {
		return &ClaudeHost{Command: "claude"}
	})
}

// Name returns the registry key for this host.
func (h *ClaudeHost) Name() string { return "claude" }

// SetCommand overrides the CLI command. Satisfies host.Configurable (see
// internal/host/configurable.go) so the cmd layer can configure a looked-
// up host without type-asserting to *ClaudeHost.
func (h *ClaudeHost) SetCommand(cmd string) {
	if cmd == "" {
		cmd = "claude"
	}
	h.Command = cmd
}

// Detect reports whether the claude CLI is resolvable on PATH and, when
// it is, the version string reported by `claude --version`. A missing
// binary yields (false, "", nil); unexpected I/O errors bubble up.
func (h *ClaudeHost) Detect() (bool, string, error) {
	cmd := h.Command
	if cmd == "" {
		cmd = "claude"
	}
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		// exec.ErrNotFound: bare name that PATH can't resolve.
		// fs.ErrNotExist: absolute path that doesn't exist on disk.
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
		// The binary is present but --version failed; still count as
		// detected so the operator sees that claude is installed.
		return true, "", nil
	}
	return true, strings.TrimSpace(out.String()), nil
}

func (h *ClaudeHost) Install(opts host.InstallOpts) error {
	if opts.SettingsPath == "" {
		return fmt.Errorf("ClaudeHost.Install requires opts.SettingsPath")
	}
	return installHooks(opts.SettingsPath)
}

func (h *ClaudeHost) Uninstall(opts host.InstallOpts) error {
	if opts.SettingsPath == "" {
		return fmt.Errorf("ClaudeHost.Uninstall requires opts.SettingsPath")
	}
	_, err := uninstallHooks(opts.SettingsPath)
	return err
}

// RunLLM invokes the claude CLI with the given model/prompt/timeout and
// returns the parsed envelope mapped onto host.Response. Errors from
// internal/claude are translated to host-level sentinels (host.ErrCommandNotFound,
// host.ErrTimeout, etc.) so the cmd/ layer never imports a host-specific
// package just to classify failures. The original error remains in the
// chain for diagnostic wrapping.
func (h *ClaudeHost) RunLLM(ctx context.Context, model, prompt string, timeout time.Duration) (*host.Response, error) {
	r := claude.New(h.Command)
	resp, err := r.Run(ctx, model, prompt, timeout)
	if err != nil {
		return nil, translateErr(err)
	}
	return &host.Response{
		Type:          resp.Type,
		Subtype:       resp.Subtype,
		Result:        resp.Result,
		SessionID:     resp.SessionID,
		Model:         resp.Model,
		DurationMS:    resp.DurationMS,
		DurationAPIMS: resp.DurationAPIMS,
		NumTurns:      resp.NumTurns,
		IsError:       resp.IsError,
		TotalCostUSD:  resp.TotalCostUSD,
		Raw:           resp.Raw,
	}, nil
}

// translateErr maps claude-package errors onto host-level sentinels. The
// returned error preserves the original in its chain so callers needing
// the internal/claude detail (tests, deep diagnostics) can still errors.As
// against *claude.ExitError — but routine callers should match on the
// host.Err* sentinels.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	var cx *claude.ExitError
	if errors.As(err, &cx) {
		return &host.ExitError{ExitCode: cx.ExitCode, Stderr: cx.Stderr}
	}
	switch {
	case errors.Is(err, claude.ErrCommandNotFound):
		return fmt.Errorf("%w: %w", host.ErrCommandNotFound, err)
	case errors.Is(err, claude.ErrTimeout):
		return fmt.Errorf("%w: %w", host.ErrTimeout, err)
	case errors.Is(err, claude.ErrEmptyResponse):
		return fmt.Errorf("%w: %w", host.ErrEmptyResponse, err)
	case errors.Is(err, claude.ErrInvalidJSON):
		return fmt.Errorf("%w: %w", host.ErrInvalidJSON, err)
	}
	return err
}

// NormalizeModel is a pass-through for Claude: the configured model id
// (e.g. "claude-haiku-4-5-20251001") is already the wire format the CLI
// expects. OpenCode will do more work here.
func (h *ClaudeHost) NormalizeModel(s string) string { return s }
