package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	derrors "devlog/internal/errors"
	"devlog/internal/hookinput"
	"devlog/internal/state"
)

// taskCaptureStdin is the reader task-capture reads from. Exposed as a
// package variable so tests can inject a prompt without spawning the binary.
var taskCaptureStdin = func() *os.File { return os.Stdin }

// TaskCapture implements `devlog task-capture`, invoked by Claude Code's
// UserPromptSubmit hook. The first prompt per session becomes the canonical
// task/goal in .devlog/task.md; every subsequent prompt is appended to
// task_updates.jsonl as a course correction.
//
// Per SPEC this hook must never block the working agent: all failures exit
// 0 and are logged to .devlog/errors.log instead of being surfaced on
// stderr. The only non-zero exit is the trivial "no stdin / Claude Code
// version mismatch" case, where even logging would be futile — even that
// one is still swallowed in production (see return 0 paths).
func TaskCapture(args []string) int {
	// cwd is our fallback errors-log directory when hook parsing fails
	// before we can read it from the payload.
	cwd, _ := os.Getwd()
	errorsLog := filepath.Join(cwd, ".devlog", "errors.log")

	raw, err := io.ReadAll(taskCaptureStdin())
	if err != nil {
		logNonFatal(errorsLog, err)
		return 0
	}

	// Extract cwd from the payload so we can locate .devlog/config.json
	// and look up the host before parsing the host-specific payload.
	if payloadCwd := extractPayloadCwd(raw); payloadCwd != "" {
		cwd = payloadCwd
	}
	devlogDir := resolveDevlogDir(cwd)
	errorsLog = filepath.Join(devlogDir, "errors.log")

	cfg, err := state.LoadConfig(filepath.Join(devlogDir, "config.json"))
	if err != nil {
		logNonFatal(errorsLog, err)
		return 0
	}

	ev, err := hookinput.Parse(cfg.Host, "UserPromptSubmit", raw)
	if err != nil {
		logNonFatal(errorsLog, err)
		return 0
	}
	if ev.Cwd != "" {
		// Re-resolve in case the host parser surfaced a different cwd
		// (rare, but keeps .devlog lookup consistent with downstream
		// consumers of the event).
		devlogDir = resolveDevlogDir(ev.Cwd)
		errorsLog = filepath.Join(devlogDir, "errors.log")
	}

	if strings.TrimSpace(ev.Prompt) == "" {
		logNonFatal(errorsLog,
			derrors.New("task-capture", "received UserPromptSubmit payload without a prompt field"))
		return 0
	}

	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		logNonFatal(errorsLog, derrors.Wrap("task-capture",
			fmt.Sprintf("create %s", devlogDir), err).
			WithRemediation(
				"Check that the project directory is writable and re-run\n"+
					"`devlog init` to create the .devlog/ directory.",
			))
		return 0
	}

	taskPath := filepath.Join(devlogDir, "task.md")
	if exists, err := fileExistsAndNonEmpty(taskPath); err != nil {
		logNonFatal(errorsLog, derrors.Wrap("task-capture",
			fmt.Sprintf("stat %s", taskPath), err))
		return 0
	} else if !exists {
		if err := writeTaskFile(taskPath, ev.Prompt); err != nil {
			logNonFatal(errorsLog, err)
			return 0
		}
		return 0
	}

	if err := appendTaskUpdate(devlogDir, ev); err != nil {
		logNonFatal(errorsLog, err)
		return 0
	}
	return 0
}

// resolveDevlogDir picks the devlog directory. Prefers the hook's cwd
// (authoritative for the working agent's project) and falls back to the
// process working directory when the payload omits it.
func resolveDevlogDir(hookCwd string) string {
	root := hookCwd
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	return filepath.Join(root, ".devlog")
}

// fileExistsAndNonEmpty returns true only when the file exists and has at
// least one byte of content. A zero-length task.md is treated as absent so
// a botched prior write (truncated but not populated) doesn't permanently
// divert new prompts into task_updates.jsonl.
func fileExistsAndNonEmpty(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Size() > 0, nil
}

// writeTaskFile writes prompt to path with 0644 permissions. The file is
// created atomically via CreateTemp + Rename so a crashed mid-write does
// not leave a half-populated task.md that future invocations would treat
// as a valid original task.
func writeTaskFile(path, prompt string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".task-*.md")
	if err != nil {
		return derrors.Wrap("task-capture",
			fmt.Sprintf("create temp in %s", dir), err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	body := prompt
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return derrors.Wrap("task-capture",
			fmt.Sprintf("write %s", tmpPath), err)
	}
	if err := tmp.Close(); err != nil {
		return derrors.Wrap("task-capture",
			fmt.Sprintf("close %s", tmpPath), err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return derrors.Wrap("task-capture",
			fmt.Sprintf("rename %s -> %s", tmpPath, path), err).
			WithRemediation(
				"Check that .devlog/ exists and is writable. Re-run: devlog init",
			)
	}
	committed = true
	return nil
}

// taskUpdateEntry is the JSON shape appended per subsequent user prompt.
// Kept small — the companion prompt includes these verbatim as course
// corrections, so tight schemas help keep context usage predictable.
type taskUpdateEntry struct {
	TS        string `json:"ts"`
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

// appendTaskUpdate appends one JSONL entry representing the new user
// prompt to task_updates.jsonl in devlogDir. Uses O_APPEND so concurrent
// writers don't interleave lines on POSIX filesystems.
func appendTaskUpdate(devlogDir string, ev *hookinput.Event) error {
	path := filepath.Join(devlogDir, "task_updates.jsonl")
	entry := taskUpdateEntry{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: ev.SessionID,
		Prompt:    ev.Prompt,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return derrors.Wrap("task-capture", "encode task update", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return derrors.Wrap("task-capture",
			fmt.Sprintf("open %s", path), err).
			WithRemediation(
				"Check that .devlog/ exists and is writable. Re-run: devlog init",
			)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return derrors.Wrap("task-capture",
			fmt.Sprintf("append %s", path), err)
	}
	return nil
}

// logNonFatal writes err to errorsLogPath and is a no-op on failure — the
// hook must still exit 0 even if the errors log itself is unwritable.
func logNonFatal(errorsLogPath string, err error) {
	if err == nil {
		return
	}
	if de, ok := err.(*derrors.DevlogError); ok {
		_ = de.WriteToLog(errorsLogPath)
		return
	}
	_ = derrors.Wrap("task-capture", "unexpected error", err).
		WriteToLog(errorsLogPath)
}
