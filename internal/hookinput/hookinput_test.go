package hookinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseClaudePreToolUse(t *testing.T) {
	raw := loadFixture(t, "claude_pretool.json")
	ev, err := Parse("claude", "pretool", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.SessionID != "sess-abc123" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit", ev.ToolName)
	}
	if ev.ToolInput.FilePath != "src/api/handler.go" {
		t.Errorf("FilePath = %q", ev.ToolInput.FilePath)
	}
	if ev.ToolInput.OldString != "Timeout: 30" {
		t.Errorf("OldString = %q", ev.ToolInput.OldString)
	}
	if ev.ToolInput.NewString != "Timeout: 60" {
		t.Errorf("NewString = %q", ev.ToolInput.NewString)
	}
	if len(ev.RawToolInput) == 0 {
		t.Error("RawToolInput should be populated")
	}
}

func TestParseOpenCodeToolBefore(t *testing.T) {
	raw := loadFixture(t, "opencode_tool_before.json")
	ev, err := Parse("opencode", "pretool", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.SessionID != "oc-sess-456" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit (normalised from edit)", ev.ToolName)
	}
	if ev.ToolInput.FilePath != "src/api/handler.go" {
		t.Errorf("FilePath = %q", ev.ToolInput.FilePath)
	}
	if ev.ToolInput.NewString != "Timeout: 60" {
		t.Errorf("NewString = %q", ev.ToolInput.NewString)
	}
}

func TestParseOpenCodeChatMessage(t *testing.T) {
	raw := loadFixture(t, "opencode_chat_message.json")
	ev, err := Parse("opencode", "prompt", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.SessionID != "oc-sess-789" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if !strings.Contains(ev.Prompt, "refactor the database client") {
		t.Errorf("Prompt = %q", ev.Prompt)
	}
	if ev.ToolName != "" {
		t.Errorf("ToolName should be empty for chat.message, got %q", ev.ToolName)
	}
}

func TestParseUnknownHost(t *testing.T) {
	_, err := Parse("vscode", "pretool", []byte("{}"))
	if err == nil {
		t.Fatal("expected error for unknown host")
	}
	if !strings.Contains(err.Error(), "unknown host") {
		t.Errorf("error = %q, want it to contain 'unknown host'", err.Error())
	}
}

func TestParseOpenCodeToolNameNormalization(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"tool":"edit","input":{},"sessionId":"s"}`, "Edit"},
		{`{"tool":"write","input":{},"sessionId":"s"}`, "Write"},
		{`{"tool":"bash","input":{},"sessionId":"s"}`, "Bash"},
		{`{"tool":"read","input":{},"sessionId":"s"}`, "Read"},
	}
	for _, tc := range cases {
		ev, err := Parse("opencode", "pretool", []byte(tc.raw))
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.raw, err)
			continue
		}
		if ev.ToolName != tc.want {
			t.Errorf("ToolName for %q = %q, want %q", tc.raw, ev.ToolName, tc.want)
		}
	}
}

func TestParseClaudeUserPromptSubmit(t *testing.T) {
	raw := []byte(`{"session_id":"sess-x","cwd":"/proj","prompt":"refactor auth"}`)
	ev, err := Parse("claude", "prompt", raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.Prompt != "refactor auth" {
		t.Errorf("Prompt = %q", ev.Prompt)
	}
	if ev.SessionID != "sess-x" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
}
