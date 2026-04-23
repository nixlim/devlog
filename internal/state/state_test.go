package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "no-such-state.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	want := &State{
		SessionID:         "abc123",
		StartedAt:         "2026-04-22T22:00:00Z",
		BufferCount:       3,
		BufferSeq:         45,
		LogCount:          8,
		LogSeq:            8,
		LogSinceCompanion: 3,
		LastCompanion: &LastCompanion{
			TS:         "2026-04-22T22:14:00Z",
			Status:     "on_track",
			Confidence: 0.92,
		},
		FlushInProgress:     false,
		CompanionInProgress: true,
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SessionID != want.SessionID ||
		got.BufferCount != want.BufferCount ||
		got.BufferSeq != want.BufferSeq ||
		got.LogCount != want.LogCount ||
		got.LogSeq != want.LogSeq ||
		got.LogSinceCompanion != want.LogSinceCompanion ||
		got.FlushInProgress != want.FlushInProgress ||
		got.CompanionInProgress != want.CompanionInProgress {
		t.Errorf("Load returned %+v, want %+v", got, want)
	}
	if got.LastCompanion == nil ||
		got.LastCompanion.Status != "on_track" ||
		got.LastCompanion.Confidence != 0.92 {
		t.Errorf("LastCompanion lost in roundtrip: %+v", got.LastCompanion)
	}
}

func TestSaveWritesPrettyJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, &State{SessionID: "pretty"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// MarshalIndent with "  " puts each JSON field on its own line.
	if !strings.Contains(string(data), "\n  \"session_id\"") {
		t.Errorf("expected indented JSON, got:\n%s", string(data))
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("expected trailing newline, got: %q", string(data))
	}
}

func TestSaveOmitsEmptyLastCompanion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, &State{SessionID: "no-verdict"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "last_companion") {
		t.Errorf("omitempty last_companion not respected: %s", string(data))
	}
}

func TestSaveAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Save(path, &State{SessionID: "clean"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "state.json" {
			continue
		}
		t.Errorf("unexpected leftover file in dir: %q", e.Name())
	}
}

func TestUpdateStartsFromZeroWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	err := Update(path, func(s *State) error {
		if s.BufferCount != 0 || s.SessionID != "" {
			return fmt.Errorf("expected zero state, got %+v", s)
		}
		s.SessionID = "fresh"
		s.BufferCount = 1
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SessionID != "fresh" || got.BufferCount != 1 {
		t.Errorf("persisted state = %+v, want SessionID=fresh BufferCount=1", got)
	}
}

func TestUpdatePropagatesFnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	sentinel := errors.New("boom")
	err := Update(path, func(s *State) error {
		s.BufferCount = 99
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update err = %v, want %v", err, sentinel)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no state file after fn error, stat err = %v", err)
	}
}

func TestUpdateRejectsNilFn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Update(path, nil); err == nil {
		t.Fatalf("Update(nil) should error")
	}
}

func TestUpdateTwentyConcurrentIncrements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Seed the file so all goroutines race on the same starting point.
	if err := Save(path, &State{SessionID: "race", BufferCount: 0}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := Update(path, func(s *State) error {
				s.BufferCount++
				return nil
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("Update failed: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.BufferCount != goroutines {
		t.Fatalf("BufferCount = %d, want %d (flock is not actually serialising)", got.BufferCount, goroutines)
	}
	if got.SessionID != "race" {
		t.Errorf("unrelated field corrupted: SessionID = %q", got.SessionID)
	}
}

func TestUpdatePreservesUntouchedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	seed := &State{
		SessionID: "persist",
		BufferSeq: 10,
		LastCompanion: &LastCompanion{
			TS:         "2026-04-22T22:14:00Z",
			Status:     "on_track",
			Confidence: 0.92,
		},
	}
	if err := Save(path, seed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	err := Update(path, func(s *State) error {
		s.BufferCount = 7
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.BufferCount != 7 {
		t.Errorf("BufferCount = %d, want 7", got.BufferCount)
	}
	if got.BufferSeq != 10 {
		t.Errorf("BufferSeq clobbered: got %d", got.BufferSeq)
	}
	if got.LastCompanion == nil || got.LastCompanion.Status != "on_track" {
		t.Errorf("LastCompanion clobbered: %+v", got.LastCompanion)
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load on corrupt file should error")
	}
	// The wrapping error should still unwrap to a JSON error.
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("expected wrapped *json.SyntaxError, got %T: %v", err, err)
	}
}
