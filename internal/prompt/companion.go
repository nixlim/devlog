// Package prompt builds the string inputs fed to the claude subprocesses
// that back `devlog flush` (Haiku summarizer) and `devlog companion`
// (Sonnet anti-pattern companion).
//
// Prompt builders are pure — they take data and return a string. All side
// effects (file I/O, subprocess invocation) live in the callers. This
// keeps the prompt construction unit-testable without touching the
// claude CLI or the filesystem.
package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
)

// UserUpdate is one row from task_updates.jsonl — a course correction
// the user sent after the original task.
type UserUpdate struct {
	TS     string `json:"ts"`
	Prompt string `json:"prompt"`
}

// TaskListRecord is one row from tasks.jsonl. The exact shape of Payload
// depends on whether TaskCreate or TaskUpdate produced it; the prompt
// builder treats the payload as opaque JSON.
type TaskListRecord struct {
	TS       string          `json:"ts"`
	ToolName string          `json:"tool_name"`
	Payload  json.RawMessage `json:"payload"`
}

// CompanionInput bundles every input the Sonnet companion needs. Fields
// that do not apply to the current session (for example TaskList when
// the agent never called TaskCreate) can be left nil — the builder
// renders them as "(none)".
type CompanionInput struct {
	// Task is the original user prompt from task.md.
	Task string

	// Updates are course corrections since the original task, oldest-first.
	Updates []UserUpdate

	// LogEntries are dev-log summaries, oldest-first. The builder keeps
	// only the tail of length MaxLogEntries to respect the prompt budget.
	LogEntries []devlog.Entry

	// DiffArchive is the archived raw buffer entries, oldest-first. The
	// builder keeps only the tail of length MaxDiffEntries.
	DiffArchive []buffer.Entry

	// TaskList is the Claude Code task-tool capture, nil/empty means
	// "the agent never created a task list".
	TaskList []TaskListRecord

	// MaxLogEntries caps how many log entries are included. Zero falls
	// back to DefaultMaxLogEntries (matches SPEC companion_log_entries=25).
	MaxLogEntries int

	// MaxDiffEntries caps how many raw diffs are included. Zero falls
	// back to DefaultMaxDiffEntries (matches SPEC companion_diff_entries=50).
	MaxDiffEntries int
}

const (
	// DefaultMaxLogEntries mirrors the SPEC's companion_log_entries=25.
	DefaultMaxLogEntries = 25
	// DefaultMaxDiffEntries mirrors the SPEC's companion_diff_entries=50.
	DefaultMaxDiffEntries = 50
)

// CompanionSystemPrompt is the verbatim SPEC companion system prompt,
// including all seven anti-pattern names. Exposed for tests and for use
// by the claude runner which may want to pass it as a system prompt
// rather than inline.
const CompanionSystemPrompt = `You are a meta-cognitive companion monitoring an AI coding agent's work trajectory. Your job is to detect "death spiral" anti-patterns — situations where the agent is locked into a wrong mental model and making increasingly desperate tactical fixes without questioning its strategic frame.

You have access to:
- ORIGINAL TASK: What the user asked the agent to do
- USER UPDATES: Any course corrections the user has given
- DEV LOG: A compressed narrative of the agent's actions over time
- RAW DIFFS: Recent code changes for detail
- TASK LIST: The agent's own task breakdown (if it created one)

Assess whether the agent is:
1. **ON_TRACK** — making coherent progress toward the goal
2. **DRIFTING** — minor concerns worth noting (no intervention yet)
3. **SPIRALING** — locked in a wrong frame, needs immediate intervention

Anti-patterns to detect:
- **Repetition Lock**: N+ consecutive changes to the same file/module without the test/error changing
- **Oscillation**: Alternating between two approaches (A→B→A→B) without realizing the loop
- **Scope Creep Under Failure**: Each attempt touches more files than the last, broadening instead of deepening understanding
- **Mock/Stub Escape**: Creating test doubles, stubs, or mocks that simulate success without solving the real problem
- **Undo Cycle**: Reverting changes from 2-3 attempts ago, indicating loss of working memory
- **Confidence Escalation**: Repeated claims of "found the root cause" / "this will definitely fix it" followed by failure
- **Tangential Resolution**: Fixing something adjacent to the actual problem to claim success

If status is SPIRALING, your intervention MUST:
1. Name the specific anti-pattern you detected
2. Cite evidence from the dev log (quote the relevant entries)
3. Identify the assumption the agent should question
4. Suggest a concrete reframe — a different question to ask or approach to try`

