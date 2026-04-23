package cmd

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	derrors "devlog/internal/errors"
	"devlog/internal/hookinput"
	"devlog/internal/state"
)

// TaskToolCapture implements `devlog task-tool-capture` — the PostToolUse
// hook wired to TaskCreate and TaskUpdate. Each invocation appends one
// JSON line describing the tool call to .devlog/tasks.jsonl so the
// companion can later cross-reference the agent's own task breakdown
// with the dev log narrative.
//
// The hook command MUST always exit 0 — the SPEC contract is that
// devlog never blocks the working agent, even when it is itself broken.
// Every error therefore writes to .devlog/errors.log (best effort) and
// still returns 0.
func TaskToolCapture(args []string) int {
	return taskToolCaptureImpl(os.Stdin)
}

// taskToolCaptureImpl is the reader-driven core of TaskToolCapture.
// Split out so tests can feed payloads without hijacking os.Stdin.
func taskToolCaptureImpl(r io.Reader) int {
	raw, err := io.ReadAll(r)
	if err != nil {
		writeHookErrorBestEffort("task-tool-capture", err, "")
		return 0
	}

	// Find cwd before parsing so we can locate .devlog/config.json and
	// look up the configured host; the host tells us which parser to use.
	cwd := extractPayloadCwd(raw)
	if cwd == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			cwd = wd
		}
	}
	devlogDir := findDevlogDir(cwd)
	errorsPath := filepath.Join(devlogDir, "errors.log")

	cfg, err := state.LoadConfig(filepath.Join(devlogDir, "config.json"))
	if err != nil {
		_ = derrors.Wrap("task-tool-capture", "load config", err).
			WriteToLog(errorsPath)
		return 0
	}

	ev, err := hookinput.Parse(cfg.Host, "PostToolUse", raw)
	if err != nil {
		writeHookErrorBestEffort("task-tool-capture", err, cwd)
		return 0
	}

	// Anything that isn't a Task tool is a no-op — no file created, no
	// log entry. This keeps tasks.jsonl focused on the agent's task
	// breakdown only.
	if ev.ToolName != "TaskCreate" && ev.ToolName != "TaskUpdate" {
		return 0
	}

	if ev.Cwd != "" {
		devlogDir = findDevlogDir(ev.Cwd)
		errorsPath = filepath.Join(devlogDir, "errors.log")
	}
	tasksPath := filepath.Join(devlogDir, "tasks.jsonl")

	entry := struct {
		TS        string          `json:"ts"`
		Tool      string          `json:"tool"`
		ToolInput json.RawMessage `json:"tool_input"`
	}{
		TS:        time.Now().UTC().Format(time.RFC3339),
		Tool:      ev.ToolName,
		ToolInput: ev.RawToolInput,
	}
	if len(entry.ToolInput) == 0 {
		entry.ToolInput = json.RawMessage("null")
	}

	data, err := json.Marshal(entry)
	if err != nil {
		_ = derrors.Wrap("task-tool-capture", "encode task entry", err).
			WriteToLog(errorsPath)
		return 0
	}
	data = append(data, '\n')

	if err := appendWithFlock(tasksPath, data); err != nil {
		_ = derrors.Wrap("task-tool-capture",
			fmt.Sprintf("append to %s", tasksPath), err).
			WriteToLog(errorsPath)
	}
	return 0
}

// writeHookErrorBestEffort attempts to route err to the project's
// errors.log. When the hook input has not parsed yet we don't know cwd —
// the one thing we can try is the current working directory. If that
// yields no .devlog dir we give up silently, since the alternative
// (polluting stderr) would leak into the working agent's tool output.
func writeHookErrorBestEffort(component string, err error, cwd string) {
	var de *derrors.DevlogError
	if !stderrors.As(err, &de) {
		de = derrors.Wrap(component, "hook failed", err)
	}
	if cwd == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			cwd = wd
		}
	}
	if cwd == "" {
		return
	}
	_ = de.WriteToLog(filepath.Join(findDevlogDir(cwd), "errors.log"))
}

// appendWithFlock writes data to path with an exclusive cross-process
// flock held on a sidecar <path>.lock file. Same pattern as
// internal/state and internal/buffer — keeping it local avoids a
// circular-ish dependency and lets each hook own its atomicity.
func appendWithFlock(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lockF, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lockF.Close()
	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
