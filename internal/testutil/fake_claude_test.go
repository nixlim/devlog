package testutil

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFakeClaude_ScriptedStdoutAndExit(t *testing.T) {
	fc := NewFakeClaude(t)
	const payload = `{"type":"result","subtype":"success","result":"hello"}`
	if err := fc.SetResponse(payload, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}

	cmd := exec.Command(fc.BinPath, "-p", "ignored", "--model", "claude-haiku-4-5-20251001")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("exec fake claude: %v", err)
	}
	if string(out) != payload {
		t.Errorf("stdout: got %q, want %q", out, payload)
	}
}

func TestFakeClaude_ScriptedFailure(t *testing.T) {
	fc := NewFakeClaude(t)
	if err := fc.SetResponse("", "model unavailable\n", 1); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}

	cmd := exec.Command(fc.BinPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit, got success; output=%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected error type %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code: got %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), "model unavailable") {
		t.Errorf("expected stderr to include scripted message, got %q", out)
	}
}

func TestFakeClaude_PrependPath(t *testing.T) {
	fc := NewFakeClaude(t)
	if err := fc.SetResponse(`{"ok":true}`, "", 0); err != nil {
		t.Fatalf("SetResponse: %v", err)
	}
	fc.PrependPath(t)

	// Resolve via PATH rather than absolute path.
	resolved, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("LookPath claude: %v", err)
	}
	if resolved != fc.BinPath {
		t.Fatalf("PATH did not resolve to stub: got %q, want %q", resolved, fc.BinPath)
	}

	out, err := exec.Command("claude", "-p", "hi").Output()
	if err != nil {
		t.Fatalf("exec by name: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"ok":true}` {
		t.Errorf("stdout: got %q, want %q", out, `{"ok":true}`)
	}
}

func TestFakeClaude_IsolatedPerInstance(t *testing.T) {
	a := NewFakeClaude(t)
	b := NewFakeClaude(t)

	if err := a.SetResponse("A", "", 0); err != nil {
		t.Fatalf("SetResponse A: %v", err)
	}
	if err := b.SetResponse("B", "", 0); err != nil {
		t.Fatalf("SetResponse B: %v", err)
	}

	outA, err := exec.Command(a.BinPath).Output()
	if err != nil {
		t.Fatalf("exec A: %v", err)
	}
	outB, err := exec.Command(b.BinPath).Output()
	if err != nil {
		t.Fatalf("exec B: %v", err)
	}
	if string(outA) != "A" || string(outB) != "B" {
		t.Errorf("instances not isolated: A=%q B=%q", outA, outB)
	}
}

func TestFakeClaude_DefaultResponseIsErroring(t *testing.T) {
	// A freshly-created FakeClaude with no SetResponse should still fail loudly
	// so that misconfigured tests give useful errors rather than silent success.
	fc := NewFakeClaude(t)
	out, err := exec.Command(fc.BinPath).CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit by default; output=%s", out)
	}
	if !strings.Contains(string(out), "SetResponse") {
		t.Errorf("default response should mention SetResponse; got %q", out)
	}
}
