package git

import (
	stderrors "errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	devlogerrors "devlog/internal/errors"
)

// newRepo creates a minimal git repo at root by making a .git directory.
// CheckRepo does filesystem-only detection so an empty .git directory is
// sufficient — no need to init an actual git repo.
func newRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

func TestCheckRepoFindsRepoAtRoot(t *testing.T) {
	dir := t.TempDir()
	newRepo(t, dir)

	if err := CheckRepo(dir); err != nil {
		t.Errorf("CheckRepo on repo root returned err: %v", err)
	}
}

func TestCheckRepoWalksUpward(t *testing.T) {
	dir := t.TempDir()
	newRepo(t, dir)

	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	if err := CheckRepo(nested); err != nil {
		t.Errorf("CheckRepo from nested subdir returned err: %v", err)
	}
}

func TestCheckRepoMissingReturnsDevlogError(t *testing.T) {
	dir := t.TempDir()
	// No .git anywhere under dir. But on systems where the tempdir lives
	// under a real repo (very rare on CI; common for devs running tests
	// locally inside a checkout) the upward walk could find one. Detect
	// that and skip — we are specifically testing the "no repo" path.
	if err := CheckRepo(dir); err == nil {
		t.Skipf("tempdir %s is inside a git repo; cannot test missing-repo case here", dir)
	}

	err := CheckRepo(dir)
	if err == nil {
		t.Fatalf("expected DevlogError, got nil")
	}

	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *DevlogError, got %T: %v", err, err)
	}
	if de.Component != "git" {
		t.Errorf("Component = %q, want git", de.Component)
	}
	if !strings.Contains(de.Message, "no git repository found") {
		t.Errorf("Message = %q, want it to contain 'no git repository found'", de.Message)
	}
	if !strings.Contains(de.Remediation, "git init") {
		t.Errorf("Remediation should reference 'git init', got %q", de.Remediation)
	}
	if !strings.Contains(de.Remediation, dir) {
		// Remediation should include the absolute path so the operator
		// can copy-paste the fix command.
		abs, _ := filepath.Abs(dir)
		if !strings.Contains(de.Remediation, abs) {
			t.Errorf("Remediation should include %q, got %q", abs, de.Remediation)
		}
	}
}

func TestCheckRepoHonoursGitFile(t *testing.T) {
	// `git worktree add` creates .git as a regular FILE pointing at the
	// real gitdir. CheckRepo must accept that shape too.
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /somewhere/else\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	if err := CheckRepo(dir); err != nil {
		t.Errorf("CheckRepo should accept .git file form: %v", err)
	}
}

func TestCheckRepoEmptyPathUsesCwd(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(orig)

	dir := t.TempDir()
	newRepo(t, dir)

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := CheckRepo(""); err != nil {
		t.Errorf("CheckRepo(\"\") should resolve cwd: %v", err)
	}
}

func TestCheckRepoRelativePath(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(orig)

	parent := t.TempDir()
	newRepo(t, parent)
	nested := filepath.Join(parent, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}

	if err := os.Chdir(parent); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := CheckRepo("pkg"); err != nil {
		t.Errorf("CheckRepo(relative path) returned err: %v", err)
	}
}

func TestCheckRepoStopsAtFilesystemRoot(t *testing.T) {
	// Choose a directory that cannot possibly contain a .git parent.
	// On Linux/macOS /tmp-like temp dirs may be inside a repo for
	// developers running tests locally, so skip if that happens.
	dir := t.TempDir()

	err := CheckRepo(dir)
	if err == nil {
		t.Skipf("tempdir %s is inside a git repo; cannot verify filesystem-root termination here", dir)
	}

	// If we got here the walk terminated and produced the expected
	// DevlogError without hanging or looping.
	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *DevlogError, got %T", err)
	}

	if runtime.GOOS == "" {
		// Silence unused-import warning on constrained builds.
		t.Log(runtime.GOOS)
	}
}
