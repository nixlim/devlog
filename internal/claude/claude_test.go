package claude_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlog/internal/claude"
	"devlog/internal/testutil"
)

// successEnvelope is a realistic `claude -p --output-format json` payload.
// Whitespace is preserved to match what claude actually emits.
const successEnvelope = `{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 1200,
  "duration_api_ms": 900,
  "num_turns": 1,
  "result": "Systematically increasing database timeouts to resolve a 500 error on /api/recommendations.",
  "session_id": "sess-xyz",
  "total_cost_usd": 0.0012,
  "model": "claude-haiku-4-5-20251001"
}`

func TestRunSuccess(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	if err := fc.SetResponse(successEnvelope, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}

	r := claude.New(fc.BinPath)
	resp, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "summarize these diffs", 10*time.Second)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("Run returned nil response with no error")
	}
	if resp.Type != "result" || resp.Subtype != "success" {
		t.Errorf("envelope discriminator wrong: type=%q subtype=%q", resp.Type, resp.Subtype)
	}
	if !strings.Contains(resp.Result, "database timeouts") {
		t.Errorf("Result missing expected text: %q", resp.Result)
	}
	if resp.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q, want haiku", resp.Model)
	}
	if resp.DurationMS != 1200 {
		t.Errorf("DurationMS = %d, want 1200", resp.DurationMS)
	}
	if len(resp.Raw) == 0 {
		t.Error("Raw should retain the stdout bytes")
	}
}

func TestRunNonZeroExitCarriesStderr(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	const stderr = "Error: model \"claude-haiku-4-5-20251001\" not available\n"
	if err := fc.SetResponse("", stderr, 1); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}

	r := claude.New(fc.BinPath)
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 10*time.Second)
	if err == nil {
		t.Fatal("expected error on exit code 1")
	}
	if !errors.Is(err, claude.ErrNonZeroExit) {
		t.Errorf("err should satisfy errors.Is(ErrNonZeroExit): %v", err)
	}
	var exitErr *claude.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err should be *ExitError: %v", err)
	}
	if exitErr.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", exitErr.ExitCode)
	}
	if !strings.Contains(exitErr.Stderr, "not available") {
		t.Errorf("Stderr missing expected content: %q", exitErr.Stderr)
	}
}

func TestRunCommandNotFound(t *testing.T) {
	r := claude.New("/definitely/does/not/exist/claude-binary-xyz")
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected command-not-found error")
	}
	if !errors.Is(err, claude.ErrCommandNotFound) {
		t.Errorf("err should satisfy errors.Is(ErrCommandNotFound): %v", err)
	}
}

func TestRunTimeout(t *testing.T) {
	// Use a shell-script "claude" that blocks on sleep. The script ignores
	// its positional args (which devlog will pass verbatim), so the runner
	// produces a process that cannot exit before the deadline fires.
	dir := t.TempDir()
	sleeper := filepath.Join(dir, "slow-claude")
	// `exec` lets the shell replace itself with sleep so SIGKILL from
	// exec.CommandContext reaches the actual blocking process (otherwise
	// the shell's stdio pipes stay held by the grandchild and cmd.Wait
	// hangs until sleep exits naturally — Go issue #24050).
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatalf("write sleeper: %v", err)
	}

	r := claude.New(sleeper)
	start := time.Now()
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 150*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, claude.ErrTimeout) {
		t.Errorf("err should satisfy errors.Is(ErrTimeout): %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run did not honor timeout: elapsed=%s", elapsed)
	}
}

func TestRunEmptyResponse(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	// Envelope with empty result — exit 0 but nothing usable.
	const emptyEnvelope = `{"type":"result","subtype":"success","result":"","model":"claude-haiku-4-5-20251001"}`
	if err := fc.SetResponse(emptyEnvelope, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	r := claude.New(fc.BinPath)
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected empty-response error")
	}
	if !errors.Is(err, claude.ErrEmptyResponse) {
		t.Errorf("err should satisfy errors.Is(ErrEmptyResponse): %v", err)
	}
}

func TestRunWhitespaceOnlyResponseIsEmpty(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	const whitespaceResult = `{"type":"result","result":"   \n\t  "}`
	if err := fc.SetResponse(whitespaceResult, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	r := claude.New(fc.BinPath)
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 5*time.Second)
	if !errors.Is(err, claude.ErrEmptyResponse) {
		t.Errorf("whitespace-only result should trigger empty-response: %v", err)
	}
}

func TestRunInvalidJSON(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	if err := fc.SetResponse("this is not JSON", "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	r := claude.New(fc.BinPath)
	_, err := r.Run(context.Background(), "claude-haiku-4-5-20251001", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected invalid-JSON error")
	}
	if !errors.Is(err, claude.ErrInvalidJSON) {
		t.Errorf("err should satisfy errors.Is(ErrInvalidJSON): %v", err)
	}
}

func TestRunRejectsEmptyModel(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	if err := fc.SetResponse(successEnvelope, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	r := claude.New(fc.BinPath)
	_, err := r.Run(context.Background(), "", "p", 5*time.Second)
	if err == nil {
		t.Fatal("expected empty-model error")
	}
}

func TestNewDefaultsToClaude(t *testing.T) {
	r := claude.New("")
	if r.Command != "claude" {
		t.Errorf("New(\"\") should default Command to \"claude\", got %q", r.Command)
	}
}

func TestParseResponseSuccess(t *testing.T) {
	resp, err := claude.ParseResponse([]byte(successEnvelope))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Result == "" {
		t.Error("Result should not be empty")
	}
	if resp.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q", resp.Model)
	}
	if len(resp.Raw) != len(successEnvelope) {
		t.Errorf("Raw length = %d, want %d", len(resp.Raw), len(successEnvelope))
	}
}

func TestParseResponseEmpty(t *testing.T) {
	_, err := claude.ParseResponse(nil)
	if !errors.Is(err, claude.ErrInvalidJSON) {
		t.Errorf("nil input should return ErrInvalidJSON: %v", err)
	}
}

func TestParseResponseMalformed(t *testing.T) {
	_, err := claude.ParseResponse([]byte("{not valid}"))
	if !errors.Is(err, claude.ErrInvalidJSON) {
		t.Errorf("malformed input should return ErrInvalidJSON: %v", err)
	}
}

func TestParseResponseRawIsolated(t *testing.T) {
	// Mutating the caller's buffer must not mutate Raw.
	buf := []byte(successEnvelope)
	resp, err := claude.ParseResponse(buf)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	buf[0] = 'X'
	if resp.Raw[0] == 'X' {
		t.Error("Raw should be defensively copied from the input buffer")
	}
}

func TestExitErrorMessage(t *testing.T) {
	e := &claude.ExitError{ExitCode: 7, Stderr: "boom\n"}
	msg := e.Error()
	if !strings.Contains(msg, "7") || !strings.Contains(msg, "boom") {
		t.Errorf("ExitError.Error() missing fields: %q", msg)
	}
	if !errors.Is(e, claude.ErrNonZeroExit) {
		t.Error("ExitError should satisfy errors.Is(ErrNonZeroExit)")
	}
}

func TestExitErrorBlankStderr(t *testing.T) {
	e := &claude.ExitError{ExitCode: 2, Stderr: "   \n  "}
	msg := e.Error()
	if strings.Contains(msg, ":") && strings.Contains(msg, "  ") {
		t.Errorf("ExitError with blank stderr should omit the colon prefix: %q", msg)
	}
}
