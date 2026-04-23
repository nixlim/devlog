package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlog/internal/devlog"
)

// setupLogProject returns a project root with .git and .devlog/ and
// returns the path to log.jsonl inside that .devlog/.
func setupLogProject(t *testing.T) (root, logPath string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}
	return root, filepath.Join(root, ".devlog", "log.jsonl")
}

func seedLog(t *testing.T, path string, entries ...devlog.Entry) {
	t.Helper()
	for _, e := range entries {
		if err := devlog.Append(path, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func mkLogEntry(seq int, summary string) devlog.Entry {
	return devlog.Entry{
		Seq:       seq,
		TS:        time.Date(2026, 4, 22, 22, 10+seq, 0, 0, time.UTC),
		SessionID: "abc123",
		Summary:   summary,
		Model:     "claude-haiku-4-5-20251001",
	}
}

func TestLogEmptyFile(t *testing.T) {
	_, logPath := setupLogProject(t)

	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(logPath, 0, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0 (stderr=%q)", rc, stderr.String())
	}
	if stdout.String() != "(no entries)\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "(no entries)\n")
	}
}

func TestLogMissingFile(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(filepath.Join(root, "log.jsonl"), 0, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	if stdout.String() != "(no entries)\n" {
		t.Errorf("missing log should render '(no entries)', got %q", stdout.String())
	}
}

func TestLogFormattedFull(t *testing.T) {
	_, logPath := setupLogProject(t)
	seedLog(t, logPath,
		mkLogEntry(1, "Increase db timeout"),
		mkLogEntry(2, "Tune pool size"),
		mkLogEntry(3, "Third attempt targeting database layer"),
	)

	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(logPath, 0, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d (stderr=%q)", rc, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		"#1 [2026-04-22T22:11:00Z] Increase db timeout",
		"#2 [2026-04-22T22:12:00Z] Tune pool size",
		"#3 [2026-04-22T22:13:00Z] Third attempt targeting database layer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
	// Ordering check: the first seq appears before the last one.
	idx1 := strings.Index(out, "#1 ")
	idx3 := strings.Index(out, "#3 ")
	if idx1 < 0 || idx3 < 0 || idx1 > idx3 {
		t.Errorf("expected entries in order, got positions %d and %d", idx1, idx3)
	}
}

func TestLogTail(t *testing.T) {
	_, logPath := setupLogProject(t)
	seedLog(t, logPath,
		mkLogEntry(1, "first"),
		mkLogEntry(2, "second"),
		mkLogEntry(3, "third"),
		mkLogEntry(4, "fourth"),
		mkLogEntry(5, "fifth"),
	)

	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(logPath, 2, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d (stderr=%q)", rc, stderr.String())
	}

	out := stdout.String()
	if strings.Contains(out, "first") || strings.Contains(out, "third") {
		t.Errorf("--tail 2 should drop older entries, got:\n%s", out)
	}
	if !strings.Contains(out, "fourth") || !strings.Contains(out, "fifth") {
		t.Errorf("--tail 2 missing last entries:\n%s", out)
	}
	// Exactly two lines.
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), lines)
	}
}

func TestLogTailLargerThanFile(t *testing.T) {
	_, logPath := setupLogProject(t)
	seedLog(t, logPath, mkLogEntry(1, "only"))

	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(logPath, 100, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(stdout.String(), "#1 ") {
		t.Errorf("expected #1 in output, got %q", stdout.String())
	}
}

func TestLogJSONModeEmitsRawBytes(t *testing.T) {
	_, logPath := setupLogProject(t)
	seedLog(t, logPath,
		mkLogEntry(1, "hello"),
		mkLogEntry(2, "world"),
	)

	var stdout, stderr bytes.Buffer
	rc := printLogRaw(logPath, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d (stderr=%q)", rc, stderr.String())
	}

	wantBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if stdout.String() != string(wantBytes) {
		t.Errorf("--json output does not match raw file\ngot: %q\nwant: %q", stdout.String(), string(wantBytes))
	}
}

func TestLogJSONModeMissingFileEmitsNothing(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	rc := printLogRaw(filepath.Join(root, "log.jsonl"), &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if stdout.Len() != 0 {
		t.Errorf("missing file should produce empty stdout in --json mode, got %q", stdout.String())
	}
}

func TestLogFormattedRejectsCorruptEntry(t *testing.T) {
	_, logPath := setupLogProject(t)
	// Write a valid line followed by garbage.
	if err := devlog.Append(logPath, mkLogEntry(1, "valid")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("{not json\n"); err != nil {
		t.Fatalf("Write garbage: %v", err)
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	rc := printLogFormatted(logPath, 0, &stdout, &stderr)
	if rc == 0 {
		t.Errorf("expected non-zero rc for corrupt line, got 0 (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "decode") {
		t.Errorf("expected 'decode' in stderr, got %q", stderr.String())
	}
}

func TestLogWriteLogLineEmptySummary(t *testing.T) {
	var buf bytes.Buffer
	writeLogLine(&buf, devlog.Entry{Seq: 7, TS: time.Unix(0, 0).UTC()})
	if !strings.Contains(buf.String(), "(empty)") {
		t.Errorf("empty summary should render as (empty), got %q", buf.String())
	}
}

func TestLogNegativeTailRejected(t *testing.T) {
	// Drive Log() through dispatch so the flag parse path is exercised.
	// Swap stderr to capture diagnostics.
	origErr := stderrWriter
	defer func() { stderrWriter = origErr }()
	var errBuf bytes.Buffer
	stderrWriter = &errBuf

	rc := Log([]string{"--tail", "-1"})
	if rc != 2 {
		t.Errorf("negative tail should exit 2, got %d", rc)
	}
}

func TestLogProjectFlagResolution(t *testing.T) {
	root, logPath := setupLogProject(t)
	seedLog(t, logPath, mkLogEntry(1, "resolved"))

	origOut := stdoutWriter
	origErr := stderrWriter
	defer func() {
		stdoutWriter = origOut
		stderrWriter = origErr
	}()
	var outBuf, errBuf bytes.Buffer
	stdoutWriter = &outBuf
	stderrWriter = &errBuf

	rc := Log([]string{"--project", root})
	if rc != 0 {
		t.Fatalf("rc = %d (stderr=%q)", rc, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "resolved") {
		t.Errorf("output missing expected summary. got:\n%s", outBuf.String())
	}
}
