package hook

import (
	"bytes"
	stderrors "errors"
	"io"
	"strings"
	"testing"

	derrors "devlog/internal/errors"
)

func parse(t *testing.T, body string) *Input {
	t.Helper()
	in, err := ParseInput(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseInput(%q) err = %v", body, err)
	}
	return in
}

func TestParseInputSessionOnlyPayload(t *testing.T) {
	body := `{
        "session_id": "abc123",
        "transcript_path": "/Users/x/.claude/projects/hash/abc123.jsonl",
        "cwd": "/path/to/project"
    }`
	in := parse(t, body)
	if in.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want abc123", in.SessionID)
	}
	if in.TranscriptPath != "/Users/x/.claude/projects/hash/abc123.jsonl" {
		t.Errorf("TranscriptPath = %q", in.TranscriptPath)
	}
	if in.Cwd != "/path/to/project" {
		t.Errorf("Cwd = %q", in.Cwd)
	}
	if in.ToolName != "" || in.Prompt != "" || len(in.RawToolInput) != 0 {
		t.Errorf("session-only payload should leave tool/prompt fields empty, got %+v", in)
	}
}

func TestParseInputPreToolUseEdit(t *testing.T) {
	body := `{
        "session_id": "abc123",
        "transcript_path": "/t.jsonl",
        "cwd": "/proj",
        "tool_name": "Edit",
        "tool_input": {
            "file_path": "src/api/handler.go",
            "old_string": "Timeout: 30 * time.Second",
            "new_string": "Timeout: 60 * time.Second"
        }
    }`
	in := parse(t, body)
	if in.ToolName != "Edit" {
		t.Errorf("ToolName = %q", in.ToolName)
	}
	if in.ToolInput.FilePath != "src/api/handler.go" {
		t.Errorf("FilePath = %q", in.ToolInput.FilePath)
	}
	if in.ToolInput.OldString != "Timeout: 30 * time.Second" {
		t.Errorf("OldString = %q", in.ToolInput.OldString)
	}
	if in.ToolInput.NewString != "Timeout: 60 * time.Second" {
		t.Errorf("NewString = %q", in.ToolInput.NewString)
	}
	if len(in.RawToolInput) == 0 {
		t.Errorf("RawToolInput should be populated for later Bash-like uses")
	}
	// The raw blob should round-trip to the same JSON object.
	if !bytes.Contains(in.RawToolInput, []byte("file_path")) {
		t.Errorf("RawToolInput missing key: %s", string(in.RawToolInput))
	}
}

func TestParseInputPostToolUseBashExposesCommand(t *testing.T) {
	body := `{
        "session_id": "abc123",
        "cwd": "/proj",
        "tool_name": "Bash",
        "tool_input": {"command": "go test ./..."}
    }`
	in := parse(t, body)
	if in.ToolName != "Bash" {
		t.Errorf("ToolName = %q", in.ToolName)
	}
	if in.ToolInput.Command != "go test ./..." {
		t.Errorf("Command = %q", in.ToolInput.Command)
	}
	// Bash has no file_path/old_string — those should stay empty.
	if in.ToolInput.FilePath != "" || in.ToolInput.OldString != "" {
		t.Errorf("non-Edit tool leaked fields: %+v", in.ToolInput)
	}
}

func TestParseInputPostToolUseWriteExposesContent(t *testing.T) {
	body := `{
        "session_id": "abc123",
        "cwd": "/proj",
        "tool_name": "Write",
        "tool_input": {"file_path": "notes.md", "content": "hello"}
    }`
	in := parse(t, body)
	if in.ToolInput.FilePath != "notes.md" {
		t.Errorf("FilePath = %q", in.ToolInput.FilePath)
	}
	if in.ToolInput.Content != "hello" {
		t.Errorf("Content = %q", in.ToolInput.Content)
	}
}

func TestParseInputUserPromptSubmit(t *testing.T) {
	body := `{
        "session_id": "abc123",
        "cwd": "/proj",
        "prompt": "Fix the 500 error on /api/recommendations"
    }`
	in := parse(t, body)
	if in.Prompt != "Fix the 500 error on /api/recommendations" {
		t.Errorf("Prompt = %q", in.Prompt)
	}
	if in.ToolName != "" {
		t.Errorf("UserPromptSubmit should have no tool_name, got %q", in.ToolName)
	}
}

func TestParseInputToolInputNullOrMissing(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"null tool_input", `{"session_id":"a","cwd":"/p","tool_name":"X","tool_input":null}`},
		{"missing tool_input", `{"session_id":"a","cwd":"/p","tool_name":"X"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := parse(t, tc.body)
			if in.ToolName != "X" {
				t.Errorf("ToolName = %q", in.ToolName)
			}
			if in.ToolInput.FilePath != "" {
				t.Errorf("expected empty ToolInput, got %+v", in.ToolInput)
			}
		})
	}
}

func TestParseInputMalformedJSONReturnsDevlogError(t *testing.T) {
	_, err := ParseInput(strings.NewReader("{not json"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var de *derrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *errors.DevlogError, got %T: %v", err, err)
	}
	if de.Component != "hook" {
		t.Errorf("Component = %q, want hook", de.Component)
	}
	if !strings.Contains(de.Remediation, "Claude Code version") {
		t.Errorf("remediation should mention Claude Code version, got: %q", de.Remediation)
	}
	if !strings.Contains(de.Remediation, "{not json") {
		t.Errorf("remediation should echo the offending input, got: %q", de.Remediation)
	}
}

func TestParseInputEmptyStdin(t *testing.T) {
	_, err := ParseInput(strings.NewReader(""))
	if err == nil {
		t.Fatalf("expected error on empty stdin")
	}
	var de *derrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *errors.DevlogError, got %T", err)
	}
	if de.Component != "hook" {
		t.Errorf("Component = %q, want hook", de.Component)
	}
	if !strings.Contains(strings.ToLower(de.Message), "empty") {
		t.Errorf("message should mention empty input, got %q", de.Message)
	}
}

type erroringReader struct{}

func (erroringReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestParseInputStdinReadError(t *testing.T) {
	_, err := ParseInput(erroringReader{})
	if err == nil {
		t.Fatalf("expected error from broken reader")
	}
	if !stderrors.Is(err, io.ErrClosedPipe) {
		t.Errorf("expected wrapped io.ErrClosedPipe, got %v", err)
	}
}

func TestParseInputToolInputWrongShape(t *testing.T) {
	// tool_input is expected to be an object. When it arrives as an
	// array, json.Unmarshal into ToolInput will type-mismatch — we
	// should surface this as a hook-level error.
	body := `{"session_id":"a","cwd":"/p","tool_name":"X","tool_input":[1,2,3]}`
	_, err := ParseInput(strings.NewReader(body))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var de *derrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected *errors.DevlogError, got %T: %v", err, err)
	}
	if de.Component != "hook" {
		t.Errorf("Component = %q, want hook", de.Component)
	}
}

func TestParseInputLongPayloadTruncatedInRemediation(t *testing.T) {
	long := strings.Repeat("X", 500)
	_, err := ParseInput(strings.NewReader(long))
	if err == nil {
		t.Fatalf("expected error on non-JSON payload")
	}
	var de *derrors.DevlogError
	if !stderrors.As(err, &de) {
		t.Fatalf("expected DevlogError, got %T", err)
	}
	if !strings.Contains(de.Remediation, "…") {
		t.Errorf("long payload should be truncated with ellipsis in remediation, got:\n%s", de.Remediation)
	}
}
