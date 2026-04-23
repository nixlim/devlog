package cmd

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"devlog/internal/devlog"
	derrors "devlog/internal/errors"
)

// Log implements `devlog log`. By default it walks log.jsonl and prints
// one human-readable line per entry (`#<seq> [<ts>] <summary>`). Flags:
//
//	--json         emit raw log.jsonl contents to stdout (no reformatting)
//	--tail N       only show the last N entries in formatted mode
//	--project DIR  act on the .devlog/ under DIR instead of cwd
//
// Missing or empty log files render as "(no entries)\n" (or an empty
// stdout under --json) and still exit 0 — a freshly-initialized devlog
// directory is a normal state, not an error.
func Log(args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr())
	jsonMode := fs.Bool("json", false, "emit raw log.jsonl contents instead of the formatted view")
	tail := fs.Int("tail", 0, "show only the last N entries (0 = all)")
	projectDir := fs.String("project", "", "project root (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tail < 0 {
		fmt.Fprintf(stderr(), "devlog: error: log: --tail must be >= 0 (got %d)\n", *tail)
		return 2
	}

	root, err := resolveProjectRootForLog(*projectDir)
	if err != nil {
		printErr(err)
		return 1
	}
	logPath := filepath.Join(findDevlogDir(root), "log.jsonl")

	if *jsonMode {
		return printLogRaw(logPath, stdout(), stderr())
	}
	return printLogFormatted(logPath, *tail, stdout(), stderr())
}

// resolveProjectRootForLog mirrors init.go's resolveProjectRoot so log
// doesn't depend on internals of another subcommand's helper. Keeping a
// parallel definition avoids ordering fragility as workers edit init.go
// independently.
func resolveProjectRootForLog(dir string) (string, error) {
	root := dir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", derrors.Wrap("log", "resolve working directory", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", derrors.Wrap("log", fmt.Sprintf("resolve absolute path for %s", root), err)
	}
	return abs, nil
}

// printLogRaw dumps the JSONL file verbatim. A missing file yields
// empty stdout — callers (operators, grep pipelines) expect consumable
// output, not an error, when there is simply nothing to print.
func printLogRaw(path string, stdout, stderr io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintf(stderr, "devlog: error: log: %v\n", err)
		return 1
	}
	defer f.Close()
	if _, err := io.Copy(stdout, f); err != nil {
		fmt.Fprintf(stderr, "devlog: error: log: copy %s: %v\n", path, err)
		return 1
	}
	return 0
}

// printLogFormatted renders entries as `#<seq> [<ts>] <summary>`. When
// tail > 0, only the last tail entries are shown. Streaming the full
// file avoids allocating a slice the size of the log for the default
// "show everything" case, which matters once a session has hundreds of
// entries.
func printLogFormatted(path string, tail int, stdout, stderr io.Writer) int {
	if tail > 0 {
		entries, err := devlog.ReadLastN(path, tail)
		if err != nil {
			fmt.Fprintf(stderr, "devlog: error: log: %v\n", err)
			return 1
		}
		if len(entries) == 0 {
			fmt.Fprintln(stdout, "(no entries)")
			return 0
		}
		for _, e := range entries {
			writeLogLine(stdout, e)
		}
		return 0
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "(no entries)")
			return 0
		}
		fmt.Fprintf(stderr, "devlog: error: log: %v\n", err)
		return 1
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), devlog.MaxScannerBytes)

	any := false
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var entry devlog.Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			fmt.Fprintf(stderr,
				"devlog: error: log: decode %s line %d: %v\n", path, lineNo, err)
			return 1
		}
		writeLogLine(stdout, entry)
		any = true
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "devlog: error: log: scan %s: %v\n", path, err)
		return 1
	}
	if !any {
		fmt.Fprintln(stdout, "(no entries)")
	}
	return 0
}

// writeLogLine renders one entry. The timestamp is emitted in RFC3339
// UTC regardless of the entry's zone so output is stable across
// machines. A missing summary is shown as "(empty)" rather than an
// awkward trailing blank.
func writeLogLine(w io.Writer, e devlog.Entry) {
	summary := e.Summary
	if summary == "" {
		summary = "(empty)"
	}
	fmt.Fprintf(w, "#%d [%s] %s\n", e.Seq, e.TS.UTC().Format(time.RFC3339), summary)
}
