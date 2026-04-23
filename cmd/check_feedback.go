package cmd

import (
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	derrors "devlog/internal/errors"
	"devlog/internal/feedback"
)

// CheckFeedback implements `devlog check-feedback` — the PreToolUse hook.
//
// Fires before every tool call the working agent makes, so it must be near-
// instant (SPEC §5 budget: <50ms). The common path is two syscalls: a
// Getwd and a Stat/Open on feedback.md that reports a missing or empty
// file; the command exits 0 with no output.
//
// When feedback.md has content, the hook prints it to stdout (Claude Code
// injects stdout into the agent's context), appends a JSONL entry to
// feedback_archive.jsonl, and truncates feedback.md to zero bytes.
//
// Error contract: this hook NEVER blocks the working agent. Any internal
// failure is logged to .devlog/errors.log and the command exits 0 with no
// stdout so Claude Code treats the invocation as a no-op.
func CheckFeedback(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), checkFeedbackUsage)
		return 0
	}

	devlogDir := resolveDevlogDirFromStdin()
	if devlogDir == "" {
		return 0
	}

	feedbackPath := filepath.Join(devlogDir, "feedback.md")
	archivePath := filepath.Join(devlogDir, "feedback_archive.jsonl")
	errLog := filepath.Join(devlogDir, "errors.log")

	content, err := feedback.Truncate(feedbackPath, archivePath)
	if err != nil {
		recordHookError(errLog, "check-feedback", err)
		return 0
	}
	if content == "" {
		return 0
	}
	fmt.Fprint(stdout(), content)
	return 0
}

const checkFeedbackUsage = `devlog check-feedback — emit and archive pending companion feedback

Usage:
    devlog check-feedback

Used as the PreToolUse hook. When .devlog/feedback.md has content it is
printed to stdout (which Claude Code injects into the working agent's
context), archived to .devlog/feedback_archive.jsonl, and truncated.

All errors are silent — this command never blocks the working agent.
`

// checkFeedbackStdin is indirected so tests can inject payloads.
var checkFeedbackStdin = func() *os.File { return os.Stdin }

// resolveDevlogDirFromStdin reads the hook's stdin payload to extract a
// cwd field (injected by the OpenCode TS plugin). Falls back to the
// process working directory when the payload is absent or missing the
// field — this is the normal path for Claude Code, which pipes a payload
// but cwd comes from the process environment.
func resolveDevlogDirFromStdin() string {
	raw, _ := io.ReadAll(checkFeedbackStdin())
	cwd := extractPayloadCwd(raw)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	return findDevlogDir(cwd)
}

// recordHookError appends a structured error line to logPath. The errors
// package guarantees each call issues a single atomic write, so concurrent
// hook invocations won't interleave lines. Write failures are swallowed —
// a broken errors.log must not break the hook.
func recordHookError(logPath, component string, err error) {
	if logPath == "" || err == nil {
		return
	}
	// Make sure the directory exists; ignore mkdir errors and let
	// WriteToLog surface the real issue (which we then swallow too).
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)

	var de *derrors.DevlogError
	if stderrors.As(err, &de) {
		_ = de.WriteToLog(logPath)
		return
	}
	_ = derrors.Wrap(component, "hook failed", err).WriteToLog(logPath)
}
