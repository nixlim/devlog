// Package hookinput parses host-specific hook payloads into a single
// host-agnostic Event. Claude Code and OpenCode emit JSON shapes with
// overlapping intent but different field names and tool name casing;
// callers (capture, task-capture, check-feedback) consume Event so they
// don't have to branch per host.
package hookinput

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Event is the normalised hook payload. Fields not present in the source
// payload remain zero — callers decide which fields they care about for
// the kind of hook they implement.
type Event struct {
	ToolName       string
	ToolInput      ToolInput
	SessionID      string
	Prompt         string
	Cwd            string
	TranscriptPath string
	RawToolInput   json.RawMessage
}

// ToolInput is the typed view of the tool_input object. Fields here cover
// the tools devlog reacts to (Edit, Write, Bash). Unknown tools decode
// cleanly with every field empty.
type ToolInput struct {
	FilePath  string `json:"file_path,omitempty"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
	Content   string `json:"content,omitempty"`
	Command   string `json:"command,omitempty"`
}

// Parse decodes raw against the schema for the named host. The kind
// argument names the hook variant (e.g. "pretool", "posttool", "prompt")
// so OpenCode's polymorphic event payload can be disambiguated. Returns
// an error for an unknown host.
func Parse(host, kind string, raw []byte) (*Event, error) {
	switch host {
	case "claude":
		return parseClaude(kind, raw)
	case "opencode":
		return parseOpenCode(kind, raw)
	default:
		return nil, fmt.Errorf("unknown host %q", host)
	}
}

// parseClaude decodes the Claude Code hook envelope. All Claude hook
// kinds share the same outer shape; tool_input is only present on
// Pre/PostToolUse and prompt only on UserPromptSubmit.
func parseClaude(_ string, raw []byte) (*Event, error) {
	var payload struct {
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcript_path"`
		Cwd            string          `json:"cwd"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		Prompt         string          `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse claude hook input: %w", err)
	}
	ev := &Event{
		SessionID:      payload.SessionID,
		TranscriptPath: payload.TranscriptPath,
		Cwd:            payload.Cwd,
		ToolName:       payload.ToolName,
		Prompt:         payload.Prompt,
		RawToolInput:   payload.ToolInput,
	}
	if hasJSONObject(payload.ToolInput) {
		if err := json.Unmarshal(payload.ToolInput, &ev.ToolInput); err != nil {
			return nil, fmt.Errorf("parse claude tool_input: %w", err)
		}
	}
	return ev, nil
}

// parseOpenCode decodes the OpenCode plugin event payloads. OpenCode's
// shape varies per event kind, and tool names arrive lowercase ("edit")
// vs Claude's capitalised ("Edit"). We normalise the casing here so
// downstream code can match against a single set of names.
func parseOpenCode(kind string, raw []byte) (*Event, error) {
	cwd := extractOpenCodeCwd(raw)
	switch kind {
	case "prompt", "chat.message", "UserPromptSubmit":
		var payload struct {
			Content   string `json:"content"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("parse opencode chat.message: %w", err)
		}
		return &Event{SessionID: payload.SessionID, Prompt: payload.Content, Cwd: cwd}, nil

	case "todo.updated", "event":
		var envelope struct {
			Type string `json:"type"`
			Data struct {
				Tool      string          `json:"tool"`
				ToolInput json.RawMessage `json:"tool_input"`
				SessionID string          `json:"sessionId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("parse opencode event: %w", err)
		}
		ev := &Event{
			SessionID:    envelope.Data.SessionID,
			ToolName:     normaliseOpenCodeTool(envelope.Data.Tool),
			Cwd:          cwd,
			RawToolInput: envelope.Data.ToolInput,
		}
		if hasJSONObject(envelope.Data.ToolInput) {
			if err := json.Unmarshal(envelope.Data.ToolInput, &ev.ToolInput); err != nil {
				return nil, fmt.Errorf("parse opencode event tool_input: %w", err)
			}
		}
		return ev, nil

	default:
		// pretool / posttool / tool.execute.before / tool.execute.after
		var payload struct {
			Tool      string          `json:"tool"`
			Input     json.RawMessage `json:"input"`
			SessionID string          `json:"sessionId"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("parse opencode tool event: %w", err)
		}
		ev := &Event{
			SessionID:    payload.SessionID,
			ToolName:     normaliseOpenCodeTool(payload.Tool),
			Cwd:          cwd,
			RawToolInput: payload.Input,
		}
		if hasJSONObject(payload.Input) {
			if err := json.Unmarshal(payload.Input, &ev.ToolInput); err != nil {
				return nil, fmt.Errorf("parse opencode tool input: %w", err)
			}
		}
		return ev, nil
	}
}

// extractOpenCodeCwd reads a best-effort "cwd" field from an OpenCode
// payload. OpenCode's plugin shim is expected to include the project cwd
// at the envelope level so devlog can locate the .devlog/ directory;
// missing or malformed payloads yield "" so callers fall back to the
// process working directory.
func extractOpenCodeCwd(raw []byte) string {
	var probe struct {
		Cwd string `json:"cwd"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Cwd
}

// normaliseOpenCodeTool capitalises the first rune of an OpenCode tool
// name so "edit" becomes "Edit", matching Claude Code's convention. An
// empty input stays empty.
func normaliseOpenCodeTool(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// hasJSONObject reports whether raw is a non-empty, non-null JSON
// fragment. We only attempt to decode tool input when there's something
// worth decoding — `null` and absent fields both round-trip to the zero
// ToolInput.
func hasJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	return true
}
