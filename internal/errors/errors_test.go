package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewError(t *testing.T) {
	e := New("init", "no git repository found")
	want := "devlog: error: init: no git repository found"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if e.Cause != nil {
		t.Fatalf("New should not set Cause, got %v", e.Cause)
	}
	if e.Remediation != "" {
		t.Fatalf("New should not set Remediation, got %q", e.Remediation)
	}
}

func TestWrapPreservesCause(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	e := Wrap("capture", "read failed", inner)

	if !stderrors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatalf("errors.Is should match the wrapped cause")
	}

	var target *DevlogError
	if !stderrors.As(e, &target) {
		t.Fatalf("errors.As should unwrap to *DevlogError")
	}
	if target.Component != "capture" {
		t.Errorf("Component = %q, want capture", target.Component)
	}
}

func TestUnwrapNilSafe(t *testing.T) {
	var e *DevlogError
	if got := e.Unwrap(); got != nil {
		t.Errorf("nil.Unwrap() = %v, want nil", got)
	}
	if got := e.Error(); got != "" {
		t.Errorf("nil.Error() = %q, want empty", got)
	}
	if got := e.Format(true); got != "" {
		t.Errorf("nil.Format(true) = %q, want empty", got)
	}
}

func TestWithRemediationCopies(t *testing.T) {
	base := New("flush", "boom")
	enriched := base.WithRemediation("try: devlog flush")

	if base.Remediation != "" {
		t.Errorf("WithRemediation mutated original: %q", base.Remediation)
	}
	if enriched.Remediation != "try: devlog flush" {
		t.Errorf("WithRemediation did not set field: %q", enriched.Remediation)
	}
	if enriched == base {
		t.Errorf("WithRemediation should return a new pointer")
	}
}

func TestFormatCompactExcludesDetail(t *testing.T) {
	e := Wrap("flush", "summarizer returned empty", stderrors.New("empty body")).
		WithRemediation("To retry now: devlog flush")

	compact := e.Format(false)
	want := "devlog: error: flush: summarizer returned empty"
	if compact != want {
		t.Errorf("Format(false) = %q, want %q", compact, want)
	}
	if strings.Contains(compact, "empty body") {
		t.Errorf("Format(false) leaked cause: %q", compact)
	}
	if strings.Contains(compact, "devlog flush") {
		t.Errorf("Format(false) leaked remediation: %q", compact)
	}
}

func TestFormatFullIncludesAllFourParts(t *testing.T) {
	e := Wrap("flush", "summarizer returned empty", stderrors.New("empty body")).
		WithRemediation("To retry now: devlog flush")

	full := e.Format(true)
	for _, want := range []string{
		"devlog: error: flush",       // component
		"summarizer returned empty",  // message
		"empty body",                 // cause
		"To retry now: devlog flush", // remediation
	} {
		if !strings.Contains(full, want) {
			t.Errorf("Format(true) missing %q\ngot:\n%s", want, full)
		}
	}
}

func TestFormatFullOmitsMissingFields(t *testing.T) {
	// No cause, no remediation: Format(true) should degrade to Error()
	e := New("init", "kaboom")
	if got := e.Format(true); got != e.Error() {
		t.Errorf("Format(true) with no extras = %q, want %q", got, e.Error())
	}

	// Cause only, no remediation.
	e2 := Wrap("init", "kaboom", stderrors.New("kernel panic"))
	full := e2.Format(true)
	if !strings.Contains(full, "kernel panic") {
		t.Errorf("Format(true) missing cause: %q", full)
	}
	if strings.Contains(full, "\n\n") {
		t.Errorf("Format(true) should not contain remediation separator when unset: %q", full)
	}
}

func TestWriteToLogCreatesFileAndAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.log")

	first := New("init", "no git").WithRemediation("git init")
	if err := first.WriteToLog(path); err != nil {
		t.Fatalf("WriteToLog #1: %v", err)
	}
	second := Wrap("flush", "empty response", stderrors.New("eof"))
	if err := second.WriteToLog(path); err != nil {
		t.Fatalf("WriteToLog #2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("log file should end in newline, got %q", string(data))
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}

	var firstEntry logEntry
	if err := json.Unmarshal([]byte(lines[0]), &firstEntry); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if firstEntry.Component != "init" || firstEntry.Message != "no git" || firstEntry.Remediation != "git init" {
		t.Errorf("line 0 payload unexpected: %+v", firstEntry)
	}
	if firstEntry.Cause != "" {
		t.Errorf("line 0 should have no cause, got %q", firstEntry.Cause)
	}
	if firstEntry.TS == "" {
		t.Errorf("line 0 missing timestamp")
	}

	var secondEntry logEntry
	if err := json.Unmarshal([]byte(lines[1]), &secondEntry); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if secondEntry.Cause != "eof" {
		t.Errorf("line 1 cause = %q, want eof", secondEntry.Cause)
	}
	if secondEntry.Remediation != "" {
		t.Errorf("line 1 should have no remediation, got %q", secondEntry.Remediation)
	}
}

func TestWriteToLogConcurrentProducesTenValidLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.log")

	const goroutines = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // maximise contention by releasing all at once
			e := New("capture", fmt.Sprintf("entry %d", i)).
				WithRemediation(fmt.Sprintf("inspect entry %d", i))
			if err := e.WriteToLog(path); err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("WriteToLog failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) != goroutines {
		t.Fatalf("expected %d lines, got %d. raw:\n%s", goroutines, len(lines), string(data))
	}

	seen := make(map[string]bool, goroutines)
	for i, line := range lines {
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is not valid JSON: %q (err=%v)", i, line, err)
		}
		if entry.Component != "capture" {
			t.Errorf("line %d component = %q, want capture", i, entry.Component)
		}
		seen[entry.Message] = true
	}
	if len(seen) != goroutines {
		t.Errorf("expected %d distinct messages, saw %d: %v", goroutines, len(seen), seen)
	}
}

func TestWriteToLogNilReceiver(t *testing.T) {
	var e *DevlogError
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.log")
	if err := e.WriteToLog(path); err != nil {
		t.Errorf("nil.WriteToLog returned err: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("nil.WriteToLog should not create the file, stat err = %v", err)
	}
}

func TestWriteToLogInvalidPath(t *testing.T) {
	e := New("init", "test")
	// A path inside a non-existent directory should surface the OS error.
	bogus := filepath.Join(t.TempDir(), "does", "not", "exist", "errors.log")
	if err := e.WriteToLog(bogus); err == nil {
		t.Errorf("expected error writing to %s, got nil", bogus)
	}
}
