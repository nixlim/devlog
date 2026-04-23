package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	claudepkg "devlog/internal/claude"
	"devlog/internal/host"
	"devlog/internal/testutil"
)

const successEnvelope = `{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 1200,
  "duration_api_ms": 900,
  "num_turns": 1,
  "result": "host-interface pass-through works",
  "session_id": "sess-host",
  "total_cost_usd": 0.0012,
  "model": "claude-haiku-4-5-20251001"
}`

func TestClaudeHostName(t *testing.T) {
	h := &ClaudeHost{Command: "claude"}
	if h.Name() != "claude" {
		t.Errorf("Name() = %q, want %q", h.Name(), "claude")
	}

	// Registered constructor yields the same name — proves init() ran.
	reg, ok := host.Lookup("claude")
	if !ok {
		t.Fatal("host.Lookup(\"claude\") not registered")
	}
	if reg.Name() != "claude" {
		t.Errorf("registered host Name() = %q", reg.Name())
	}
}

func TestClaudeHostNormalizeModel(t *testing.T) {
	h := &ClaudeHost{Command: "claude"}
	for _, in := range []string{"", "claude-haiku-4-5-20251001", "sonnet-4"} {
		if got := h.NormalizeModel(in); got != in {
			t.Errorf("NormalizeModel(%q) = %q, want pass-through", in, got)
		}
	}
}

func TestClaudeHostRunLLM(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	if err := fc.SetResponse(successEnvelope, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}

	h := &ClaudeHost{Command: fc.BinPath}
	resp, err := h.RunLLM(context.Background(), "claude-haiku-4-5-20251001",
		"hello", 5*time.Second)
	if err != nil {
		t.Fatalf("RunLLM: %v", err)
	}
	if resp == nil {
		t.Fatal("RunLLM returned nil response with no error")
	}
	if resp.Type != "result" || resp.Subtype != "success" {
		t.Errorf("envelope wrong: type=%q subtype=%q", resp.Type, resp.Subtype)
	}
	if !strings.Contains(resp.Result, "pass-through") {
		t.Errorf("Result = %q", resp.Result)
	}
	if resp.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q", resp.Model)
	}
	if resp.DurationMS != 1200 {
		t.Errorf("DurationMS = %d", resp.DurationMS)
	}
	if len(resp.Raw) == 0 {
		t.Error("Raw should carry stdout bytes")
	}
}

func TestClaudeHostRunLLMTranslatesSentinels(t *testing.T) {
	h := &ClaudeHost{Command: "/definitely/does/not/exist/claude-xyz"}
	_, err := h.RunLLM(context.Background(), "claude-haiku-4-5-20251001",
		"p", 2*time.Second)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !errors.Is(err, host.ErrCommandNotFound) {
		t.Errorf("err should match host.ErrCommandNotFound: %v", err)
	}
	// Original claude sentinel still in the chain for diagnostic callers.
	if !errors.Is(err, claudepkg.ErrCommandNotFound) {
		t.Errorf("err should keep claude.ErrCommandNotFound in chain: %v", err)
	}
}

func TestClaudeHostRunLLMTranslatesExitError(t *testing.T) {
	fc := testutil.NewFakeClaude(t)
	const stderr = "Error: model unavailable\n"
	if err := fc.SetResponse("", stderr, 1); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	h := &ClaudeHost{Command: fc.BinPath}
	_, err := h.RunLLM(context.Background(), "claude-haiku-4-5-20251001",
		"p", 5*time.Second)
	if err == nil {
		t.Fatal("expected exit error")
	}
	if !errors.Is(err, host.ErrNonZeroExit) {
		t.Errorf("err should match host.ErrNonZeroExit: %v", err)
	}
	var hx *host.ExitError
	if !errors.As(err, &hx) {
		t.Fatalf("err should be *host.ExitError: %v", err)
	}
	if hx.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", hx.ExitCode)
	}
	if !strings.Contains(hx.Stderr, "model unavailable") {
		t.Errorf("stderr not preserved: %q", hx.Stderr)
	}
}

func TestClaudeHostDetect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is POSIX only")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho 'claude 1.2.3'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	h := &ClaudeHost{Command: stub}
	ok, version, err := h.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect should report true when the binary exists")
	}
	if !strings.Contains(version, "1.2.3") {
		t.Errorf("version = %q, want to contain 1.2.3", version)
	}

	// Missing binary → (false, "", nil).
	missing := &ClaudeHost{Command: "/nope/does/not/exist/claude-xyz"}
	ok, version, err = missing.Detect()
	if err != nil {
		t.Fatalf("Detect(missing): %v", err)
	}
	if ok {
		t.Error("Detect should report false for missing binary")
	}
	if version != "" {
		t.Errorf("version for missing = %q, want empty", version)
	}
}

func TestClaudeHostSetCommand(t *testing.T) {
	h := &ClaudeHost{Command: "claude"}
	h.SetCommand("/custom/path/claude")
	if h.Command != "/custom/path/claude" {
		t.Errorf("SetCommand: Command = %q", h.Command)
	}
	// Empty falls back to "claude" so downstream Runner.Run doesn't panic.
	h.SetCommand("")
	if h.Command != "claude" {
		t.Errorf("SetCommand(\"\"): Command = %q, want \"claude\"", h.Command)
	}

	// And ClaudeHost should satisfy host.Configurable.
	var _ host.Configurable = (*ClaudeHost)(nil)
}
