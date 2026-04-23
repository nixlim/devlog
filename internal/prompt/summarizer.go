// Package prompt builds the text prompts fed to the Haiku summarizer
// (dev log line) and the Sonnet companion (anti-pattern assessment).
//
// Both builders are pure functions: they take ready-sliced inputs and
// return a string. Trimming log/buffer entries to the configured
// context windows is the caller's job, so prompt logic stays trivial to
// unit-test without any I/O or config plumbing.
package prompt

import (
	"fmt"
	"strings"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
)

// LogEntry aliases devlog.Entry so prompt builders can speak the name
// used throughout SPEC.md and the task descriptions ("log entries").
type LogEntry = devlog.Entry

// SummarizerSystemPrompt is the verbatim system prompt from SPEC.md
// (section "Dev Log Summarizer (Haiku)"). It is exported so tests can
// assert byte-equality — any drift from the spec will trip CI.
const SummarizerSystemPrompt = "You are a dev log writer tracking an AI coding agent's work. " +
	"Summarize what these code changes are trying to accomplish in 1-2 sentences. " +
	"Write in present tense, focusing on intent and approach, not individual file changes. " +
	"Your summary should read as the next paragraph in an ongoing narrative. " +
	"Note any repeated patterns (same files touched, same approach retried)."

// BuildSummarizerPrompt renders the full Haiku prompt.
//
// task is the original user instruction captured in .devlog/task.md.
// logEntries should already be trimmed to config.summarizer_context_entries
// (typically the most recent 5) — this function does not enforce a cap
// of its own, so callers can supply more or fewer as needed for
// testing and dry-runs.
//
// bufferEntries is the full batch of diffs being summarised — typically
// the contents of buffer.jsonl at flush time.
//
// Sections are separated by blank lines with clear ALL-CAPS headers so
// Haiku parses the boundaries reliably.
func BuildSummarizerPrompt(task string, logEntries []LogEntry, bufferEntries []buffer.Entry) string {
	var b strings.Builder

	b.WriteString(SummarizerSystemPrompt)
	b.WriteString("\n\n")

	b.WriteString("ORIGINAL TASK:\n")
	if strings.TrimSpace(task) == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(strings.TrimSpace(task))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("RECENT LOG ENTRIES:\n")
	if len(logEntries) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, e := range logEntries {
			renderLogEntry(&b, e)
		}
	}
	b.WriteString("\n")

	b.WriteString("BUFFERED DIFFS:\n")
	if len(bufferEntries) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, e := range bufferEntries {
			renderBufferEntry(&b, e)
		}
	}

	return b.String()
}

func renderLogEntry(b *strings.Builder, e LogEntry) {
	ts := e.TS.UTC().Format(time.RFC3339)
	fmt.Fprintf(b, "#%d [%s]: %s\n", e.Seq, ts, strings.TrimSpace(e.Summary))
}

func renderBufferEntry(b *strings.Builder, e buffer.Entry) {
	fmt.Fprintf(b, "#%d [%s] %s", e.Seq, e.TS, e.Tool)
	if e.File != "" {
		fmt.Fprintf(b, " %s", e.File)
	}
	if e.Detail != "" {
		fmt.Fprintf(b, ": %s", e.Detail)
	}
	if !e.Changed {
		b.WriteString(" [no file changes]")
	}
	b.WriteString("\n")
}
