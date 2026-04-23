package cmd

import (
	"context"
	stderrors "errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
	derrors "devlog/internal/errors"
	"devlog/internal/host"
	"devlog/internal/prompt"
	"devlog/internal/state"
)

// flushLLMRunner resolves the LLM backend for the summariser. Package-
// level so tests can swap in a stub without compiling a real claude
// binary. Returns nil if no host is registered under the configured name;
// the caller surfaces that as a classifiable error.
var flushLLMRunner = func(cfg *state.Config) llmRunner {
	h, ok := host.Lookup(cfg.Host)
	if !ok {
		return nil
	}
	if c, ok := h.(host.Configurable); ok {
		c.SetCommand(cfg.HostCommand)
	}
	return h
}

// llmRunner is the narrow surface flush depends on. host.Host satisfies
// it today; tests provide their own implementations without having to
// stub out install/detect.
type llmRunner interface {
	RunLLM(ctx context.Context, model, prompt string, timeout time.Duration) (*host.Response, error)
}

// flushCompanionSpawner launches `devlog companion` in the background.
// Var so tests can observe the spawn without actually forking.
var flushCompanionSpawner = defaultCompanionSpawner

// flushNow gives tests a stable clock.
var flushNow = func() time.Time { return time.Now().UTC() }

// Flush implements `devlog flush`. It drains buffer.jsonl through the
// Haiku summariser into log.jsonl and (when the log-entry threshold is
// crossed) spawns `devlog companion` in the background.
//
// Concurrency: the state.flush_in_progress flag is set under the state
// mutex before any work begins and cleared in a defer afterwards. A
// concurrent invocation that finds the flag already set exits 0 without
// touching the buffer — this is the safe, invariant-preserving path for
// the capture hook's fire-and-forget spawn.
//
// Error policy: the summariser is the only step that can fail without
// user intervention; when it does, the buffer is NOT archived so the
// next flush retries the same entries. State's flush_in_progress flag is
// always cleared on return so a failed flush doesn't permanently wedge
// the pipeline.
func Flush(args []string) int {
	fs := flag.NewFlagSet("flush", flag.ContinueOnError)
	fs.SetOutput(stderr())
	dryRun := fs.Bool("dry-run", false, "print the prompt that would be sent to Haiku, then exit")
	projectDir := fs.String("project", "", "project root (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveProjectRoot(*projectDir)
	if err != nil {
		printErr(err)
		return 1
	}
	paths := devlogPaths(root)

	cfg, err := loadConfigForFlush(paths.configPath)
	if err != nil {
		printErr(err)
		return 1
	}
	if !cfg.IsEnabled() {
		return 0
	}

	if *dryRun {
		return flushDryRun(paths, cfg)
	}

	return flushExecute(paths, cfg, root)
}

// devlogLayout holds the file paths flush touches. Grouping them at the
// top of the call flow avoids re-typing filepath.Join in every helper.
type devlogLayout struct {
	dir         string
	statePath   string
	configPath  string
	bufferPath  string
	archivePath string
	logPath     string
	taskPath    string
	errorsLog   string
}

func devlogPaths(root string) devlogLayout {
	dir := filepath.Join(root, ".devlog")
	return devlogLayout{
		dir:         dir,
		statePath:   filepath.Join(dir, "state.json"),
		configPath:  filepath.Join(dir, "config.json"),
		bufferPath:  filepath.Join(dir, "buffer.jsonl"),
		archivePath: filepath.Join(dir, "buffer_archive.jsonl"),
		logPath:     filepath.Join(dir, "log.jsonl"),
		taskPath:    filepath.Join(dir, "task.md"),
		errorsLog:   filepath.Join(dir, "errors.log"),
	}
}

// loadConfigForFlush loads config.json, applies defaults for missing
// fields, and validates the result. A missing file falls back to the
// canonical defaults so running flush on a minimally-initialised project
// still works.
func loadConfigForFlush(path string) (*state.Config, error) {
	cfg, err := state.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// flushDryRun prints the prompt that would be sent to Haiku and exits.
// Intended for prompt debugging — never mutates state or buffer.
func flushDryRun(paths devlogLayout, cfg *state.Config) int {
	task, _ := os.ReadFile(paths.taskPath)

	entries, err := buffer.ReadAll(paths.bufferPath)
	if err != nil {
		printErr(derrors.Wrap("flush",
			fmt.Sprintf("read %s", paths.bufferPath), err))
		return 1
	}
	priorLogs, err := devlog.ReadLastN(paths.logPath, cfg.SummarizerContextEntries)
	if err != nil {
		printErr(derrors.Wrap("flush",
			fmt.Sprintf("read %s", paths.logPath), err))
		return 1
	}

	p := prompt.BuildSummarizerPrompt(string(task), priorLogs, entries)
	fmt.Fprint(stdout(), p)
	if !strings.HasSuffix(p, "\n") {
		fmt.Fprintln(stdout())
	}
	return 0
}

// flushExecute runs the full summariser pipeline: guard -> read buffer
// -> invoke claude -> append log -> archive buffer -> bump counters ->
// optional companion spawn.
//
// The state.flush_in_progress flag is set unconditionally on entry and
// cleared on exit. Capture pre-sets it before spawning flush, so a check-
// before-claim would make the spawned flush a no-op (the flag is already
// true from capture's own update). Capture's own decision to "only spawn
// when !FlushInProgress" still prevents duplicate spawns; flush just
// trusts that and runs.
func flushExecute(paths devlogLayout, cfg *state.Config, root string) int {
	if err := setFlushGuard(paths.statePath); err != nil {
		printErr(err)
		return 1
	}
	defer releaseFlushGuard(paths.statePath, paths.errorsLog)

	entries, err := buffer.ReadAll(paths.bufferPath)
	if err != nil {
		logAndPrint(paths.errorsLog, derrors.Wrap("flush",
			fmt.Sprintf("read %s", paths.bufferPath), err))
		return 1
	}
	if len(entries) == 0 {
		return 0
	}

	task, _ := os.ReadFile(paths.taskPath)
	priorLogs, err := devlog.ReadLastN(paths.logPath, cfg.SummarizerContextEntries)
	if err != nil {
		logAndPrint(paths.errorsLog, derrors.Wrap("flush",
			fmt.Sprintf("read %s", paths.logPath), err))
		return 1
	}

	summary, model, durationMS, err := invokeSummariser(cfg, string(task), priorLogs, entries)
	if err != nil {
		logAndPrint(paths.errorsLog, classifySummariserError(err, cfg.Host, paths.errorsLog))
		return 1
	}

	// Persist the summary first, then archive the buffer. If either step
	// fails mid-way the worst outcome is a log entry covering entries
	// still present in the buffer — dedup by seq on next flush is
	// straightforward; the opposite ordering could lose data entirely.
	sessionID, nextSeq, err := bumpLogCounters(paths.statePath)
	if err != nil {
		logAndPrint(paths.errorsLog, err)
		return 1
	}

	logEntry := devlog.Entry{
		Seq:        nextSeq,
		TS:         flushNow(),
		SessionID:  sessionID,
		CoversSeqs: coveredSeqs(entries),
		Summary:    summary,
		Model:      model,
		DurationMS: durationMS,
	}
	if err := devlog.Append(paths.logPath, logEntry); err != nil {
		logAndPrint(paths.errorsLog, err)
		return 1
	}

	if err := buffer.Archive(paths.bufferPath, paths.archivePath); err != nil {
		logAndPrint(paths.errorsLog, derrors.Wrap("flush",
			"archive buffer", err))
		return 1
	}

	shouldSpawn, err := resetBufferAndCheckCompanion(paths.statePath, cfg)
	if err != nil {
		logAndPrint(paths.errorsLog, err)
		return 1
	}
	if shouldSpawn {
		if err := flushCompanionSpawner(root); err != nil {
			_ = derrors.Wrap("flush", "spawn companion", err).
				WriteToLog(paths.errorsLog)
			// Don't fail the flush — the summary is already durable.
		}
	}

	fmt.Fprintf(stdout(), "devlog: flushed %d diff(s) → log entry #%d\n",
		len(entries), nextSeq)
	return 0
}

// setFlushGuard sets state.flush_in_progress to true. No-op when the flag
// is already set (see flushExecute's docstring for the rationale).
func setFlushGuard(statePath string) error {
	err := state.Update(statePath, func(s *state.State) error {
		s.FlushInProgress = true
		return nil
	})
	if err != nil {
		return derrors.Wrap("flush", "set flush guard", err)
	}
	return nil
}

// releaseFlushGuard clears state.flush_in_progress. Errors are logged
// only — the caller has already finished the critical section.
func releaseFlushGuard(statePath, errorsLogPath string) {
	err := state.Update(statePath, func(s *state.State) error {
		s.FlushInProgress = false
		return nil
	})
	if err != nil {
		_ = derrors.Wrap("flush", "release flush guard", err).
			WriteToLog(errorsLogPath)
	}
}

// invokeSummariser builds the prompt and runs the configured host. Returns
// the summary text (trimmed), the model name, and the duration reported
// by the host.
func invokeSummariser(cfg *state.Config, task string, priorLogs []devlog.Entry,
	entries []buffer.Entry) (string, string, int, error) {
	promptText := prompt.BuildSummarizerPrompt(task, priorLogs, entries)

	runner := flushLLMRunner(cfg)
	if runner == nil {
		return "", "", 0, fmt.Errorf("no LLM host registered")
	}
	timeout := time.Duration(cfg.SummarizerTimeoutSeconds) * time.Second
	resp, err := runner.RunLLM(context.Background(), cfg.SummarizerModel, promptText, timeout)
	if err != nil {
		return "", "", 0, err
	}
	if resp.IsError {
		return "", "", 0, fmt.Errorf("host reported error (subtype=%s): %s", resp.Subtype, resp.Result)
	}
	model := resp.Model
	if model == "" {
		model = cfg.SummarizerModel
	}
	return strings.TrimSpace(resp.Result), model, resp.DurationMS, nil
}

// classifySummariserError maps a host.RunLLM error to a DevlogError with
// the SPEC-mandated remediation text so operators see what happened and
// what to do next.
func classifySummariserError(err error, hostName string, errorsLog string) error {
	switch {
	case stderrors.Is(err, host.ErrCommandNotFound):
		return derrors.Wrap("flush", fmt.Sprintf("summarizer: %s command not found", hostName), err).
			WithRemediation(
				fmt.Sprintf("The '%s' CLI is not in PATH. DevLog needs it to run the\n"+
					"summariser and companion models.\n\n"+
					"Set path: devlog config host_command /path/to/%s\n\n"+
					"Full error logged to: %s", hostName, hostName, errorsLog),
			)
	case stderrors.Is(err, host.ErrTimeout):
		return derrors.Wrap("flush", fmt.Sprintf("summarizer: %s process timed out", hostName), err).
			WithRemediation(
				"The summariser took too long to respond. Buffer preserved —\n" +
					"will retry on next flush. Run 'devlog flush' to retry manually.\n\n" +
					"Full error logged to: " + errorsLog,
			)
	case stderrors.Is(err, host.ErrEmptyResponse):
		return derrors.Wrap("flush", "summarizer returned empty response", err).
			WithRemediation(
				"The summariser was invoked but returned no usable summary.\n" +
					"Buffer preserved — will retry on next flush.\n" +
					"To retry now: devlog flush\n" +
					"To inspect the prompt: devlog flush --dry-run\n\n" +
					"Full error logged to: " + errorsLog,
			)
	case stderrors.Is(err, host.ErrNonZeroExit):
		var exitErr *host.ExitError
		stderrTxt := ""
		if stderrors.As(err, &exitErr) {
			stderrTxt = exitErr.Stderr
		}
		remediation := "Check your subscription supports the configured model.\n" +
			"Buffer preserved — will retry on next flush.\n\n" +
			"Full error logged to: " + errorsLog
		if strings.TrimSpace(stderrTxt) != "" {
			remediation = "stderr: " + strings.TrimSpace(stderrTxt) + "\n\n" + remediation
		}
		return derrors.Wrap("flush", fmt.Sprintf("summarizer: %s exited non-zero", hostName), err).
			WithRemediation(remediation)
	default:
		return derrors.Wrap("flush", "summarizer failed", err).
			WithRemediation("Buffer preserved — will retry on next flush.\n" +
				"Full error logged to: " + errorsLog)
	}
}

// bumpLogCounters allocates the next log_seq and returns it along with
// the session id. Writes are cross-process safe via state.Update.
func bumpLogCounters(statePath string) (string, int, error) {
	var sessionID string
	var nextSeq int
	err := state.Update(statePath, func(s *state.State) error {
		s.LogSeq++
		s.LogCount++
		s.LogSinceCompanion++
		sessionID = s.SessionID
		nextSeq = s.LogSeq
		return nil
	})
	if err != nil {
		return "", 0, derrors.Wrap("flush", "bump log counters", err)
	}
	return sessionID, nextSeq, nil
}

// resetBufferAndCheckCompanion zeroes buffer_count in state.json and
// reports whether the companion should now be spawned. When the
// companion threshold is crossed, log_since_companion is reset to 0 here
// so a rapid second flush doesn't re-trigger the spawn.
func resetBufferAndCheckCompanion(statePath string, cfg *state.Config) (bool, error) {
	spawn := false
	err := state.Update(statePath, func(s *state.State) error {
		s.BufferCount = 0
		if s.LogSinceCompanion >= cfg.CompanionInterval {
			s.LogSinceCompanion = 0
			spawn = true
		}
		return nil
	})
	if err != nil {
		return false, derrors.Wrap("flush", "reset buffer counters", err)
	}
	return spawn, nil
}

// coveredSeqs extracts the seq field from every entry. Order follows the
// buffer file — the companion cites these in-order when explaining
// trajectory.
func coveredSeqs(entries []buffer.Entry) []int {
	out := make([]int, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}

// defaultCompanionSpawner fork-execs `devlog companion --project <root>`
// in a new process group so the child outlives the current invocation.
// The child inherits /dev/null for stdio so it doesn't disturb the
// working agent's terminal.
func defaultCompanionSpawner(projectRoot string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devnull.Close()

	cmd := exec.Command(self, "companion", "--project", projectRoot)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start companion: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// logAndPrint writes err to errorsLogPath and prints it to stderr. Keeps
// the two sinks in sync — every user-visible error is also available in
// errors.log for later inspection.
func logAndPrint(errorsLogPath string, err error) {
	if err == nil {
		return
	}
	if de, ok := err.(*derrors.DevlogError); ok {
		_ = de.WriteToLog(errorsLogPath)
		printErr(de)
		return
	}
	wrapped := derrors.Wrap("flush", "unexpected error", err)
	_ = wrapped.WriteToLog(errorsLogPath)
	printErr(wrapped)
}
