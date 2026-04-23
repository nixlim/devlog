package cmd

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	derrors "devlog/internal/errors"
	"devlog/internal/state"
)

// resetStdin is the reader used to prompt for y/N confirmation. Exposed
// as a package variable so tests can inject scripted responses.
var resetStdin io.Reader = os.Stdin

// resetFiles enumerates the per-session artefacts Reset wipes. Keeping
// them as a list (rather than a glob) avoids clobbering files an operator
// might have dropped into .devlog/ manually, and documents the exact
// on-disk contract alongside SPEC.md's file-structure table.
var resetFiles = []string{
	"buffer.jsonl",
	"log.jsonl",
	"feedback.md",
	"task.md",
	"task_updates.jsonl",
	"tasks.jsonl",
}

// Reset implements `devlog reset`. It clears per-session artefacts so a
// fresh task can begin without stale context from the previous session
// leaking into summariser or companion prompts.
//
// --yes skips the interactive confirmation (required for scripted use).
// --keep-log preserves log.jsonl, so the narrative history survives even
// when the raw buffer is cleared (useful when the operator wants the
// companion to retain long-term trajectory context across sessions).
//
// The command is destructive by design; running without --yes prompts
// for confirmation and exits 1 on anything that isn't "y"/"Y".
func Reset(args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(stderr())
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	keepLog := fs.Bool("keep-log", false, "preserve log.jsonl (keeps narrative history)")
	projectDir := fs.String("project", "", "project root (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveProjectRoot(*projectDir)
	if err != nil {
		printErr(err)
		return 1
	}
	devlogDir := filepath.Join(root, ".devlog")

	if _, err := os.Stat(devlogDir); err != nil {
		if os.IsNotExist(err) {
			printErr(derrors.New("reset",
				fmt.Sprintf("no .devlog directory at %s", devlogDir)).
				WithRemediation(
					"Nothing to reset — this project has not been initialized.\n" +
						"Run: devlog init",
				))
			return 1
		}
		printErr(derrors.Wrap("reset",
			fmt.Sprintf("stat %s", devlogDir), err))
		return 1
	}

	if !*yes {
		ok, err := confirmReset(resetStdin, stdout(), *keepLog)
		if err != nil {
			printErr(derrors.Wrap("reset", "read confirmation", err))
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout(), "devlog: reset aborted")
			return 1
		}
	}

	cleared, errs := clearFiles(devlogDir, *keepLog)
	if err := resetStateCounters(filepath.Join(devlogDir, "state.json")); err != nil {
		errs = append(errs, err)
	}

	for _, e := range errs {
		printErr(e)
	}
	if len(errs) > 0 {
		return 1
	}

	if *keepLog {
		fmt.Fprintf(stdout(), "devlog: reset %d file(s), preserved log.jsonl\n", cleared)
	} else {
		fmt.Fprintf(stdout(), "devlog: reset %d file(s)\n", cleared)
	}
	return 0
}

// confirmReset prompts the operator for a y/N confirmation. The caller
// owns the stdin reader so tests can feed in canned responses without
// touching the real console.
func confirmReset(in io.Reader, out io.Writer, keepLog bool) (bool, error) {
	suffix := ""
	if keepLog {
		suffix = " (log.jsonl will be preserved)"
	}
	fmt.Fprintf(out, "This will clear buffer/feedback/task state in .devlog/%s.\nContinue? [y/N]: ", suffix)

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// clearFiles truncates each per-session file listed in resetFiles.
// Missing files are ignored; other errors are collected and returned so
// a failure on one file doesn't prevent the rest from being cleared.
// Returns the number of files actually truncated.
func clearFiles(devlogDir string, keepLog bool) (int, []error) {
	var errs []error
	cleared := 0
	for _, name := range resetFiles {
		if keepLog && name == "log.jsonl" {
			continue
		}
		path := filepath.Join(devlogDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			errs = append(errs, derrors.Wrap("reset",
				fmt.Sprintf("stat %s", path), err))
			continue
		}
		if err := truncateFile(path); err != nil {
			errs = append(errs, err)
			continue
		}
		cleared++
	}
	return cleared, errs
}

// truncateFile replaces the contents of path with a zero-byte file.
// O_TRUNC combined with O_WRONLY resets size without altering the inode,
// so anything still holding a file handle (e.g. a stale tail) sees the
// truncation without a rename dance.
func truncateFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return derrors.Wrap("reset",
			fmt.Sprintf("truncate %s", path), err)
	}
	return f.Close()
}

// resetStateCounters zeroes out the counter fields in state.json while
// preserving session identity (SessionID, StartedAt) and the last
// companion verdict — an operator who runs `devlog reset` in the middle
// of a session still wants to know "what did Sonnet say most recently".
// Runs under state.Update so the change is cross-process safe.
//
// A missing state.json is treated as a no-op: reset cleans the artefacts
// it can, and a future `devlog init` will create the file.
func resetStateCounters(statePath string) error {
	if _, err := os.Stat(statePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return derrors.Wrap("reset",
			fmt.Sprintf("stat %s", statePath), err)
	}
	if err := state.Update(statePath, func(s *state.State) error {
		s.BufferCount = 0
		s.BufferSeq = 0
		s.LogCount = 0
		s.LogSeq = 0
		s.LogSinceCompanion = 0
		s.FlushInProgress = false
		s.CompanionInProgress = false
		return nil
	}); err != nil {
		return derrors.Wrap("reset",
			fmt.Sprintf("update %s", statePath), err)
	}
	return nil
}
