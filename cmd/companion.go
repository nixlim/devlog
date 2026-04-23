package cmd

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/claude"
	"devlog/internal/devlog"
	derrors "devlog/internal/errors"
	"devlog/internal/feedback"
	"devlog/internal/prompt"
	"devlog/internal/state"
)

// Companion implements `devlog companion` — the Sonnet anti-pattern check.
//
// Flow (happy path):
//  1. Load config + gather prompt inputs (task, updates, log, diffs, tasks).
//  2. Build the Sonnet prompt via prompt.BuildCompanionPrompt.
//  3. Acquire the companion_in_progress guard in state.json.
//  4. Invoke claude with the companion model and configured timeout.
//  5. Parse the model's JSON into feedback.CompanionResult.
//  6. Persist last_companion and reset log_since_companion=0.
//  7. If result is DRIFTING or SPIRALING, format + write feedback.md.
//  8. Release the guard.
//
// On any failure after step 3, the guard is released in defer and the
// counter (log_since_companion) is NOT reset, so the next threshold
// crossing will retry.
func Companion(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), companionUsage)
		return 0
	}

	fs := flag.NewFlagSet("companion", flag.ContinueOnError)
	fs.SetOutput(stderr())
	dryRun := fs.Bool("dry-run", false, "build and print the prompt without invoking claude")
	projectFlag := fs.String("project", "", "project root (defaults to cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveProjectRoot(*projectFlag)
	if err != nil {
		printErr(err)
		return 1
	}
	devlogDir := filepath.Join(root, ".devlog")
	statePath := filepath.Join(devlogDir, "state.json")
	configPath := filepath.Join(devlogDir, "config.json")

	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		printErr(err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		printErr(err)
		return 1
	}
	if !cfg.IsEnabled() {
		return 0
	}

	input, err := buildCompanionInput(devlogDir, cfg)
	if err != nil {
		printErr(err)
		return 1
	}
	promptStr := prompt.BuildCompanionPrompt(input)

	if *dryRun {
		fmt.Fprint(stdout(), promptStr)
		return 0
	}

	acquired, err := acquireCompanionGuard(statePath)
	if err != nil {
		printErr(err)
		return 1
	}
	if !acquired {
		fmt.Fprintln(stderr(), "devlog: companion already in progress; skipping this run")
		return 0
	}
	defer releaseCompanionGuard(statePath)

	runner := claude.New(cfg.ClaudeCommand)
	timeout := time.Duration(cfg.CompanionTimeoutSeconds) * time.Second
	resp, runErr := runner.Run(context.Background(), cfg.CompanionModel, promptStr, timeout)
	if runErr != nil {
		printCompanionRunError(devlogDir, runErr)
		recordHookError(filepath.Join(devlogDir, "errors.log"), "companion", runErr)
		return 1
	}
	if resp.IsError {
		isErr := fmt.Errorf("claude reported error (subtype=%s): %s", resp.Subtype, resp.Result)
		recordHookError(filepath.Join(devlogDir, "errors.log"), "companion", isErr)
		fmt.Fprintf(stderr(), "devlog: error: companion: %v\n", isErr)
		return 1
	}

	result, parseErr := parseCompanionResult(resp.Result)
	if parseErr != nil {
		recordHookError(filepath.Join(devlogDir, "errors.log"), "companion", parseErr)
		fmt.Fprintf(stderr(), "devlog: error: companion: %v\n", parseErr)
		return 1
	}

	if err := commitCompanionResult(statePath, result); err != nil {
		recordHookError(filepath.Join(devlogDir, "errors.log"), "companion", err)
		printErr(err)
		return 1
	}

	if result.NeedsIntervention() {
		formatted := feedback.Format(result)
		feedbackPath := filepath.Join(devlogDir, "feedback.md")
		if err := feedback.Write(feedbackPath, formatted); err != nil {
			recordHookError(filepath.Join(devlogDir, "errors.log"), "companion", err)
			fmt.Fprintf(stderr(), "devlog: error: companion: write feedback: %v\n", err)
			return 1
		}
	}

	return 0
}

const companionUsage = `devlog companion — run the Sonnet anti-pattern assessment

Usage:
    devlog companion [--dry-run] [--project DIR]

Flags:
    --dry-run     build and print the prompt without invoking claude.
    --project     project root (defaults to the current working directory).

Writes feedback.md when the assessment is DRIFTING or SPIRALING. On ON_TRACK
the only side effect is the updated last_companion record in state.json.
`

// acquireCompanionGuard atomically sets companion_in_progress=true in
// state.json. Returns (true, nil) on acquisition, (false, nil) when the
// flag was already set by a concurrent invocation.
func acquireCompanionGuard(statePath string) (bool, error) {
	var acquired bool
	err := state.Update(statePath, func(s *state.State) error {
		if s.CompanionInProgress {
			acquired = false
			return nil
		}
		s.CompanionInProgress = true
		acquired = true
		return nil
	})
	if err != nil {
		return false, derrors.Wrap("companion", "acquire guard", err)
	}
	return acquired, nil
}

// releaseCompanionGuard clears companion_in_progress. Any I/O failure here
// is swallowed — a stuck flag will be detected and cleared by the next
// successful Update, which is preferable to a stuck subprocess.
func releaseCompanionGuard(statePath string) {
	_ = state.Update(statePath, func(s *state.State) error {
		s.CompanionInProgress = false
		return nil
	})
}

// commitCompanionResult persists the model verdict and resets the "entries
// since last companion" counter. The reset happens only on success — a
// failed run leaves the counter alone so the next flush threshold crossing
// will retry the assessment.
func commitCompanionResult(statePath string, r feedback.CompanionResult) error {
	return state.Update(statePath, func(s *state.State) error {
		s.LastCompanion = &state.LastCompanion{
			TS:         time.Now().UTC().Format(time.RFC3339),
			Status:     r.Status,
			Confidence: r.Confidence,
		}
		s.LogSinceCompanion = 0
		return nil
	})
}

