package host

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by Host.RunLLM. Callers classify failures via
// errors.Is so remediation messages can be host-agnostic. Concrete hosts
// are responsible for mapping their underlying errors onto these so that
// cmd/ code never imports a host-specific package just for sentinel
// values.
var (
	ErrCommandNotFound = errors.New("host: command not found")
	ErrTimeout         = errors.New("host: invocation timed out")
	ErrNonZeroExit     = errors.New("host: exited non-zero")
	ErrEmptyResponse   = errors.New("host: returned empty response")
	ErrInvalidJSON     = errors.New("host: output is not valid JSON")
)

// ExitError carries the exit code and stderr from a non-zero host
// invocation. Unwraps to ErrNonZeroExit so both the specific
// errors.As(err, &*ExitError) and generic errors.Is(err, ErrNonZeroExit)
// paths work.
type ExitError struct {
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	s := strings.TrimSpace(e.Stderr)
	if s == "" {
		return fmt.Sprintf("host: exited with code %d", e.ExitCode)
	}
	return fmt.Sprintf("host: exited with code %d: %s", e.ExitCode, s)
}

func (e *ExitError) Unwrap() error { return ErrNonZeroExit }
