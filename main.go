package main

import (
	"fmt"
	"io"
	"os"

	"devlog/cmd"
)

// command is a registered subcommand.
type command struct {
	name    string
	summary string
	run     func(args []string) int
}

// commands defines every subcommand exposed by the devlog CLI. The order
// here is the order used when printing usage.
var commands = []command{
	{"init", "Initialize .devlog/, verify git, set session ID", cmd.Init},
	{"capture", "Buffer a diff entry; trigger flush if threshold met", cmd.Capture},
	{"task-capture", "Record user's prompt as task/update", cmd.TaskCapture},
	{"task-tool-capture", "Record TaskCreate/TaskUpdate tool calls", cmd.TaskToolCapture},
	{"check-feedback", "Output pending companion feedback or exit silently", cmd.CheckFeedback},
	{"flush", "Run Haiku summarizer on buffered diffs", cmd.Flush},
	{"companion", "Run Sonnet anti-pattern assessment", cmd.Companion},
	{"status", "Show current state: counters, last companion, health", cmd.Status},
	{"log", "Print the dev log narrative (human-readable)", cmd.Log},
	{"reset", "Clear all state for a fresh session", cmd.Reset},
	{"config", "Get/set tunable parameters", cmd.Config},
	{"install", "Install hooks for the configured host backend", cmd.Install},
	{"uninstall", "Remove hooks for the configured host backend", cmd.Uninstall},
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch selects and runs the subcommand named by args[0]. It is split
// out of main so tests can exercise the dispatcher without exec'ing a binary.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		printUsage(stdout)
		return 0
	}

	for _, c := range commands {
		if c.name == name {
			return c.run(args[1:])
		}
	}

	fmt.Fprintf(stderr, "devlog: error: unknown command %q\n\n", name)
	printUsage(stderr)
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "devlog — death-spiral prevention system for AI coding agents")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "    devlog <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "    %-20s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'devlog <command> -h' for command-specific help.")
}
