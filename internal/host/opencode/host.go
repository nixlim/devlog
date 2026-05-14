// Package opencode implements host.Host for the OpenCode CLI. In addition
// to shelling out to `opencode run` for LLM invocations, it owns the
// installation / uninstallation of the embedded TypeScript plugin shim
// (see embed.go) into the user's OpenCode project directory.
package opencode

import (
	"bufio"
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
// PluginDir defaults to ".opencode/plugins" relative to the current
// working directory. OpenCode auto-loads plugins from that directory,
// so no opencode.json modification is needed.
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

	return nil
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
	return nil
}

// modelPatterns maps devlog roles to ordered substring preferences used
// by DiscoverModels to pick the best model from `opencode models`.
// Each list is tried in order; the first model whose ID contains the
// substring wins. This avoids hardcoding provider prefixes (anthropic/,
// tesco-anthropic/, etc.) while still landing on the right tier.
var modelPatterns = map[string][]string{
	"summarizer": {"haiku-4.5", "haiku-4-5", "haiku"},
	"companion":  {"sonnet-4.6", "sonnet-4-6", "sonnet"},
}

// listModelsCommand is indirected for tests.
var listModelsCommand = func(cmd string, args ...string) *exec.Cmd {
	return exec.Command(cmd, args...)
}

// DiscoverModels runs `opencode models` and picks the best summarizer
// (haiku-class) and companion (sonnet-class) models from the output.
// Returns empty strings for roles where no match is found — the caller
// falls back to config defaults in that case.
func (h *OpenCodeHost) DiscoverModels() (summarizer, companion string, err error) {
	cmd := h.Command
	if cmd == "" {
		cmd = "opencode"
	}

	out, err := runModelList(cmd, "models")
	if err != nil {
		legacyOut, legacyErr := runModelList(cmd, "model", "ls")
		if legacyErr != nil {
			return "", "", fmt.Errorf("opencode models: %w; opencode model ls: %v", err, legacyErr)
		}
		out = legacyOut
	}

	models := parseModelList(out)
	summarizer = matchModel(models, modelPatterns["summarizer"])
	companion = matchModel(models, modelPatterns["companion"])
	return summarizer, companion, nil
}

func runModelList(cmd string, args ...string) (string, error) {
	c := listModelsCommand(cmd, args...)
	var out, stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return out.String(), nil
}

// parseModelList splits the output of `opencode models` into trimmed,
// non-empty lines.
func parseModelList(output string) []string {
	var models []string
	for _, line := range strings.Split(output, "\n") {
		m := strings.TrimSpace(line)
		if m != "" {
			models = append(models, m)
		}
	}
	return models
}

// matchModel returns the first model whose id contains one of the
// patterns, tried in pattern order. Empty when no pattern matches.
func matchModel(models []string, patterns []string) string {
	for _, pat := range patterns {
		for _, m := range models {
			if strings.Contains(strings.ToLower(m), pat) {
				return m
			}
		}
	}
	return ""
}

// execCommand is indirected for tests. Production is exec.CommandContext.
var execCommand = exec.CommandContext

// RunLLM invokes `opencode run --format json --model <model>`
// and maps the response / failure modes onto host.Response and the
// host-level sentinel errors. A zero timeout means "inherit the caller's
// context deadline, if any".
//
// OpenCode's --format json emits NDJSON (one JSON object per line):
// step_start, text (one or more), step_finish — and possibly error events.
// This method parses each line, concatenates text parts, and extracts
// metadata from step_finish.
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

	args := []string{"run", "--pure", "--format", "json", "--model", normalized}
	c := execCommand(runCtx, cmd, args...)
	c.Stdin = strings.NewReader(prompt)
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

	resp, parseErr := parseNDJSON(raw)
	if parseErr != nil {
		return nil, parseErr
	}
	if strings.TrimSpace(resp.Result) == "" {
		return nil, fmt.Errorf("%w (stdout %d bytes)", host.ErrEmptyResponse, len(raw))
	}
	return resp, nil
}

// parseNDJSON parses the NDJSON streaming output from `opencode run
// --format json`. Each line is a JSON event with a "type" discriminator.
// Text is collected from "text" events; metadata comes from "step_finish".
// An "error" event is surfaced as host.ErrNonZeroExit with the error
// message.
func parseNDJSON(data []byte) (*host.Response, error) {
	var texts []string
	var sessionID string
	var model string
	var durationMS int
	var costUSD float64

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Error     *struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
			Part json.RawMessage `json:"part"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "error":
			msg := "unknown error"
			if event.Error != nil && event.Error.Data.Message != "" {
				msg = event.Error.Data.Message
			}
			return nil, &host.ExitError{ExitCode: 1, Stderr: msg}

		case "text":
			var part struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Part, &part) == nil && part.Text != "" {
				texts = append(texts, part.Text)
			}

		case "step_finish":
			var part struct {
				Cost   float64 `json:"cost"`
				Tokens struct {
					Total int `json:"total"`
				} `json:"tokens"`
			}
			if json.Unmarshal(event.Part, &part) == nil {
				costUSD += part.Cost
			}
		}
	}

	result := strings.Join(texts, "")
	elapsed := 0
	if len(data) > 0 {
		var first, last struct {
			Timestamp int64 `json:"timestamp"`
		}
		scanner2 := bufio.NewScanner(bytes.NewReader(data))
		for scanner2.Scan() {
			line := bytes.TrimSpace(scanner2.Bytes())
			if len(line) == 0 {
				continue
			}
			if first.Timestamp == 0 {
				_ = json.Unmarshal(line, &first)
			}
			_ = json.Unmarshal(line, &last)
		}
		if first.Timestamp > 0 && last.Timestamp > 0 {
			elapsed = int(last.Timestamp - first.Timestamp)
			durationMS = elapsed
		}
	}

	_ = model
	return &host.Response{
		Type:         "result",
		Subtype:      "success",
		Result:       result,
		SessionID:    sessionID,
		Model:        model,
		DurationMS:   durationMS,
		TotalCostUSD: costUSD,
		Raw:          append([]byte(nil), data...),
	}, nil
}