// companionOutputSpec is the instruction appended to the end of the user
// payload describing the exact JSON shape Sonnet must return. Kept
// separate from the system prompt so tests can assert it independently.
const companionOutputSpec = `Respond ONLY with a single JSON object, no markdown fences, using exactly these keys:
{
  "status": "on_track" | "drifting" | "spiraling",
  "confidence": number between 0 and 1,
  "pattern": string (the specific anti-pattern name, or "" if status=on_track),
  "evidence": array of strings quoting specific dev log or diff entries,
  "summary": string (one-sentence overall assessment),
  "intervention": string (the message the agent should see; required when status=spiraling, may be empty otherwise),
  "reframe": string (a different question or framing for the agent; required when status=spiraling, may be empty otherwise)
}`

// BuildCompanionPrompt renders in into a single string suitable for
// passing as the positional prompt argument to `claude -p`. It does not
// include the system prompt — callers pass CompanionSystemPrompt via
// --system-prompt (or equivalent) to claude.
func BuildCompanionPrompt(in CompanionInput) string {
	maxLog := in.MaxLogEntries
	if maxLog <= 0 {
		maxLog = DefaultMaxLogEntries
	}
	maxDiff := in.MaxDiffEntries
	if maxDiff <= 0 {
		maxDiff = DefaultMaxDiffEntries
	}

	var b strings.Builder
	b.WriteString("ORIGINAL TASK:\n")
	b.WriteString(renderTask(in.Task))
	b.WriteString("\n\n")

	b.WriteString("USER UPDATES:\n")
	b.WriteString(renderUpdates(in.Updates))
	b.WriteString("\n\n")

	b.WriteString("DEV LOG:\n")
	b.WriteString(renderLogEntries(tailLog(in.LogEntries, maxLog)))
	b.WriteString("\n\n")

	b.WriteString("RAW DIFFS:\n")
	b.WriteString(renderDiffArchive(tailBuffer(in.DiffArchive, maxDiff)))
	b.WriteString("\n\n")

	b.WriteString("TASK LIST:\n")
	b.WriteString(renderTaskList(in.TaskList))
	b.WriteString("\n\n")

	b.WriteString(companionOutputSpec)
	b.WriteString("\n")
	return b.String()
}

// renderTask returns the verbatim task text with "(none)" as a stand-in
// when no task has been captured yet. That should be vanishingly rare in
// practice (the companion only runs after the summarizer, which runs
// after at least one buffered edit, which implies at least one user
// prompt), but the guard lets tests drive the builder without seeding a
// task file.
func renderTask(task string) string {
	trimmed := strings.TrimSpace(task)
	if trimmed == "" {
		return "(none)"
	}
	return trimmed
}

// renderUpdates formats user course corrections as a numbered list. The
// empty case returns "(none)" — we don't want Sonnet hallucinating
// corrections.
func renderUpdates(updates []UserUpdate) string {
	if len(updates) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, u := range updates {
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, u.TS, strings.TrimSpace(u.Prompt))
		if i < len(updates)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderLogEntries formats dev-log summaries as one line per entry.
// Entries are prefixed with their seq so the companion can cite them by
// "Log #N" — matching the SPEC example intervention format.
func renderLogEntries(entries []devlog.Entry) string {
	if len(entries) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "Log #%d [%s]: %s", e.Seq, e.TS.UTC().Format("2006-01-02T15:04:05Z"), strings.TrimSpace(e.Summary))
		if i < len(entries)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderDiffArchive formats raw buffer entries as one line per tool
// call, mirroring the buffer.jsonl layout but in a compact human-readable
// form.
func renderDiffArchive(entries []buffer.Entry) string {
	if len(entries) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "#%d [%s] tool=%s", e.Seq, e.TS, e.Tool)
		if e.File != "" {
			fmt.Fprintf(&b, " file=%s", e.File)
		}
		if !e.Changed {
			b.WriteString(" (no change)")
		}
		if e.Detail != "" {
			fmt.Fprintf(&b, "\n    %s", strings.TrimSpace(e.Detail))
		}
		if i < len(entries)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderTaskList stringifies task tool captures. Each record's payload is
// preserved as raw JSON so the companion can read whatever the agent
// actually passed to TaskCreate/TaskUpdate.
func renderTaskList(records []TaskListRecord) string {
	if len(records) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, r := range records {
		fmt.Fprintf(&b, "#%d [%s] %s", i+1, r.TS, r.ToolName)
		if len(r.Payload) > 0 {
			// Indent the payload on a new line so multi-line JSON is
			// readable in the prompt.
			fmt.Fprintf(&b, "\n    %s", string(r.Payload))
		}
		if i < len(records)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// tailLog returns the last n entries of s, or s unchanged if it is
// already <= n entries.
func tailLog(s []devlog.Entry, n int) []devlog.Entry {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// tailBuffer returns the last n entries of s, or s unchanged if already <= n.
func tailBuffer(s []buffer.Entry, n int) []buffer.Entry {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