// buildCompanionInput gathers every input the prompt builder needs. Every
// file is optional: a missing file means the corresponding section renders
// as "(none)".
func buildCompanionInput(devlogDir string, cfg *state.Config) (prompt.CompanionInput, error) {
	var in prompt.CompanionInput

	task, err := readStringFile(filepath.Join(devlogDir, "task.md"))
	if err != nil {
		return in, derrors.Wrap("companion", "read task.md", err)
	}
	in.Task = task

	updates, err := readUserUpdates(filepath.Join(devlogDir, "task_updates.jsonl"))
	if err != nil {
		return in, derrors.Wrap("companion", "read task_updates.jsonl", err)
	}
	in.Updates = updates

	logEntries, err := devlog.ReadLastN(filepath.Join(devlogDir, "log.jsonl"), cfg.CompanionLogEntries)
	if err != nil {
		return in, err
	}
	in.LogEntries = logEntries

	archEntries, err := buffer.ReadAll(filepath.Join(devlogDir, "buffer_archive.jsonl"))
	if err != nil {
		return in, derrors.Wrap("companion", "read buffer_archive.jsonl", err)
	}
	in.DiffArchive = tailBufferEntries(archEntries, cfg.CompanionDiffEntries)

	tasks, err := readTaskList(filepath.Join(devlogDir, "tasks.jsonl"))
	if err != nil {
		return in, derrors.Wrap("companion", "read tasks.jsonl", err)
	}
	in.TaskList = tasks

	in.MaxLogEntries = cfg.CompanionLogEntries
	in.MaxDiffEntries = cfg.CompanionDiffEntries
	return in, nil
}

// readStringFile returns the contents of path, or "" if the file does not
// exist. Distinguishing "missing" from "empty" isn't useful here — both
// render as "(none)" in the prompt.
func readStringFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// readUserUpdates parses one UserUpdate per non-empty line of path. A
// missing file is treated as no updates.
func readUserUpdates(path string) ([]prompt.UserUpdate, error) {
	lines, err := readJSONLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]prompt.UserUpdate, 0, len(lines))
	for i, line := range lines {
		var u prompt.UserUpdate
		if err := json.Unmarshal(line, &u); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, u)
	}
	return out, nil
}

// readTaskList parses one TaskListRecord per non-empty line of path.
func readTaskList(path string) ([]prompt.TaskListRecord, error) {
	lines, err := readJSONLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]prompt.TaskListRecord, 0, len(lines))
	for i, line := range lines {
		var r prompt.TaskListRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// readJSONLines returns the non-empty lines of path. A missing file
// returns (nil, nil). The split is done on '\n' — we don't use bufio here
// because the JSONL files in question are small (task updates, task list).
func readJSONLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out [][]byte
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		out = append(out, []byte(raw))
	}
	return out, nil
}

func tailBufferEntries(s []buffer.Entry, n int) []buffer.Entry {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// parseCompanionResult extracts the first balanced JSON object from the
// model's result text and unmarshals it into CompanionResult.
//
// Sonnet is instructed to reply with JSON only, but real models
// occasionally wrap the object in ```json fences or add a preamble. We
// strip a leading/trailing fence, then locate the outermost { ... } so
// stray prose never blocks parsing.
func parseCompanionResult(result string) (feedback.CompanionResult, error) {
	trimmed := strings.TrimSpace(result)

	// Strip a surrounding markdown fence, if present.
	if strings.HasPrefix(trimmed, "```") {
		if nl := strings.Index(trimmed, "\n"); nl >= 0 {
			trimmed = trimmed[nl+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return feedback.CompanionResult{}, derrors.New("companion",
			"model response contained no JSON object").
			WithRemediation(
				"The companion model did not return a JSON object. Inspect the raw\n" +
					"response in .devlog/errors.log. Rerun manually with:\n\n" +
					"    devlog companion\n",
			)
	}

	var r feedback.CompanionResult
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &r); err != nil {
		return feedback.CompanionResult{}, derrors.Wrap("companion",
			"parse model JSON", err)
	}
	return r, nil
}

// printCompanionRunError renders a SPEC-style 4-part error to stderr for
// the specific claude runner failure. The detail of err is also written
// to errors.log by the caller.
func printCompanionRunError(devlogDir string, err error) {
	errLog := filepath.Join(devlogDir, "errors.log")
	switch {
	case stderrors.Is(err, claude.ErrCommandNotFound):
		fmt.Fprintf(stderr(),
			"devlog: error: companion: claude command not found\n\n"+
				"The 'claude' CLI is not in PATH. DevLog needs it to run the\n"+
				"summarizer and companion models.\n\n"+
				"Install: https://docs.anthropic.com/en/docs/claude-code\n"+
				"Or set path: devlog config claude_command /path/to/claude\n\n"+
				"Full error logged to: %s\n", errLog)
	case stderrors.Is(err, claude.ErrTimeout):
		fmt.Fprintf(stderr(),
			"devlog: error: companion: %v\n\n"+
				"The anti-pattern companion took too long to respond. This can happen with very large\n"+
				"dev logs. Try reducing companion_log_entries in .devlog/config.json.\n\n"+
				"Assessment skipped. Will retry after next threshold crossing.\n"+
				"Run 'devlog companion' to retry manually.\n\n"+
				"Full error logged to: %s\n", err, errLog)
	default:
		fmt.Fprintf(stderr(),
			"devlog: error: companion: %v\n\n"+
				"Full error logged to: %s\n", err, errLog)
	}
}
