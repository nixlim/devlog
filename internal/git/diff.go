package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	devlogerrors "devlog/internal/errors"
)

// diffTimeout caps every git invocation. The capture hook fires on every
// Edit/Write/Bash tool call; a hung git process would freeze the working
// agent, so 5s is generous but firm. Errors due to timeout surface as
// context.DeadlineExceeded wrapped in a DevlogError.
const diffTimeout = 5 * time.Second

// DiffStatResult is what DiffStat reports back: whether the working
// tree differs from HEAD and, when it does, the list of affected paths.
type DiffStatResult struct {
	Changed bool
	Files   []string
}

// DiffStat runs `git -C cwd diff --stat HEAD` and interprets the output.
// A clean working tree yields Changed=false and Files==nil. Any
// untracked-but-modified tree yields Changed=true with Files populated
// from the --stat output.
//
// The spec (PostToolUse hook on Bash) specifically calls for
// `git diff --stat HEAD` rather than `git status`, because we only care
// about actually-tracked file changes after a Bash tool call.
func DiffStat(cwd string) (DiffStatResult, error) {
	out, err := runGit(cwd, "diff", "--stat", "HEAD")
	if err != nil {
		return DiffStatResult{}, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return DiffStatResult{}, nil
	}
	return DiffStatResult{Changed: true, Files: parseDiffStatFiles(trimmed)}, nil
}

// parseDiffStatFiles extracts the modified file names from the output of
// `git diff --stat`. Example line:
//
//	src/api/handler.go | 2 +-
//
// The final summary line (" N files changed, ...") is skipped.
func parseDiffStatFiles(output string) []string {
	lines := strings.Split(output, "\n")
	var files []string
	for i, line := range lines {
		// Last line is the "N file[s] changed" summary.
		if i == len(lines)-1 && strings.Contains(line, " changed") {
			continue
		}
		pipe := strings.Index(line, "|")
		if pipe <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:pipe])
		if name == "" {
			continue
		}
		files = append(files, name)
	}
	return files
}

// Diff runs `git -C cwd diff HEAD` and returns the textual diff. When
// maxChars > 0 and the diff exceeds that many bytes, the result is cut
// and a visible marker is appended so downstream consumers (prompts,
// archives) know the content was truncated.
//
// maxChars <= 0 disables truncation. The caller is responsible for
// configuring a sensible cap — devlog's default from SPEC is 2000.
func Diff(cwd string, maxChars int) (string, error) {
	out, err := runGit(cwd, "diff", "HEAD")
	if err != nil {
		return "", err
	}
	if maxChars > 0 && len(out) > maxChars {
		return out[:maxChars] + fmt.Sprintf("\n… [truncated at %d chars]\n", maxChars), nil
	}
	return out, nil
}

// runGit executes `git -C cwd <args...>` with the standard devlog
// timeout and converts any non-zero exit into a DevlogError whose
// remediation matches SPEC.md's "git diff failed" example.
func runGit(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()

	fullArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}

	exitCode := -1
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	}

	cmdStr := "git " + strings.Join(fullArgs, " ")
	msg := fmt.Sprintf("%s failed (exit code %d) in %s", cmdStr, exitCode, cwd)
	remediation := fmt.Sprintf(
		"This usually means the git repository is corrupted or a git operation is in progress.\n"+
			"Check: git status\n\n"+
			"stderr: %s\n\n"+
			"Diff capture skipped for this tool call. DevLog will resume on next call.",
		strings.TrimSpace(stderr.String()),
	)
	return "", devlogerrors.Wrap("git", msg, err).WithRemediation(remediation)
}
