package opencode

import (
	"strings"
	"testing"
)

func TestPluginSource(t *testing.T) {
	if len(PluginSource) == 0 {
		t.Fatal("PluginSource is empty")
	}
	s := string(PluginSource)
	for _, want := range []string{"tool.execute.before", "tool.execute.after", "chat.message", "todo.updated"} {
		if !strings.Contains(s, want) {
			t.Errorf("PluginSource missing %q", want)
		}
	}
	if !strings.Contains(s, `"edit"`) || !strings.Contains(s, `"write"`) || !strings.Contains(s, `"bash"`) {
		t.Error("PluginSource should filter tool.execute.after on edit/write/bash")
	}
	if !strings.Contains(s, "input.args") {
		t.Error("PluginSource should forward OpenCode tool args to devlog capture")
	}
	if !strings.Contains(s, "!p.synthetic") {
		t.Error("PluginSource should exclude synthetic chat parts from task capture")
	}
	if !strings.Contains(s, "throw new Error(feedback)") {
		t.Error("PluginSource should block the tool call when check-feedback emits feedback")
	}
}
