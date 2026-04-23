// Package buffer manages the .devlog/buffer.jsonl capture file.
//
// The capture hook (PostToolUse) appends one Entry per tool call; the
// summarizer (devlog flush) drains that buffer into buffer_archive.jsonl
// and asks Haiku to describe the moved batch as a single dev-log line.
package buffer

// Entry is one captured diff event. It mirrors the JSON schema in
// SPEC.md (Buffer entry format). Field order of the struct matches the
// spec example so the generated JSON stays close to the documented
// layout for humans reading the file directly.
type Entry struct {
	Seq       int    `json:"seq"`
	TS        string `json:"ts"`
	SessionID string `json:"session_id"`
	Tool      string `json:"tool"`
	File      string `json:"file"`
	Detail    string `json:"detail"`
	DiffLines int    `json:"diff_lines"`
	Changed   bool   `json:"changed"`
}
