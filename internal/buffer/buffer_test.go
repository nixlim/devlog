package buffer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func mkEntry(seq int, session string) Entry {
	return Entry{
		Seq:       seq,
		TS:        "2026-04-22T22:15:00Z",
		SessionID: session,
		Tool:      "Edit",
		File:      "src/api/handler.go",
		Detail:    fmt.Sprintf("entry %d detail", seq),
		DiffLines: 4,
		Changed:   true,
	}
}

func TestAppendReadAllRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")

	want := []Entry{mkEntry(1, "abc"), mkEntry(2, "abc"), mkEntry(3, "abc")}
	for _, e := range want {
		if err := Append(path, e); err != nil {
			t.Fatalf("Append(%d): %v", e.Seq, err)
		}
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (raw: %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAppendWritesOneJSONLinePerEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")

	if err := Append(path, mkEntry(1, "a")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Append(path, mkEntry(2, "a")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d invalid JSON: %q (err=%v)", i, line, err)
		}
	}
}

func TestReadAllMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil entries, got %+v", got)
	}
}

func TestReadAllEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no entries, got %+v", got)
	}
}

func TestReadAllRejectsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")
	if err := os.WriteFile(path, []byte("{not valid\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ReadAll(path)
	if err == nil {
		t.Fatalf("expected decode error")
	}
}

func TestArchiveMovesLinesAndTruncatesBuffer(t *testing.T) {
	dir := t.TempDir()
	bufferPath := filepath.Join(dir, "buffer.jsonl")
	archivePath := filepath.Join(dir, "buffer_archive.jsonl")

	for i := 1; i <= 3; i++ {
		if err := Append(bufferPath, mkEntry(i, "abc")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if err := Archive(bufferPath, archivePath); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	bufData, err := os.ReadFile(bufferPath)
	if err != nil {
		t.Fatalf("ReadFile(buffer): %v", err)
	}
	if len(bufData) != 0 {
		t.Errorf("expected buffer truncated, got %q", string(bufData))
	}

	archData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile(archive): %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(archData), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("archive has %d lines, want 3: %q", len(lines), string(archData))
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("archive line %d invalid JSON: %q (err=%v)", i, line, err)
		}
	}
}

func TestArchiveAppendsToExistingArchive(t *testing.T) {
	dir := t.TempDir()
	bufferPath := filepath.Join(dir, "buffer.jsonl")
	archivePath := filepath.Join(dir, "buffer_archive.jsonl")

	// First round.
	if err := Append(bufferPath, mkEntry(1, "a")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Archive(bufferPath, archivePath); err != nil {
		t.Fatalf("Archive #1: %v", err)
	}

	// Second round.
	if err := Append(bufferPath, mkEntry(2, "a")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Archive(bufferPath, archivePath); err != nil {
		t.Fatalf("Archive #2: %v", err)
	}

	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile(archive): %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("archive has %d lines after two rounds, want 2: %q", len(lines), string(data))
	}
}

func TestArchiveOnEmptyBufferIsNoOp(t *testing.T) {
	dir := t.TempDir()
	bufferPath := filepath.Join(dir, "buffer.jsonl")
	archivePath := filepath.Join(dir, "buffer_archive.jsonl")

	if err := Archive(bufferPath, archivePath); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Errorf("archive should not be created when buffer is absent, stat err = %v", err)
	}
}

func TestClearTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")

	if err := Append(path, mkEntry(1, "a")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Clear(path); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty buffer after Clear, got %q", string(data))
	}
}

func TestClearMissingIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")
	if err := Clear(path); err != nil {
		t.Errorf("Clear on missing should be no-op, got %v", err)
	}
}

func TestAppendConcurrentProducesAllEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")

	const goroutines = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := Append(path, mkEntry(i, "race")); err != nil {
				errCh <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("Append failed: %v", err)
	}

	entries, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != goroutines {
		t.Fatalf("len(entries) = %d, want %d (flock not serialising?)", len(entries), goroutines)
	}

	seen := make(map[int]bool, goroutines)
	for _, e := range entries {
		seen[e.Seq] = true
	}
	if len(seen) != goroutines {
		t.Errorf("expected %d distinct seq values, got %d", goroutines, len(seen))
	}
}

func TestAppendArchiveInterleaveIsSerialised(t *testing.T) {
	dir := t.TempDir()
	bufferPath := filepath.Join(dir, "buffer.jsonl")
	archivePath := filepath.Join(dir, "buffer_archive.jsonl")

	const appenders = 10
	var wg sync.WaitGroup

	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Append(bufferPath, mkEntry(i, "mix")); err != nil {
				t.Errorf("Append: %v", err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := Archive(bufferPath, archivePath); err != nil {
			t.Errorf("Archive: %v", err)
		}
	}()
	wg.Wait()

	// Invariant: every appended entry now lives in exactly one of the
	// two files, and each file parses cleanly as JSONL.
	total := 0
	for _, p := range []string{bufferPath, archivePath} {
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		trimmed := strings.TrimSuffix(string(data), "\n")
		if trimmed == "" {
			continue
		}
		for i, line := range strings.Split(trimmed, "\n") {
			var e Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Errorf("%s line %d invalid JSON: %q", p, i, line)
			}
			total++
		}
	}
	if total != appenders {
		t.Errorf("expected %d total entries across buffer+archive, got %d", appenders, total)
	}
}
