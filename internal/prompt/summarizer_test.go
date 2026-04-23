package prompt

import (
	"strings"
	"testing"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
)

func TestSummarizerSystemPromptMatchesSpec(t *testing.T) {
	// SPEC.md (Dev Log Summarizer) pins this phrasing. Any edit has to
	// flow through a spec update and this test at the same time.
	const want = "You are a dev log writer tracking an AI coding agent's work. " +
		"Summarize what these code changes are trying to accomplish in 1-2 sentences. " +
		"Write in present tense, focusing on intent and approach, not individual file changes. " +
		"Your summary should read as the next paragraph in an ongoing narrative. " +
		"Note any repeated patterns (same files touched, same approach retried)."
	if SummarizerSystemPrompt != want {
		t.Fatalf("SummarizerSystemPrompt drifted from SPEC.md:\ngot:  %q\nwant: %q", SummarizerSystemPrompt, want)
	}
}

func TestBuildSummarizerPromptSections(t *testing.T) {
	task := "Fix the 500 error on /api/recommendations"
	logs := []LogEntry{
		{Seq: 6, TS: time.Date(2026, 4, 22, 22, 10, 0, 0, time.UTC), Summary: "Increase db timeout"},
		{Seq: 7, TS: time.Date(2026, 4, 22, 22, 12, 0, 0, time.UTC), Summary: "Tune pool size"},
	}
	bufs := []buffer.Entry{
		{Seq: 42, TS: "2026-04-22T22:15:00Z", Tool: "Edit", File: "src/api/handler.go",
			Detail:    "old: 'Timeout: 30 * time.Second' → new: 'Timeout: 60 * time.Second'",
			DiffLines: 4, Changed: true},
	}

	out := BuildSummarizerPrompt(task, logs, bufs)

	for _, want := range []string{
		SummarizerSystemPrompt,
		"ORIGINAL TASK:",
		"Fix the 500 error on /api/recommendations",
		"RECENT LOG ENTRIES:",
		"#6 [2026-04-22T22:10:00Z]: Increase db timeout",
		"#7 [2026-04-22T22:12:00Z]: Tune pool size",
		"BUFFERED DIFFS:",
		"#42 [2026-04-22T22:15:00Z] Edit src/api/handler.go:",
		"Timeout: 30 * time.Second",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n----- prompt -----\n%s\n---- end -----", want, out)
		}
	}

	// Headers must appear in the documented order so Haiku parses them
	// deterministically.
	idxTask := strings.Index(out, "ORIGINAL TASK:")
	idxLog := strings.Index(out, "RECENT LOG ENTRIES:")
	idxBuf := strings.Index(out, "BUFFERED DIFFS:")
	if !(idxTask < idxLog && idxLog < idxBuf) {
		t.Errorf("section order wrong: TASK=%d LOG=%d BUF=%d", idxTask, idxLog, idxBuf)
	}
}

func TestBuildSummarizerPromptEmptyTask(t *testing.T) {
	out := BuildSummarizerPrompt("   ", nil, nil)
	if !strings.Contains(out, "ORIGINAL TASK:\n(none)") {
		t.Errorf("blank task should render as (none), got:\n%s", out)
	}
}

func TestBuildSummarizerPromptEmptyLogEntries(t *testing.T) {
	out := BuildSummarizerPrompt("do x", nil, []buffer.Entry{{Seq: 1, Tool: "Edit", Changed: true}})
	if !strings.Contains(out, "RECENT LOG ENTRIES:\n(none)") {
		t.Errorf("empty log entries should render as (none), got:\n%s", out)
	}
}

func TestBuildSummarizerPromptEmptyBufferEntries(t *testing.T) {
	out := BuildSummarizerPrompt("do x",
		[]LogEntry{{Seq: 1, TS: time.Unix(0, 0).UTC(), Summary: "s"}},
		nil)
	if !strings.Contains(out, "BUFFERED DIFFS:\n(none)") {
		t.Errorf("empty buffer entries should render as (none), got:\n%s", out)
	}
}

func TestBuildSummarizerPromptIncludesAllPassedLogEntries(t *testing.T) {
	// The builder itself doesn't enforce summarizer_context_entries —
	// the caller trims. This test confirms: whatever the caller passes,
	// the builder renders. Passing more than the default 5 must not be
	// silently dropped by the builder.
	logs := make([]LogEntry, 0, 8)
	for i := 1; i <= 8; i++ {
		logs = append(logs, LogEntry{
			Seq:     i,
			TS:      time.Unix(int64(i), 0).UTC(),
			Summary: "entry-" + string(rune('A'-1+i)),
		})
	}
	out := BuildSummarizerPrompt("t", logs, nil)
	for i := 1; i <= 8; i++ {
		marker := "entry-" + string(rune('A'-1+i))
		if !strings.Contains(out, marker) {
			t.Errorf("expected %q in prompt, missing. Output:\n%s", marker, out)
		}
	}
}

func TestBuildSummarizerPromptFlagsUnchangedBashEntries(t *testing.T) {
	bufs := []buffer.Entry{
		{Seq: 10, TS: "2026-04-22T22:15:00Z", Tool: "Bash", Detail: "ls -la", Changed: false},
		{Seq: 11, TS: "2026-04-22T22:15:10Z", Tool: "Bash", Detail: "touch x", Changed: true},
	}
	out := BuildSummarizerPrompt("t", nil, bufs)
	if !strings.Contains(out, "[no file changes]") {
		t.Errorf("expected unchanged Bash entry to carry '[no file changes]' marker, got:\n%s", out)
	}
	// The changed Bash entry must NOT carry that marker.
	idx := strings.Index(out, "#11")
	if idx < 0 {
		t.Fatalf("entry #11 missing from prompt: %s", out)
	}
	rest := out[idx:]
	if strings.Contains(strings.SplitN(rest, "\n", 2)[0], "[no file changes]") {
		t.Errorf("entry #11 (changed) should not carry '[no file changes]' marker, got line: %q",
			strings.SplitN(rest, "\n", 2)[0])
	}
}

func TestBuildSummarizerPromptIsDeterministic(t *testing.T) {
	task := "same"
	logs := []LogEntry{{Seq: 1, TS: time.Unix(0, 0).UTC(), Summary: "s"}}
	bufs := []buffer.Entry{{Seq: 1, TS: "t", Tool: "Edit", File: "f", Detail: "d", Changed: true}}

	a := BuildSummarizerPrompt(task, logs, bufs)
	b := BuildSummarizerPrompt(task, logs, bufs)
	if a != b {
		t.Errorf("prompt should be deterministic for identical inputs\na=%q\nb=%q", a, b)
	}
}

// Sanity check: the devlog.Entry import path is still the source of
// truth for LogEntry. If this alias is removed we want a loud compile
// failure at this package boundary, not a silent drift.
var _ LogEntry = devlog.Entry{}
