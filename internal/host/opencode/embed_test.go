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
}
