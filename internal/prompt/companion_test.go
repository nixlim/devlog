package prompt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"devlog/internal/buffer"
	"devlog/internal/devlog"
)

// sevenAntiPatternNames are the exact anti-pattern labels the SPEC
// requires to appear verbatim in the system prompt. Missing any one is a
// spec violation.
var sevenAntiPatternNames = []string{
	"Repetition Lock",
	"Oscillation",
	"Scope Creep Under Failure",
	"Mock/Stub Escape",
	"Undo Cycle",
	"Confidence Escalation",
	"Tangential Resolution",
}

func TestCompanionSystemPromptIncludesAllAntiPatternsVerbatim(t *testing.T) {
	for _, pattern := range sevenAntiPatternNames {
		if !strings.Contains(CompanionSystemPrompt, pattern) {
			t.Errorf("system prompt missing anti-pattern %q", pattern)
		}
	}
}

func TestCompanionSystemPromptListsAllThreeStatuses(t *testing.T) {
	for _, status := range []string{"ON_TRACK", "DRIFTING", "SPIRALING"} {
		if !strings.Contains(CompanionSystemPrompt, status) {
			t.Errorf("system prompt missing status %q", status)
		}
	}
}

func TestCompanionSystemPromptMentionsAllFiveSections(t *testing.T) {
	for _, section := range []string{"ORIGINAL TASK", "USER UPDATES", "DEV LOG", "RAW DIFFS", "TASK LIST"} {
		if !strings.Contains(CompanionSystemPrompt, section) {
			t.Errorf("system prompt missing section %q", section)
		}
	}
}

func TestBuildCompanionPromptIncludesAllSectionHeaders(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: "fix the bug"})
	for _, header := range []string{
		"ORIGINAL TASK:",
		"USER UPDATES:",
		"DEV LOG:",
		"RAW DIFFS:",
		"TASK LIST:",
	} {
		if !strings.Contains(got, header) {
			t.Errorf("prompt missing section header %q\nprompt:\n%s", header, got)
		}
	}
}

func TestBuildCompanionPromptEmptySectionsSayNone(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: "fix the bug"})
	// ORIGINAL TASK is non-empty so only the other four sections should
	// contain (none).
	if count := strings.Count(got, "(none)"); count != 4 {
		t.Errorf("expected 4 '(none)' markers, got %d\nprompt:\n%s", count, got)
	}
}

func TestBuildCompanionPromptEmptyTaskReportsNone(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: ""})
	// All five sections empty.
	if count := strings.Count(got, "(none)"); count != 5 {
		t.Errorf("expected 5 '(none)' markers, got %d\nprompt:\n%s", count, got)
	}
}

func TestBuildCompanionPromptIncludesOriginalTask(t *testing.T) {
	task := "Fix the 500 error on /api/recommendations"
	got := BuildCompanionPrompt(CompanionInput{Task: task})
	if !strings.Contains(got, task) {
		t.Errorf("prompt missing original task text:\n%s", got)
	}
}

func TestBuildCompanionPromptIncludesUserUpdates(t *testing.T) {
	in := CompanionInput{
		Task: "fix the api",
		Updates: []UserUpdate{
			{TS: "2026-04-22T22:10:00Z", Prompt: "Also check the DB layer"},
			{TS: "2026-04-22T22:20:00Z", Prompt: "Never mind, try HTTP"},
		},
	}
	got := BuildCompanionPrompt(in)
	if !strings.Contains(got, "Also check the DB layer") {
		t.Errorf("prompt missing first update:\n%s", got)
	}
	if !strings.Contains(got, "Never mind, try HTTP") {
		t.Errorf("prompt missing second update:\n%s", got)
	}
	if !strings.Contains(got, "2026-04-22T22:10:00Z") {
		t.Errorf("prompt missing first update timestamp:\n%s", got)
	}
}

func TestBuildCompanionPromptIncludesLogEntries(t *testing.T) {
	base := time.Date(2026, 4, 22, 22, 15, 0, 0, time.UTC)
	in := CompanionInput{
		Task: "fix api",
		LogEntries: []devlog.Entry{
			{Seq: 5, TS: base, Summary: "Third consecutive attempt targeting DB"},
			{Seq: 6, TS: base.Add(time.Minute), Summary: "Rewriting query with index hints"},
		},
	}
	got := BuildCompanionPrompt(in)
	if !strings.Contains(got, "Log #5") {
		t.Errorf("prompt missing 'Log #5' citation anchor:\n%s", got)
	}
	if !strings.Contains(got, "Log #6") {
		t.Errorf("prompt missing 'Log #6' citation anchor:\n%s", got)
	}
	if !strings.Contains(got, "Third consecutive attempt targeting DB") {
		t.Errorf("prompt missing summary text:\n%s", got)
	}
}

