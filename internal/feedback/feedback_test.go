package feedback

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleResult returns the companion result from SPEC §4 verbatim.
func sampleResult() CompanionResult {
	return CompanionResult{
		Status:     "spiraling",
		Confidence: 0.85,
		Pattern:    "repetition_lock",
		Evidence: []string{
			`Log #3: 'Increasing database connection pool from 10 to 25'`,
			`Log #5: 'Rewriting query with explicit index hints'`,
			`Log #7: 'Third consecutive attempt targeting the database layer'`,
		},
		Summary:      "Agent has made 6 consecutive database-layer modifications targeting timeout behavior, none resolving the 500 error.",
		Intervention: "STOP. You've made 6 database-related changes and the error is unchanged.",
		Reframe:      `Instead of asking "how do I fix the database timeout", ask "what are ALL the things that could produce a timeout in this request path?"`,
	}
}

func TestFormat_HasAllSections(t *testing.T) {
	got := Format(sampleResult())

	required := []string{
		"━",
		"[DevLog Companion — Trajectory Assessment]",
		"STATUS: SPIRALING (confidence: 85%)",
		"PATTERN DETECTED: Repetition Lock",
		"EVIDENCE:",
		"  - Log #3:",
		"REFRAME:",
		"ACTION:",
	}
	for _, want := range required {
		if !strings.Contains(got, want) {
			t.Errorf("Format output missing %q\n----\n%s\n----", want, got)
		}
	}

	// Must start and end with the banner line.
	if !strings.HasPrefix(got, banner+"\n") {
		t.Errorf("expected banner at start, got prefix %q", got[:minLen(got, 80)])
	}
	if !strings.HasSuffix(got, banner+"\n") {
		t.Errorf("expected banner at end, got suffix %q", got[maxLen(got, 80):])
	}
}

func TestFormat_ConfidenceRounding(t *testing.T) {
	cases := []struct {
		conf float64
		want string
	}{
		{0.0, "STATUS: ON TRACK (confidence: 0%)"},
		{0.495, "confidence: 50%"},
		{0.854, "confidence: 85%"},
		{0.999, "confidence: 100%"}, // 99.9 rounds to 100
		{1.0, "confidence: 100%"},
		{-0.5, "confidence: 0%"},   // clamped
		{42.0, "confidence: 100%"}, // clamped
	}
	for _, c := range cases {
		r := CompanionResult{Status: "on_track", Confidence: c.conf}
		got := Format(r)
		if !strings.Contains(got, c.want) {
			t.Errorf("conf=%v: missing %q in output:\n%s", c.conf, c.want, got)
		}
	}
}

func TestFormat_OnTrackSkipsEmptySections(t *testing.T) {
	r := CompanionResult{Status: "on_track", Confidence: 0.95}
	got := Format(r)

	// Empty sections must not produce their labels.
	for _, label := range []string{"PATTERN DETECTED", "EVIDENCE:", "REFRAME:", "ACTION:"} {
		if strings.Contains(got, label) {
			t.Errorf("on_track output should omit %q:\n%s", label, got)
		}
	}
	// But the banner and status line are still required.
	if !strings.Contains(got, "STATUS: ON TRACK") {
		t.Errorf("on_track output missing status line:\n%s", got)
	}
}

func TestFormat_HumanizesMultiUnderscorePatterns(t *testing.T) {
	r := CompanionResult{Status: "spiraling", Confidence: 0.5, Pattern: "mock_stub_escape"}
	got := Format(r)
	if !strings.Contains(got, "PATTERN DETECTED: Mock Stub Escape") {
		t.Errorf("humanize failed; got:\n%s", got)
	}
}

func TestNeedsIntervention(t *testing.T) {
	cases := map[string]bool{
		StatusOnTrack:   false,
		StatusDrifting:  true,
		StatusSpiraling: true,
		"":              false,
		"unknown":       false,
	}
	for status, want := range cases {
		got := CompanionResult{Status: status}.NeedsIntervention()
		if got != want {
			t.Errorf("status=%q: got %v, want %v", status, got, want)
		}
	}
}

func TestWrite_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")

	if err := Write(path, "first\n"); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first\n" {
		t.Fatalf("after first write: data=%q err=%v", data, err)
	}

	if err := Write(path, "replacement payload"); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "replacement payload" {
		t.Fatalf("after replace: data=%q err=%v", data, err)
	}

	// No leftover temp files should remain in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".feedback.") && strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}

func TestRead_MissingReturnsEmpty(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != "" {
		t.Errorf("missing file should return empty, got %q", got)
	}
}

func TestRead_EmptyReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "" {
		t.Errorf("empty file should return empty, got %q", got)
	}
}

func TestRead_ReturnsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	const payload = "here is the banner\nwith multiple lines"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != payload {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestTruncate_ArchivesThenEmptiesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	archive := filepath.Join(dir, "feedback_archive.jsonl")

	const payload = "━━━\nbanner content here\n━━━\n"
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Truncate(path, archive)
	if err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if got != payload {
		t.Errorf("returned content mismatch: got %q, want %q", got, payload)
	}

	// feedback.md should now exist and be empty (not deleted).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after truncate: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("after truncate size=%d, want 0", info.Size())
	}

	// Archive should contain one JSONL entry with our content.
	assertArchiveHas(t, archive, []string{payload})
}

func TestTruncate_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	archive := filepath.Join(dir, "feedback_archive.jsonl")

	got, err := Truncate(path, archive)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != "" {
		t.Errorf("missing file should return empty, got %q", got)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("archive should not have been created, stat=%v", err)
	}
}

func TestTruncate_EmptyFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	archive := filepath.Join(dir, "feedback_archive.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := Truncate(path, archive)
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if got != "" {
		t.Errorf("empty file should return empty, got %q", got)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("archive should not have been created, stat=%v", err)
	}
}

func TestTruncate_AppendsMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.md")
	archive := filepath.Join(dir, "feedback_archive.jsonl")

	first := "first feedback"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatalf("setup 1: %v", err)
	}
	if _, err := Truncate(path, archive); err != nil {
		t.Fatalf("Truncate 1: %v", err)
	}

	second := "second feedback"
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatalf("setup 2: %v", err)
	}
	if _, err := Truncate(path, archive); err != nil {
		t.Fatalf("Truncate 2: %v", err)
	}

	assertArchiveHas(t, archive, []string{first, second})
}

// --- helpers -------------------------------------------------------------

func assertArchiveHas(t *testing.T, path string, wantContents []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var gotContents []string
	var gotTimestamps []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var entry struct {
			TS      string `json:"ts"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("bad archive line %q: %v", scanner.Text(), err)
		}
		if entry.TS == "" {
			t.Errorf("archive entry missing ts: %q", scanner.Text())
		}
		gotTimestamps = append(gotTimestamps, entry.TS)
		gotContents = append(gotContents, entry.Content)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(gotContents) != len(wantContents) {
		t.Fatalf("archive line count: got %d, want %d\nraw=%s",
			len(gotContents), len(wantContents), data)
	}
	for i := range wantContents {
		if gotContents[i] != wantContents[i] {
			t.Errorf("archive[%d].content: got %q, want %q",
				i, gotContents[i], wantContents[i])
		}
	}
}

func minLen(s string, n int) int {
	if len(s) < n {
		return len(s)
	}
	return n
}

func maxLen(s string, n int) int {
	if len(s) < n {
		return 0
	}
	return len(s) - n
}
