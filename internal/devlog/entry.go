// Package devlog provides the on-disk storage layer for log.jsonl — the
// compressed narrative emitted by the Haiku summarizer on every flush.
//
// Each flush appends exactly one LogEntry. Reads are append-aware: the
// companion pulls the last N entries to assess trajectory. The file is
// line-oriented JSONL so partial writes at the tail never corrupt earlier
// entries.
package devlog

import "time"

// Entry mirrors the SPEC-defined log.jsonl row shape:
//
//	{
//	  "seq": 7,
//	  "ts": "2026-04-22T22:15:30Z",
//	  "session_id": "abc123",
//	  "covers_seqs": [33, 42],
//	  "summary": "...",
//	  "model": "claude-haiku-4-5-20251001",
//	  "duration_ms": 1200
//	}
//
// CoversSeqs is the inclusive list of buffer.jsonl seq values that fed
// this summary. It is surfaced to the companion so interventions can cite
// specific edits.
type Entry struct {
	Seq        int       `json:"seq"`
	TS         time.Time `json:"ts"`
	SessionID  string    `json:"session_id"`
	CoversSeqs []int     `json:"covers_seqs"`
	Summary    string    `json:"summary"`
	Model      string    `json:"model"`
	DurationMS int       `json:"duration_ms"`
}
