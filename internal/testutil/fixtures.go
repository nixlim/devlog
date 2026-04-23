// Package testutil provides shared fixtures and helpers for devlog tests.
//
// Concrete domain types live in internal/buffer and internal/devlog. The
// sample structs defined here mirror the SPEC-defined JSON shape so other
// packages can consume the fixtures without an import cycle: tests write the
// sample data to a jsonl file and read it back through their own types.
package testutil

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// BufferEntry mirrors the SPEC buffer.jsonl schema.
type BufferEntry struct {
	Seq       int       `json:"seq"`
	TS        time.Time `json:"ts"`
	SessionID string    `json:"session_id"`
	Tool      string    `json:"tool"`
	File      string    `json:"file"`
	Detail    string    `json:"detail"`
	DiffLines int       `json:"diff_lines"`
	Changed   bool      `json:"changed"`
}

// LogCompanion mirrors the SPEC last_companion struct in state.json.
type LogCompanion struct {
	TS         time.Time `json:"ts"`
	Status     string    `json:"status"`
	Confidence float64   `json:"confidence"`
}

// LogEntry mirrors the SPEC log.jsonl schema.
type LogEntry struct {
	Seq        int       `json:"seq"`
	TS         time.Time `json:"ts"`
	SessionID  string    `json:"session_id"`
	CoversSeqs []int     `json:"covers_seqs"`
	Summary    string    `json:"summary"`
	Model      string    `json:"model"`
	DurationMS int       `json:"duration_ms"`
}

// NewTempDevlogDir creates a temporary project root containing a .devlog/
// subdirectory and a real initialized .git/ directory. The directory is
// cleaned up automatically by t.TempDir(). Returns the project root.
//
// Fails the test immediately if git is not available or initialization
// fails — tests that need a non-git directory should use t.TempDir directly.
func NewTempDevlogDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("testutil: mkdir .devlog: %v", err)
	}
	cmd := exec.Command("git", "init", "--quiet", root)
	// Avoid picking up any user git template directory that might slow this down.
	cmd.Env = append(os.Environ(), "GIT_TEMPLATE_DIR=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testutil: git init: %v: %s", err, out)
	}
	// Configure identity so git commit works in tests without relying on host config.
	for _, args := range [][]string{
		{"-C", root, "config", "user.email", "test@devlog.local"},
		{"-C", root, "config", "user.name", "DevLog Test"},
		{"-C", root, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("testutil: git %v: %v: %s", args, err, out)
		}
	}
	return root
}

// SampleBufferEntries returns a small, deterministic slice of buffer entries
// spanning Edit, Write, and Bash tool types with a mix of changed/unchanged.
func SampleBufferEntries() []BufferEntry {
	base := time.Date(2026, 4, 22, 22, 15, 0, 0, time.UTC)
	return []BufferEntry{
		{
			Seq: 40, TS: base, SessionID: "sess-abc",
			Tool: "Edit", File: "src/api/handler.go",
			Detail:    "old: 'Timeout: 30 * time.Second' → new: 'Timeout: 60 * time.Second'",
			DiffLines: 4, Changed: true,
		},
		{
			Seq: 41, TS: base.Add(30 * time.Second), SessionID: "sess-abc",
			Tool: "Write", File: "src/api/handler_test.go",
			Detail:    "wrote 1820 bytes",
			DiffLines: 42, Changed: true,
		},
		{
			Seq: 42, TS: base.Add(90 * time.Second), SessionID: "sess-abc",
			Tool: "Bash", File: "",
			Detail:    "go test ./...",
			DiffLines: 0, Changed: false,
		},
	}
}

// SampleLogEntries returns a small, deterministic slice of dev-log entries
// suitable for feeding summarizer/companion prompt builders.
func SampleLogEntries() []LogEntry {
	base := time.Date(2026, 4, 22, 22, 15, 30, 0, time.UTC)
	return []LogEntry{
		{
			Seq: 5, TS: base, SessionID: "sess-abc",
			CoversSeqs: []int{20, 21, 22, 23, 24, 25, 26, 27, 28, 29},
			Summary:    "Increasing database connection pool from 10 to 25 in response to timeout errors.",
			Model:      "claude-haiku-4-5-20251001", DurationMS: 1150,
		},
		{
			Seq: 6, TS: base.Add(5 * time.Minute), SessionID: "sess-abc",
			CoversSeqs: []int{30, 31, 32, 33, 34, 35, 36, 37, 38, 39},
			Summary:    "Rewriting query with explicit index hints; error unchanged.",
			Model:      "claude-haiku-4-5-20251001", DurationMS: 1230,
		},
		{
			Seq: 7, TS: base.Add(10 * time.Minute), SessionID: "sess-abc",
			CoversSeqs: []int{40, 41, 42},
			Summary:    "Third consecutive attempt targeting the database layer — 500 on /api/recommendations persists.",
			Model:      "claude-haiku-4-5-20251001", DurationMS: 1200,
		},
	}
}

// WriteJSONL marshals entries as one JSON object per line and writes the
// result to path. Fails the test on any error.
func WriteJSONL[T any](t *testing.T, path string, entries []T) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			t.Fatalf("testutil: encode entry %d: %v", i, err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("testutil: write %s: %v", path, err)
	}
}
