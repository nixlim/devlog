// Package claude invokes the `claude` CLI as a subprocess. Both the Haiku
// summarizer and the Sonnet anti-pattern companion run through this package.
//
// The runner executes `claude -p "<prompt>" --model <model>
// --output-format json --dangerously-skip-permissions --max-turns 1` and
// returns the parsed JSON envelope. Errors are classified into distinct,
// programmatically-detectable kinds — command-not-found, timeout, non-zero
// exit, and empty response — so callers (flush, companion) can render
// SPEC-mandated remediation messages without sniffing strings.
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
)

// Sentinel errors. Use errors.Is to classify failures from Run. Every
// Run error wraps exactly one of these (or is a *ExitError, which also
// wraps ErrNonZeroExit).
var (
	// ErrCommandNotFound is returned when the configured claude binary
	// cannot be resolved on PATH or does not exist on disk.
	ErrCommandNotFound = errors.New("claude command not found")
	// ErrTimeout is returned when the context deadline fires before
	// claude exits. The process is sent SIGKILL by exec.CommandContext.
	ErrTimeout = errors.New("claude invocation timed out")
	// ErrNonZeroExit is returned (wrapped in *ExitError) when claude
	// exits with a non-zero code.
	ErrNonZeroExit = errors.New("claude exited non-zero")
	// ErrEmptyResponse is returned when claude exits 0 but the parsed
	// response carries no result text — nothing useful to write.
	ErrEmptyResponse = errors.New("claude returned empty response")
	// ErrInvalidJSON is returned when stdout cannot be parsed as the
	// --output-format json envelope.
	ErrInvalidJSON = errors.New("claude output is not valid JSON")
)

// ExitError carries the stderr and exit code from a non-zero claude
// invocation. Callers format the stderr into their own error messages so
// the operator sees the model's own diagnostic.
type ExitError struct {
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("claude exited with code %d", e.ExitCode)
	}
	return fmt.Sprintf("claude exited with code %d: %s", e.ExitCode, stderr)
}

// Unwrap lets errors.Is(err, ErrNonZeroExit) succeed on *ExitError values.
func (e *ExitError) Unwrap() error { return ErrNonZeroExit }

// Runner invokes the claude CLI. The zero value is not usable — always
// construct with New so the binary path is explicit.
//
// Runner is safe for concurrent use; it holds no mutable state and every
// invocation exec's a fresh subprocess.
type Runner struct {
	// Command is the path (or PATH-resolvable name) of the claude binary.
	// Typically the value of Config.ClaudeCommand.
	Command string
}

// New returns a Runner configured to invoke the named claude binary. An
// empty command falls back to "claude", matching the default in
// state.Config.
func New(command string) *Runner {
	if command == "" {
		command = "claude"
	}
	return &Runner{Command: command}
}

// Run invokes claude with the given model and prompt. The timeout bounds
// the entire subprocess lifetime (exec + model reasoning); pass 0 for no
// timeout (useful in tests but unsafe in production paths).
//
// On success, the returned Response carries both the parsed envelope and
// the raw stdout bytes for diagnostics. On failure, err is one of the
// sentinel errors above (possibly wrapped in *ExitError for the
// non-zero-exit case) and response is nil.
//
// Run does not retry. Callers that need retry semantics (e.g. buffer
// preservation across flush failures) own that policy themselves.
func (r *Runner) Run(ctx context.Context, model, prompt string, timeout time.Duration) (*Response, error) {
	if r == nil || r.Command == "" {
		return nil, fmt.Errorf("claude.Run: runner not configured: %w", ErrCommandNotFound)
	}
	if model == "" {
		return nil, fmt.Errorf("claude.Run: model must not be empty")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	args := []string{
		"-p", prompt,
		"--model", model,
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--max-turns", "1",
	}

	cmd := exec.CommandContext(runCtx, r.Command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Timeout is the most specific classification — check it first so a
	// killed-by-context process isn't misreported as a non-zero exit.
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%w after %s", ErrTimeout, timeout)
	}

	if err != nil {
		// exec.ErrNotFound fires when LookPath couldn't resolve a bare
		// name on PATH; fs.ErrNotExist covers fork/exec on an absolute
		// path that doesn't exist (ENOENT bubbles through PathError).
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrCommandNotFound, r.Command)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
			}
		}
		return nil, fmt.Errorf("claude.Run: %w", err)
	}

	resp, parseErr := ParseResponse(stdout.Bytes())
	if parseErr != nil {
		return nil, parseErr
	}
	if strings.TrimSpace(resp.Result) == "" {
		return nil, fmt.Errorf("%w (stdout %d bytes)", ErrEmptyResponse, len(stdout.Bytes()))
	}
	return resp, nil
}
