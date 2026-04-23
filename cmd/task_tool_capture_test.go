package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setupDevlogProject creates a fake project root with a .git and .devlog
// directory. The .git directory is what findDevlogDir latches onto, so
// tasks.jsonl lands inside the right .devlog/.
func setupDevlogProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".devlog"), 0o755); err != nil {
		t.Fatalf("mkdir .devlog: %v", err)
	}
	return root
}

func readTasksJSONL(t *testing.T, root string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".devlog", "tasks.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile tasks.jsonl: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestTaskToolCaptureAppendsTaskCreate(t *testing.T) {
	root := setupDevlogProject(t)

	payload := fmt.Sprintf(
		`{"session_id":"s","cwd":%q,"tool_name":"TaskCreate",`+
			`"tool_input":{"subject":"Ship the thing","description":"do X"}}`,
		root,
	)
	if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}

	rows := readTasksJSONL(t, root)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row["tool"] != "TaskCreate" {
		t.Errorf("tool = %v, want TaskCreate", row["tool"])
	}
	if ts, _ := row["ts"].(string); ts == "" {
		t.Errorf("ts missing in row: %+v", row)
	}
	ti, ok := row["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_input not object: %T %v", row["tool_input"], row["tool_input"])
	}
	if ti["subject"] != "Ship the thing" {
		t.Errorf("tool_input.subject = %v", ti["subject"])
	}
}

func TestTaskToolCaptureAppendsTaskUpdate(t *testing.T) {
	root := setupDevlogProject(t)

	payload := fmt.Sprintf(
		`{"session_id":"s","cwd":%q,"tool_name":"TaskUpdate",`+
			`"tool_input":{"taskId":"42","status":"in_progress"}}`,
		root,
	)
	if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}

	rows := readTasksJSONL(t, root)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["tool"] != "TaskUpdate" {
		t.Errorf("tool = %v", rows[0]["tool"])
	}
}

func TestTaskToolCaptureIgnoresNonTaskTool(t *testing.T) {
	root := setupDevlogProject(t)

	for _, tool := range []string{"Edit", "Write", "Bash", "Read", "Glob"} {
		t.Run(tool, func(t *testing.T) {
			payload := fmt.Sprintf(
				`{"session_id":"s","cwd":%q,"tool_name":%q,"tool_input":{"file_path":"x"}}`,
				root, tool,
			)
			if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
				t.Errorf("rc = %d, want 0", rc)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(root, ".devlog", "tasks.jsonl")); !os.IsNotExist(err) {
		t.Errorf("tasks.jsonl should not exist when only non-Task tools fired, stat err = %v", err)
	}
}

func TestTaskToolCaptureMultipleAppends(t *testing.T) {
	root := setupDevlogProject(t)

	for i := 0; i < 5; i++ {
		payload := fmt.Sprintf(
			`{"session_id":"s","cwd":%q,"tool_name":"TaskCreate","tool_input":{"i":%d}}`,
			root, i,
		)
		if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
			t.Fatalf("iter %d: rc = %d", i, rc)
		}
	}
	rows := readTasksJSONL(t, root)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
}

func TestTaskToolCaptureMalformedJSONExitsZeroWithoutPanic(t *testing.T) {
	root := setupDevlogProject(t)
	// chdir so writeHookErrorBestEffort's Getwd fallback has somewhere
	// meaningful to write the error log.
	prevWD, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	rc := taskToolCaptureImpl(strings.NewReader("{not json"))
	if rc != 0 {
		t.Errorf("rc = %d, want 0 (hook must never block agent)", rc)
	}
	if _, err := os.Stat(filepath.Join(root, ".devlog", "tasks.jsonl")); !os.IsNotExist(err) {
		t.Errorf("tasks.jsonl should not exist when input was malformed, stat err = %v", err)
	}
}

func TestTaskToolCaptureEmptyStdinExitsZero(t *testing.T) {
	root := setupDevlogProject(t)
	prevWD, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	if rc := taskToolCaptureImpl(strings.NewReader("")); rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
}

func TestTaskToolCaptureMissingToolInputStillSucceeds(t *testing.T) {
	root := setupDevlogProject(t)
	payload := fmt.Sprintf(
		`{"session_id":"s","cwd":%q,"tool_name":"TaskCreate"}`,
		root,
	)
	if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	rows := readTasksJSONL(t, root)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["tool_input"] != nil {
		t.Errorf("tool_input should be null when absent, got %T %v",
			rows[0]["tool_input"], rows[0]["tool_input"])
	}
}

func TestTaskToolCaptureConcurrentAppendsAreSerialised(t *testing.T) {
	root := setupDevlogProject(t)

	const goroutines = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprintf(
				`{"session_id":"s","cwd":%q,"tool_name":"TaskCreate","tool_input":{"i":%d}}`,
				root, i,
			)
			<-start
			if rc := taskToolCaptureImpl(strings.NewReader(payload)); rc != 0 {
				t.Errorf("rc = %d, want 0", rc)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	rows := readTasksJSONL(t, root)
	if len(rows) != goroutines {
		t.Fatalf("expected %d rows, got %d (flock not serialising?)", goroutines, len(rows))
	}
	// Each row must have a distinct i; no torn lines.
	seen := map[float64]bool{}
	for _, row := range rows {
		ti, ok := row["tool_input"].(map[string]any)
		if !ok {
			t.Errorf("row tool_input not object: %+v", row)
			continue
		}
		i, ok := ti["i"].(float64)
		if !ok {
			t.Errorf("row tool_input.i missing: %+v", row)
			continue
		}
		seen[i] = true
	}
	if len(seen) != goroutines {
		t.Errorf("expected %d distinct i values, got %d", goroutines, len(seen))
	}
}
