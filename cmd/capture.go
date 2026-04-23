package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"devlog/internal/buffer"
	derrors "devlog/internal/errors"
	"devlog/internal/git"
	"devlog/internal/hook"
	"devlog/internal/state"
)

// captureStdin is indirected so tests can inject hook payloads without
// having to exec the binary.
var captureStdin = func() *os.File { return os.Stdin }

// captureFlushSpawner is indirected for tests that want to observe when
// a background flush would have been launched. The production
// implementation is spawnFlushDetached (below).
var captureFlushSpawner = spawnFlushDetached

// Capture implements `devlog capture`, invoked by Claude Code's
// PostToolUse hook on Edit/Write/Bash tools.
//
// The hook runs inline on every tool call so it MUST complete quickly
// (<200ms target) and MUST never fail the working agent — any internal
// error is logged to errors.log and the process still exits 0.
//
// Tool handling per SPEC:
//
//	Edit:  record file_path plus a 200-char-max old→new detail string
//	Write: record file_path plus the content length
//	Bash:  run `git diff --stat HEAD` to detect tree changes; when the
//	       tree changed, capture `git diff HEAD` truncated to 2000 chars.
//	       When the tree is clean, record the command with changed=false.
//
// Once the buffer hits cfg.BufferSize entries, the hook spawns a detached
// `devlog flush` and resets the counter.
func Capture(args []string) int {
	cwd, _ := os.Getwd()
	errorsLog := filepath.Join(cwd, ".devlog", "errors.log")

	in, err := hook.ParseInput(captureStdin())
	if err != nil {
		captureLogNonFatal(errorsLog, err)
		return 0
	}

	devlogDir := resolveDevlogDir(in.Cwd)
	errorsLog = filepath.Join(devlogDir, "errors.log")

	cfg, err := state.LoadConfig(filepath.Join(devlogDir, "config.json"))
	if err != nil {
		captureLogNonFatal(errorsLog, err)
		return 0
	}
	if !cfg.IsEnabled() {
		return 0
	}

	entry, err := buildBufferEntry(in, cfg)
	if err != nil {
		captureLogNonFatal(errorsLog, err)
		return 0
	}
	if entry == nil {
		// Tool is not one we buffer (e.g. the capture hook matcher was
		// widened beyond Edit|Write|Bash).
		return 0
	}

	bufferPath := filepath.Join(devlogDir, "buffer.jsonl")
	statePath := filepath.Join(devlogDir, "state.json")

	// Under the state lock: assign seq, stamp session_id, append to
	// buffer, then decide whether to trigger a flush. The append is
	// inside the lock so that seq is only persisted on successful write.
	var shouldFlush bool
	err = state.Update(statePath, func(s *state.State) error {
		s.BufferSeq++
		entry.Seq = s.BufferSeq
		if entry.SessionID == "" {
			entry.SessionID = s.SessionID
		}

		if err := buffer.Append(bufferPath, *entry); err != nil {
			return fmt.Errorf("append buffer: %w", err)
		}

		s.BufferCount++
		if s.BufferCount >= cfg.BufferSize && !s.FlushInProgress {
			shouldFlush = true
			s.FlushInProgress = true
			s.BufferCount = 0
		}
		return nil
	})
	if err != nil {
		captureLogNonFatal(errorsLog, derrors.Wrap("capture", "update state", err))
		return 0
	}

	if shouldFlush {
		if err := captureFlushSpawner(in.Cwd); err != nil {
			captureLogNonFatal(errorsLog, derrors.Wrap("capture", "spawn flush", err))
			// Roll back the FlushInProgress flag so the next capture can
			// retry. Losing a BufferCount reset is fine — the next
			// threshold crossing will catch up.
			_ = state.Update(statePath, func(s *state.State) error {
				s.FlushInProgress = false
				return nil
			})
		}
	}
	return 0
}

// buildBufferEntry maps a hook Input into a buffer.Entry. Returns
// (nil, nil) for tools we ignore so the caller can short-circuit without
// special-casing.
func buildBufferEntry(in *hook.Input, cfg *state.Config) (*buffer.Entry, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	switch in.ToolName {
	case "Edit":
		return &buffer.Entry{
			TS:        now,
			Tool:      "Edit",
			File:      in.ToolInput.FilePath,
			Detail:    summarizeEdit(in.ToolInput.OldString, in.ToolInput.NewString, cfg.MaxDetailChars),
			DiffLines: editDiffLines(in.ToolInput.OldString, in.ToolInput.NewString),
			Changed:   true,
		}, nil

	case "Write":
		content := in.ToolInput.Content
		return &buffer.Entry{
			TS:        now,
			Tool:      "Write",
			File:      in.ToolInput.FilePath,
			Detail:    fmt.Sprintf("wrote %d bytes", len(content)),
			DiffLines: strings.Count(content, "\n"),
			Changed:   true,
		}, nil

	case "Bash":
		return buildBashEntry(in, cfg, now)

	default:
		return nil, nil
	}
}

