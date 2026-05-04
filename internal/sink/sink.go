// Package sink emits devlog events to external consumers. The primary
// consumer is attest-code-ownership, which wraps these events in signed
// in-toto attestations for regulatory audit.
//
// Sinks receive the raw, untruncated hook payload — not the lossy buffer
// entry that devlog stores internally. This lets consumers like attest
// compute content hashes and extract full file paths without information
// loss from devlog's summarization.
//
// Sink emission is best-effort: failures are logged but never block the
// capture hook or degrade the working agent's experience. The <200ms
// budget for PostToolUse hooks is the hard constraint.
package sink

import (
	"encoding/json"
)

// EventType identifies the kind of devlog event being emitted.
type EventType string

const (
	// EventCapture: a PostToolUse hook fired on Edit, Write, or Bash.
	// RawInput contains the full hook payload with untruncated tool_input.
	EventCapture EventType = "capture"

	// EventTask: a UserPromptSubmit hook fired. RawInput contains the
	// user's prompt — this is the "reason" / intent behind subsequent
	// code changes.
	EventTask EventType = "task"

	// EventTaskTool: a PostToolUse hook fired on TaskCreate or TaskUpdate.
	// RawInput contains the task breakdown details.
	EventTaskTool EventType = "task_tool"

	// EventLog: the Haiku summarizer produced a compressed narrative entry.
	// RawInput contains the log entry JSON.
	EventLog EventType = "log"

	// EventCompanion: the Sonnet companion produced a trajectory assessment.
	// RawInput contains the assessment JSON.
	EventCompanion EventType = "companion"
)

// Event is the envelope emitted to sinks. It carries enough metadata for
// consumers to correlate events without parsing RawInput, plus the full
// unmodified hook payload for consumers that need lossless data.
type Event struct {
	Type      EventType       `json:"type"`
	Seq       int             `json:"seq"`
	Timestamp string          `json:"ts"`
	SessionID string          `json:"session_id"`
	Host      string          `json:"host"`
	HookKind  string          `json:"hook_kind"`
	ToolName  string          `json:"tool_name,omitempty"`
	RawInput  json.RawMessage `json:"raw_input"`
}

// Sink receives devlog events for external consumption.
type Sink interface {
	// Emit sends an event to the sink. Implementations MUST return
	// within 50ms under normal conditions — the capture hook's total
	// budget is 200ms and buffer.Append takes the lion's share.
	//
	// Errors are informational: callers log them but never retry or
	// block on failure.
	Emit(event Event) error

	// Close releases resources held by the sink (open files, socket
	// connections). Safe to call multiple times.
	Close() error
}

// SinkConfig is the JSON-serialisable configuration for a single sink,
// as it appears in .devlog/config.json under the "sinks" key.
type SinkConfig struct {
	Type string `json:"type"` // "unix_socket" or "jsonl"
	Path string `json:"path"` // socket path or file path
}