func TestBuildCompanionPromptRespectsLogLimit(t *testing.T) {
	var entries []devlog.Entry
	for i := 1; i <= 30; i++ {
		entries = append(entries, devlog.Entry{Seq: i, Summary: "s" + string(rune('A'+i%26))})
	}
	in := CompanionInput{
		Task:          "t",
		LogEntries:    entries,
		MaxLogEntries: 25,
	}
	got := BuildCompanionPrompt(in)
	// The oldest 5 (seq 1..5) should be trimmed; seq 6..30 should remain.
	for i := 1; i <= 5; i++ {
		bad := "Log #" + itoa(i) + " ["
		if strings.Contains(got, bad) {
			t.Errorf("prompt should have trimmed %q from oldest entries\n%s", bad, got)
		}
	}
	for i := 6; i <= 30; i++ {
		want := "Log #" + itoa(i)
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing kept entry %q", want)
		}
	}
}

func TestBuildCompanionPromptLogLimitDefaultsToTwentyFive(t *testing.T) {
	var entries []devlog.Entry
	for i := 1; i <= 40; i++ {
		entries = append(entries, devlog.Entry{Seq: i, Summary: "x"})
	}
	in := CompanionInput{Task: "t", LogEntries: entries} // MaxLogEntries=0 → default
	got := BuildCompanionPrompt(in)
	// With default 25 kept, seqs 1..15 trimmed, 16..40 kept.
	if strings.Contains(got, "Log #1 ") || strings.Contains(got, "Log #15 ") {
		t.Errorf("default limit should trim oldest entries")
	}
	if !strings.Contains(got, "Log #16") || !strings.Contains(got, "Log #40") {
		t.Errorf("default limit should keep 25 newest entries")
	}
}

func TestBuildCompanionPromptRespectsDiffLimit(t *testing.T) {
	var entries []buffer.Entry
	for i := 1; i <= 60; i++ {
		entries = append(entries, buffer.Entry{Seq: i, Tool: "Edit", File: "f.go", Changed: true, TS: "2026-04-22T22:15:00Z"})
	}
	in := CompanionInput{
		Task:           "t",
		DiffArchive:    entries,
		MaxDiffEntries: 50,
	}
	got := BuildCompanionPrompt(in)
	// Seqs 1..10 trimmed, 11..60 kept.
	if strings.Contains(got, "#1 [") && !strings.Contains(got, "#11 [") {
		t.Errorf("limit not respected")
	}
	if !strings.Contains(got, "#11 [") {
		t.Errorf("diff #11 should be present after trimming oldest 10")
	}
	if !strings.Contains(got, "#60 [") {
		t.Errorf("diff #60 (newest) should be present")
	}
}

func TestBuildCompanionPromptDiffLimitDefaultsToFifty(t *testing.T) {
	var entries []buffer.Entry
	for i := 1; i <= 100; i++ {
		entries = append(entries, buffer.Entry{Seq: i, Tool: "Bash", Changed: false, TS: "2026-04-22T22:15:00Z"})
	}
	got := BuildCompanionPrompt(CompanionInput{Task: "t", DiffArchive: entries})
	if !strings.Contains(got, "#51 [") || !strings.Contains(got, "#100 [") {
		t.Errorf("default diff limit should keep seqs 51..100")
	}
}

func TestBuildCompanionPromptIncludesTaskList(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"subject": "Run tests", "status": "pending"})
	in := CompanionInput{
		Task: "t",
		TaskList: []TaskListRecord{
			{TS: "2026-04-22T22:05:00Z", ToolName: "TaskCreate", Payload: payload},
		},
	}
	got := BuildCompanionPrompt(in)
	if !strings.Contains(got, "TaskCreate") {
		t.Errorf("prompt missing TaskCreate:\n%s", got)
	}
	if !strings.Contains(got, "Run tests") {
		t.Errorf("prompt missing payload contents:\n%s", got)
	}
}

func TestBuildCompanionPromptTaskListNilIsNone(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: "t", TaskList: nil})
	// Locate the TASK LIST: section and verify it says (none).
	idx := strings.Index(got, "TASK LIST:")
	if idx == -1 {
		t.Fatalf("no TASK LIST: header")
	}
	tail := got[idx:]
	if !strings.Contains(tail, "(none)") {
		t.Errorf("TASK LIST section should contain (none) when nil:\n%s", tail)
	}
}

func TestBuildCompanionPromptDescribesOutputJSONSchema(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: "t"})
	for _, key := range []string{
		`"status"`,
		`"confidence"`,
		`"pattern"`,
		`"evidence"`,
		`"summary"`,
		`"intervention"`,
		`"reframe"`,
	} {
		if !strings.Contains(got, key) {
			t.Errorf("output-format instructions missing key %s\n%s", key, got)
		}
	}
}

func TestBuildCompanionPromptOrderOfSections(t *testing.T) {
	got := BuildCompanionPrompt(CompanionInput{Task: "t"})
	order := []string{
		"ORIGINAL TASK:",
		"USER UPDATES:",
		"DEV LOG:",
		"RAW DIFFS:",
		"TASK LIST:",
	}
	prev := -1
	for _, header := range order {
		idx := strings.Index(got, header)
		if idx == -1 {
			t.Fatalf("missing header %q", header)
		}
		if idx < prev {
			t.Errorf("header %q appears before previous section (idx=%d, prev=%d)", header, idx, prev)
		}
		prev = idx
	}
}

// itoa is a tiny integer-to-string helper; we avoid strconv to keep the
// tests' external dependencies focused.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
