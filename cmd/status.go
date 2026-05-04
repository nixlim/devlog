package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"devlog/internal/git"
	"devlog/internal/state"
)

// hostHealthInfo holds the resolved host label and command for the health check.
type hostHealthInfo struct {
	label   string
	command string
}

func resolveHostHealth(devlogDir string) hostHealthInfo {
	configPath := filepath.Join(devlogDir, "config.json")
	cfg, err := state.LoadConfig(configPath)
	if err != nil {
		return hostHealthInfo{label: "host", command: "claude"}
	}
	label := cfg.Host
	if label == "" {
		label = "host"
	}
	cmd := cfg.HostCommand
	if cmd == "" {
		cmd = "claude"
	}
	return hostHealthInfo{label: label, command: cmd}
}

// Status implements `devlog status`.
//
// Prints a session summary (counters, last companion verdict) followed by a
// three-line Health dashboard for git / claude / .devlog. Returns 0 when
// every Health item is OK and 1 when any is FAIL. Errors that prevent
// printing (e.g. a broken os.Getwd) go to stderr and exit 1.
//
// Honors NO_COLOR: when the environment variable is set to any non-empty
// value, ANSI color codes are omitted so the output is safe to pipe.
func Status(args []string) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout(), statusUsage)
		return 0
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr(), "devlog: error: status: resolve working directory: %v\n", err)
		return 1
	}
	return writeStatus(wd, stdout())
}

const statusUsage = `devlog status — show current session state and health

Usage:
    devlog status

Environment:
    NO_COLOR   when set to any non-empty value, disables colored output.
`

// writeStatus is split out of Status so tests can drive it from a temp
// project directory without exec'ing the binary or chdir'ing the process.
//
// Returns 1 if any Health item is FAIL, 0 otherwise.
func writeStatus(projectDir string, w io.Writer) int {
	devlogDir := findDevlogDir(projectDir)
	statePath := filepath.Join(devlogDir, "state.json")

	useColor := os.Getenv("NO_COLOR") == ""

	s, stateErr := state.Load(statePath)
	switch {
	case stateErr == nil:
		writeSessionBlock(w, s)
	case os.IsNotExist(stateErr):
		fmt.Fprintln(w, "Session:        not initialized — run 'devlog init'")
		fmt.Fprintln(w)
	default:
		fmt.Fprintf(w, "Session:        error loading state: %v\n\n", stateErr)
	}

	anyFail := writeHealth(w, useColor, projectDir, devlogDir)
	if anyFail {
		return 1
	}
	return 0
}

// findDevlogDir walks upward from projectDir looking for a directory that
// contains a .git entry and returns <that>/.devlog. If no ancestor has
// .git, returns projectDir/.devlog — the Health pane will then flag the
// repo as FAIL, which is the signal the user needs.
func findDevlogDir(projectDir string) string {
	current := projectDir
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return filepath.Join(current, ".devlog")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return filepath.Join(projectDir, ".devlog")
}

func writeSessionBlock(w io.Writer, s *state.State) {
	fmt.Fprintf(w, "Session:        %s\n", orMissing(s.SessionID))
	fmt.Fprintf(w, "Started:        %s\n", orMissing(s.StartedAt))
	fmt.Fprintf(w, "Buffer:         %d entries (next seq: %d)\n", s.BufferCount, s.BufferSeq)
	fmt.Fprintf(w, "Log:            %d entries (%d since last companion)\n",
		s.LogCount, s.LogSinceCompanion)
	if s.LastCompanion == nil || s.LastCompanion.TS == "" {
		fmt.Fprintln(w, "Last companion: never")
	} else {
		fmt.Fprintf(w, "Last companion: %s @ %s (confidence: %d%%)\n",
			orUnknown(s.LastCompanion.Status),
			s.LastCompanion.TS,
			percentOf(s.LastCompanion.Confidence))
	}
	fmt.Fprintln(w)
}

// writeHealth prints the three-line health block and returns true if any
// line is FAIL.
func writeHealth(w io.Writer, useColor bool, projectDir, devlogDir string) bool {
	fmt.Fprintln(w, "Health:")
	anyFail := false

	gitErr := git.CheckRepo(projectDir)
	gitOK := gitErr == nil
	gitDetail := projectDir
	if !gitOK {
		if os.IsPermission(gitErr) {
			gitDetail = projectDir + " (permission denied checking .git)"
		} else {
			gitDetail = projectDir + " (no .git found in this or any parent directory)"
		}
	}
	printHealthLine(w, useColor, "git", gitOK, gitDetail)
	anyFail = anyFail || !gitOK

	hi := resolveHostHealth(devlogDir)
	hostPath, lookErr := exec.LookPath(hi.command)
	hostOK := lookErr == nil
	hostDetail := hostPath
	if !hostOK {
		hostDetail = hi.command + " not found in PATH"
	}
	printHealthLine(w, useColor, hi.label, hostOK, hostDetail)
	anyFail = anyFail || !hostOK

	info, statErr := os.Stat(devlogDir)
	devlogOK := statErr == nil && info.IsDir()
	devlogDetail := devlogDir
	switch {
	case statErr != nil && os.IsNotExist(statErr):
		devlogDetail = devlogDir + " (not found — run 'devlog init')"
	case statErr != nil:
		devlogDetail = fmt.Sprintf("%s (%v)", devlogDir, statErr)
	case info != nil && !info.IsDir():
		devlogDetail = devlogDir + " (exists but is not a directory)"
	}
	printHealthLine(w, useColor, ".devlog", devlogOK, devlogDetail)
	anyFail = anyFail || !devlogOK

	return anyFail
}

// printHealthLine formats one row of the Health dashboard. Padding is
// computed on the plain badge text so ANSI escape bytes don't mis-align
// the output in a TTY.
func printHealthLine(w io.Writer, useColor bool, label string, ok bool, detail string) {
	badge := "OK"
	code := "\033[32m"
	if !ok {
		badge = "FAIL"
		code = "\033[31m"
	}
	pad := strings.Repeat(" ", 4-len(badge))
	coloredBadge := badge
	if useColor {
		coloredBadge = code + badge + "\033[0m"
	}
	fmt.Fprintf(w, "  %-9s %s%s  %s\n", label+":", coloredBadge, pad, detail)
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}

func orMissing(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// percentOf converts a 0..1 confidence to a 0..100 integer, rounding to
// nearest. Values outside [0, 1] are clamped. Kept local to the cmd
// package to avoid a dependency on internal/feedback just for one helper.
func percentOf(c float64) int {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 100
	}
	return int(c*100 + 0.5)
}
