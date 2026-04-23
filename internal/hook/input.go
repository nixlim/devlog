// Package hook parses the JSON payloads Claude Code pipes to its hook
// commands on stdin.
//
// Three shapes are in play:
//
//  1. Every hook carries session_id, transcript_path, cwd.
//  2. PreToolUse / PostToolUse additionally carry tool_name and
//     tool_input (whose shape varies per tool — Edit has file_path +
//     old_string + new_string; Bash has command; Write has file_path
//     + content).
//  3. UserPromptSubmit additionally carries prompt.
//
// Rather than branching per hook kind, ParseInput decodes all three
// shapes into a single Input struct. Fields not present in the active
// payload simply remain zero — callers decide which fields they care
// about for the hook they implement.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	derrors "devlog/internal/errors"
)

// ToolInput is a best-effort typed view of the tool_input object. The
// fields here cover the tools devlog reacts to (Edit, Write, Bash) plus
// the task-related tools. Unknown tools decode cleanly with every
// field empty — callers that need tool-specific detail should consult
// Input.RawToolInput directly.
type ToolInput struct {
	FilePath  string `json:"file_path,omitempty"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
	Content   string `json:"content,omitempty"`
	Command   string `json:"command,omitempty"`
}

// Input is the parsed hook payload.
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInput      ToolInput       `json:"tool_input,omitempty"`
	RawToolInput   json.RawMessage `json:"-"`
	Prompt         string          `json:"prompt,omitempty"`
}

// ParseInput reads the JSON hook payload from r and decodes it. It
// always returns a non-nil *Input on success, even when optional fields
// are missing. Errors are always *errors.DevlogError with component
// "hook".
func ParseInput(r io.Reader) (*Input, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, derrors.Wrap("hook", "failed to read hook input from stdin", err).
			WithRemediation(malformedRemediation(data))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, derrors.New("hook", "empty hook input on stdin").
			WithRemediation(malformedRemediation(data))
	}

	var raw struct {
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcript_path"`
		Cwd            string          `json:"cwd"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		Prompt         string          `json:"prompt"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, derrors.Wrap("hook", "failed to parse hook input from stdin", err).
			WithRemediation(malformedRemediation(data))
	}

	in := &Input{
		SessionID:      raw.SessionID,
		TranscriptPath: raw.TranscriptPath,
		Cwd:            raw.Cwd,
		ToolName:       raw.ToolName,
		RawToolInput:   raw.ToolInput,
		Prompt:         raw.Prompt,
	}
	if hasToolInput(raw.ToolInput) {
		if err := json.Unmarshal(raw.ToolInput, &in.ToolInput); err != nil {
			return nil, derrors.Wrap("hook", "failed to parse tool_input payload", err).
				WithRemediation(malformedRemediation(data))
		}
	}
	return in, nil
}

// hasToolInput reports whether raw is a non-empty, non-null JSON
// fragment worth decoding into ToolInput. Returning false for `null`
// avoids a bogus type-mismatch error when Claude Code sends
// "tool_input": null for tools that have no input.
func hasToolInput(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	return true
}

// malformedRemediation returns the standard remediation text, with the
// observed payload truncated for the user's reference.
func malformedRemediation(data []byte) string {
	preview := string(data)
	const max = 100
	if len(preview) > max {
		preview = preview[:max] + "…"
	}
	return fmt.Sprintf(
		"Expected JSON with 'session_id', 'cwd', and (for tool hooks) "+
			"'tool_name' + 'tool_input', or (for UserPromptSubmit) 'prompt'.\n"+
			"Got: %s\n\n"+
			"This may indicate a Claude Code version incompatibility.\n"+
			"Check Claude Code version: claude --version\n"+
			"DevLog requires Claude Code >= 2.1.0",
		preview,
	)
}
