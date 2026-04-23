package devlog

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	devlogerrors "devlog/internal/errors"
)

func sampleEntry(seq int) Entry {
	return Entry{
		Seq:        seq,
		TS:         time.Date(2026, 4, 22, 22, 15, seq, 0, time.UTC),
		SessionID:  "sess-xyz",
		CoversSeqs: []int{seq*10 + 1, seq*10 + 2},
		Summary:    fmt.Sprintf("summary %d", seq),
		Model:      "claude-haiku-4-5-20251001",
		DurationMS: 1000 + seq,
	}
}

func TestAppendCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	if err := Append(path, sampleEntry(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("expected trailing newline, got %q", string(data))
	}
	var got Entry
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n")), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := sampleEntry(1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestAppendAppendsInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	for i := 1; i <= 3; i++ {
		if err := Append(path, sampleEntry(i)); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if e.Seq != i+1 {
			t.Errorf("line %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestReadLastNOnMissingFileReturnsNil(t *testing.T) {
	entries, err := ReadLastN(filepath.Join(t.TempDir(), "nope.jsonl"), 5)
	if err != nil {
		t.Errorf("ReadLastN(missing) err = %v", err)
	}
	if entries != nil {
		t.Errorf("ReadLastN(missing) = %v, want nil", entries)
	}
}

func TestReadLastNZeroReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	if err := Append(path, sampleEntry(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := ReadLastN(path, 0)
	if err != nil {
		t.Fatalf("ReadLastN(0): %v", err)
	}
	if got != nil {
		t.Errorf("ReadLastN(0) = %v, want nil", got)
	}
}

func TestReadLastNNegativeErrors(t *testing.T) {
	got, err := ReadLastN(filepath.Join(t.TempDir(), "x.jsonl"), -1)
	if err == nil {
		t.Errorf("expected error for n<0, got %v", got)
	}
}

func TestReadLastNReturnsAllWhenFewerThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	for i := 1; i <= 3; i++ {
		if err := Append(path, sampleEntry(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := ReadLastN(path, 5)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	for i, e := range got {
		if e.Seq != i+1 {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestReadLastNReturnsLastN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	for i := 1; i <= 10; i++ {
		if err := Append(path, sampleEntry(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := ReadLastN(path, 5)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(got))
	}
	// Must be the LAST 5 (seq 6..10), in original file order.
	for i, e := range got {
		if e.Seq != i+6 {
			t.Errorf("entry %d seq = %d, want %d", i, e.Seq, i+6)
		}
	}
}

func TestReadLastNSkipsBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	first, _ := json.Marshal(sampleEntry(1))
	second, _ := json.Marshal(sampleEntry(2))
	// Deliberately sprinkle blank lines — these can happen if the file
	// was edited by hand. Parser should skip silently.
	body := string(first) + "\n\n" + string(second) + "\n\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadLastN(path, 10)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
}

func TestReadLastNCorruptLineSurfacesError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	first, _ := json.Marshal(sampleEntry(1))
	body := string(first) + "\nnot-json\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := ReadLastN(path, 5)
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected DevlogError, got %T: %v", err, err)
	}
	if de.Component != "devlog" {
		t.Errorf("component = %q, want devlog", de.Component)
	}
	if !strings.Contains(de.Remediation, path) {
		t.Errorf("remediation should mention the file: %q", de.Remediation)
	}
}

func TestAppendAndReadRoundTripPreservesFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	want := sampleEntry(42)
	if err := Append(path, want); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := ReadLastN(path, 1)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	// time.Time round-trips through JSON as RFC3339, which may drop sub-
	// second precision that was zero anyway. Compare with Equal.
	if !got[0].TS.Equal(want.TS) {
		t.Errorf("TS mismatch: got %v, want %v", got[0].TS, want.TS)
	}
	got[0].TS = want.TS
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got[0], want)
	}
}

func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	const goroutines = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := Append(path, sampleEntry(i+1)); err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Append err: %v", err)
	}

	// Every line must parse as a complete Entry.
	got, err := ReadLastN(path, goroutines)
	if err != nil {
		t.Fatalf("ReadLastN: %v", err)
	}
	if len(got) != goroutines {
		t.Fatalf("expected %d entries, got %d", goroutines, len(got))
	}
	seen := make(map[int]bool, goroutines)
	for _, e := range got {
		seen[e.Seq] = true
	}
	if len(seen) != goroutines {
		t.Errorf("expected %d distinct seqs, saw %d: %v", goroutines, len(seen), seen)
	}
}

func TestAppendMarshalError(t *testing.T) {
	// Entry has no non-marshalable field, so we cannot directly force a
	// marshal failure without reflection tricks. Instead confirm that
	// writes to an invalid path return DevlogError.
	err := Append(filepath.Join(t.TempDir(), "does", "not", "exist.jsonl"), sampleEntry(1))
	if err == nil {
		t.Fatalf("expected error writing to invalid dir")
	}
	var de *devlogerrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected DevlogError, got %T", err)
	}
}
