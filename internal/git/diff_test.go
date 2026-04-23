package git

import (
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	devlogerrors "devlog/internal/errors"
)

// setupRepo creates a fresh git repo in a temp dir, makes a committed
// file, and returns the repo path. The repo is configured with a
// throwaway identity so `git commit` works regardless of host git
// config. If git is not installed the test is skipped — diff.go is
// fundamentally a wrapper around the git binary and there is nothing
// meaningful to assert without it.
func setupRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH; skipping git-dependent test")
	}

	dir := t.TempDir()
	runOrFail(t, dir, "init")
	runOrFail(t, dir, "config", "user.email", "devlog-test@example.invalid")
	runOrFail(t, dir, "config", "user.name", "devlog-test")
	runOrFail(t, dir, "config", "commit.gpgsign", "false")

	initial := filepath.Join(dir, "handler.go")
	if err := os.WriteFile(initial, []byte("package main\n\nfunc Handler() {}\n"), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	runOrFail(t, dir, "add", "handler.go")
	runOrFail(t, dir, "commit", "-m", "initial")

	return dir
}

func runOrFail(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Ensure deterministic default branch behaviour across git versions.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v — output: %s", strings.Join(args, " "), err, string(out))
	}
}

func TestDiffStatCleanRepo(t *testing.T) {
	dir := setupRepo(t)
	got, err := DiffStat(dir)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if got.Changed {
		t.Errorf("clean repo should have Changed=false, got %+v", got)
	}
	if len(got.Files) != 0 {
		t.Errorf("clean repo should have no Files, got %v", got.Files)
	}
}

func TestDiffStatModifiedFile(t *testing.T) {
	dir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "handler.go"),
		[]byte("package main\n\nfunc Handler() { println(\"hello\") }\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	got, err := DiffStat(dir)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !got.Changed {
		t.Errorf("modified repo should have Changed=true, got %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0] != "handler.go" {
		t.Errorf("Files = %v, want [handler.go]", got.Files)
	}
}

func TestDiffStatMultipleFiles(t *testing.T) {
	dir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("changed a\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	// Add a second committed file, then modify it too.
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write other: %v", err)
	}
	runOrFail(t, dir, "add", "other.go")
	runOrFail(t, dir, "commit", "-m", "add other")
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main\n// edit\n"), 0o644); err != nil {
		t.Fatalf("modify other: %v", err)
	}

	got, err := DiffStat(dir)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !got.Changed {
		t.Errorf("expected Changed=true")
	}
	if len(got.Files) < 2 {
		t.Errorf("expected at least 2 files, got %v", got.Files)
	}
}

func TestDiffReturnsDiff(t *testing.T) {
	dir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "handler.go"),
		[]byte("package main\n\nfunc Handler() { println(\"x\") }\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := Diff(dir, 0)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "handler.go") {
		t.Errorf("diff output should mention handler.go:\n%s", out)
	}
	if !strings.Contains(out, "+func Handler() { println(\"x\") }") {
		t.Errorf("diff output should contain added line:\n%s", out)
	}
}

func TestDiffTruncation(t *testing.T) {
	dir := setupRepo(t)

	// Write enough content to exceed the tiny maxChars we'll pass.
	big := strings.Repeat("extra content\n", 200)
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte(big), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := Diff(dir, 64)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "[truncated at 64 chars]") {
		t.Errorf("expected truncation marker, got:\n%s", out)
	}
	// The truncation marker itself is outside the cap — only the *diff
	// portion* must respect it.
	markerStart := strings.Index(out, "\n… [truncated")
	if markerStart < 0 {
		t.Fatalf("truncation marker not found in %q", out)
	}
	if markerStart > 64 {
		t.Errorf("diff portion exceeds cap: cut at %d, want <=64", markerStart)
	}
}

func TestDiffNoTruncationWhenSmallEnough(t *testing.T) {
	dir := setupRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("small change\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := Diff(dir, 10_000)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(out, "[truncated") {
		t.Errorf("small diff should not be truncated, got:\n%s", out)
	}
}

func TestDiffNonGitDirectoryReturnsDevlogError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir() // not a git repo

	_, err := DiffStat(dir)
	if err == nil {
		t.Fatalf("expected error for non-repo")
	}
	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *DevlogError, got %T: %v", err, err)
	}
	if de.Component != "git" {
		t.Errorf("Component = %q, want git", de.Component)
	}
	if !strings.Contains(de.Message, "exit code") {
		t.Errorf("message should include exit code: %q", de.Message)
	}
	if !strings.Contains(de.Remediation, "Check: git status") {
		t.Errorf("remediation should include 'Check: git status', got:\n%s", de.Remediation)
	}
}

func TestDiffStatOutputParsing(t *testing.T) {
	// Unit test the parser directly so it is covered even if the git
	// binary output layout drifts.
	sample := ` src/api/handler.go | 2 +-
 src/api/other.go   | 1 +
 2 files changed, 2 insertions(+), 1 deletion(-)`
	got := parseDiffStatFiles(sample)
	want := []string{"src/api/handler.go", "src/api/other.go"}
	if len(got) != len(want) {
		t.Fatalf("parsed %d files, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
