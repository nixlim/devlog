// Package feedback handles the companion→agent injection channel.
//
// The Sonnet companion emits a CompanionResult (JSON). Format renders it as
// the human-readable banner injected into the working agent's context by the
// PreToolUse hook. Write atomically places the banner in feedback.md; Read
// and Truncate let the hook consume it (archiving the old content).
package feedback

// CompanionResult mirrors the JSON output produced by the Sonnet
// anti-pattern companion. See SPEC §4 "Output format".
//
// Fields are populated by the companion model, not by devlog code. When the
// companion decides no intervention is needed (status="on_track"), the
// Pattern/Evidence/Intervention/Reframe fields may be empty.
type CompanionResult struct {
	Status       string   `json:"status"`
	Confidence   float64  `json:"confidence"`
	Pattern      string   `json:"pattern,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Intervention string   `json:"intervention,omitempty"`
	Reframe      string   `json:"reframe,omitempty"`
}

// Status constants matching the values the companion is instructed to emit.
const (
	StatusOnTrack   = "on_track"
	StatusDrifting  = "drifting"
	StatusSpiraling = "spiraling"
)

// NeedsIntervention reports whether a status warrants writing feedback.md.
// Per SPEC §4 "After assessment", on_track results never produce an
// intervention — only DRIFTING and SPIRALING do.
func (r CompanionResult) NeedsIntervention() bool {
	return r.Status == StatusDrifting || r.Status == StatusSpiraling
}
