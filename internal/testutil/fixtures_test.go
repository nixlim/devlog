package testutil

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTempDevlogDir(t *testing.T) {
	root := NewTempDevlogDir(t)

	info, err := os.Stat(filepath.Join(root, ".devlog"))
	if err != nil || !info.IsDir() {
		t.Fatalf(".devlog not created: %v", err)
	}
	info, err = os.Stat(filepath.Join(root, ".git"))
	if err != nil || !info.IsDir() {
		t.Fatalf(".git not created: %v", err)
	}

	// git must be functional — not just a mkdir.
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("expected inside-work-tree=true, got %q", out)
	}

	// Identity should be configured so `git commit` works without host config.
	cmd = exec.Command("git", "-C", root, "config", "user.email")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("git user.email not configured: %v: %s", err, out)
	}
}

func TestSampleBufferEntries(t *testing.T) {
	entries := SampleBufferEntries()
	if len(entries) < 3 {
		t.Fatalf("want >=3 entries, got %d", len(entries))
	}
	// Must cover all three captured tool kinds per SPEC §Diff Capture.
	tools := map[string]bool{}
	for _, e := range entries {
		tools[e.Tool] = true
	}
	for _, want := range []string{"Edit", "Write", "Bash"} {
		if !tools[want] {
			t.Errorf("SampleBufferEntries missing tool %q (have %v)", want, tools)
		}
	}
	// Sequence numbers must be monotonic.
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq <= entries[i-1].Seq {
			t.Errorf("entries not monotonically sequenced at %d: %d <= %d",
				i, entries[i].Seq, entries[i-1].Seq)
		}
	}
}

func TestSampleLogEntries(t *testing.T) {
	entries := SampleLogEntries()
	if len(entries) < 2 {
		t.Fatalf("want >=2 entries, got %d", len(entries))
	}
	for i, e := range entries {
		if e.Seq == 0 || e.Summary == "" || e.Model == "" {
			t.Errorf("entry %d missing required fields: %+v", i, e)
		}
		if len(e.CoversSeqs) == 0 {
			t.Errorf("entry %d has no covers_seqs", i)
		}
	}
}

func TestWriteJSONL_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.jsonl")

	want := SampleBufferEntries()
	WriteJSONL(t, path, want)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Every line should be a valid JSON object; count must match.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var got []BufferEntry
	for scanner.Scan() {
		var e BufferEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal line %q: %v", scanner.Text(), err)
		}
		got = append(got, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("round-trip length mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Tool != want[i].Tool {
			t.Errorf("entry %d mismatch: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