// summarizeEdit renders "old: '...' → new: '...'" with per-side
// truncation to maxChars (as configured by max_detail_chars; default 200
// from SPEC). Newlines in old/new strings are squashed to spaces so the
// summary fits on one log line.
func summarizeEdit(oldStr, newStr string, maxChars int) string {
	return fmt.Sprintf("old: %q → new: %q",
		truncateRunes(flattenWS(oldStr), maxChars),
		truncateRunes(flattenWS(newStr), maxChars),
	)
}

// editDiffLines gives a rough line-delta count for an Edit, computed as
// the larger of the two strings' newline counts plus one. That matches
// the intuition that a single-line replacement counts as 1 line.
func editDiffLines(oldStr, newStr string) int {
	o := strings.Count(oldStr, "\n") + 1
	n := strings.Count(newStr, "\n") + 1
	if n > o {
		return n
	}
	return o
}

// buildBashEntry records a Bash tool call. If the working tree changed
// as a result it captures the diff (truncated), otherwise it records the
// command with Changed=false. git errors are surfaced as DevlogErrors so
// the caller can log them once — the buffer entry is still returned so
// the agent's action is visible in the trajectory.
func buildBashEntry(in *hook.Input, cfg *state.Config, now string) (*buffer.Entry, error) {
	entry := &buffer.Entry{
		TS:     now,
		Tool:   "Bash",
		Detail: truncateRunes(flattenWS(in.ToolInput.Command), cfg.MaxDetailChars),
	}

	stat, err := git.DiffStat(in.Cwd)
	if err != nil {
		// Non-fatal: surface the diff failure but still record the
		// command so the trajectory reflects what the agent tried.
		entry.Changed = false
		entry.DiffLines = 0
		return entry, err
	}

	if !stat.Changed {
		entry.Changed = false
		entry.DiffLines = 0
		return entry, nil
	}

	diffOut, diffErr := git.Diff(in.Cwd, cfg.MaxDiffChars)
	entry.Changed = true
	if diffErr != nil {
		// Same treatment — keep the command, report the diff failure.
		return entry, diffErr
	}

	entry.DiffLines = strings.Count(diffOut, "\n")
	// If the command itself had text, prefer to keep that as Detail and
	// append the diff summary. Otherwise use the diff as the detail.
	if strings.TrimSpace(entry.Detail) == "" {
		entry.Detail = truncateRunes(diffOut, cfg.MaxDiffChars)
	} else {
		combined := entry.Detail + "\n" + truncateRunes(diffOut, cfg.MaxDiffChars-len(entry.Detail)-1)
		entry.Detail = truncateRunes(combined, cfg.MaxDiffChars)
	}
	return entry, nil
}

// truncateRunes returns s clipped to at most maxRunes characters. When
// truncation happens the suffix " …" is appended so readers can see the
// value was cut. maxRunes <= 0 disables truncation.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + " …"
}

// flattenWS replaces any run of CR/LF/tab with a single space so a
// multi-line edit fragment does not explode the detail column.
func flattenWS(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
}

// captureLogNonFatal writes err to errorsLogPath with the "capture"
// component when err is not already a DevlogError.
func captureLogNonFatal(errorsLogPath string, err error) {
	if err == nil {
		return
	}
	var de *derrors.DevlogError
	if errors.As(err, &de) {
		_ = de.WriteToLog(errorsLogPath)
		return
	}
	_ = derrors.Wrap("capture", "unexpected error", err).WriteToLog(errorsLogPath)
}

// spawnFlushDetached launches `devlog flush` as an asynchronous child
// process with its own process group so it survives the current hook
// returning. Stdin/stdout/stderr are wired to /dev/null because the
// flush command does not consume or produce anything the hook cares
// about.
func spawnFlushDetached(cwd string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "flush")
	cmd.Dir = cwd
	// Detach: new session + pgid so SIGHUP when the shell closes does
	// not take the flush down.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		return err
	}
	// Release the child — we intentionally do not Wait().
	go func() { _ = cmd.Process.Release() }()
	_ = devnull.Close()
	return nil
}
