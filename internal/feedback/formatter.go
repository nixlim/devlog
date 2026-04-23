package feedback

import (
	"fmt"
	"strings"
	"unicode"
)

// banner is the visual separator surrounding the injected feedback block.
// SPEC §5 shows 53 U+2501 characters ("━") on each side; matching it keeps
// the rendering consistent across terminals of any width.
const banner = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

// Format renders r as the human-readable banner shown to the working agent.
// Output structure per SPEC §5:
//
//	━━━…━━━
//	[DevLog Companion — Trajectory Assessment]
//
//	STATUS: <UPPER> (confidence: NN%)
//
//	PATTERN DETECTED: <Humanized>
//	<summary paragraph>
//
//	EVIDENCE:
//	  - <item>
//	  …
//
//	REFRAME: <reframe>
//
//	ACTION: <intervention>
//	━━━…━━━
//
// Empty fields are gracefully omitted (e.g. an on_track result with no
// pattern/evidence still produces a valid, if sparse, banner).
func Format(r CompanionResult) string {
	var b strings.Builder
	b.WriteString(banner)
	b.WriteString("\n[DevLog Companion — Trajectory Assessment]\n\n")

	fmt.Fprintf(&b, "STATUS: %s (confidence: %d%%)\n\n",
		strings.ToUpper(strings.ReplaceAll(r.Status, "_", " ")),
		clampPercent(r.Confidence))

	if r.Pattern != "" {
		fmt.Fprintf(&b, "PATTERN DETECTED: %s\n", humanize(r.Pattern))
	}
	if r.Summary != "" {
		b.WriteString(r.Summary)
		b.WriteString("\n\n")
	}

	if len(r.Evidence) > 0 {
		b.WriteString("EVIDENCE:\n")
		for _, ev := range r.Evidence {
			fmt.Fprintf(&b, "  - %s\n", ev)
		}
		b.WriteByte('\n')
	}

	if r.Reframe != "" {
		fmt.Fprintf(&b, "REFRAME: %s\n\n", r.Reframe)
	}

	if r.Intervention != "" {
		fmt.Fprintf(&b, "ACTION: %s\n", r.Intervention)
	}

	b.WriteString(banner)
	b.WriteByte('\n')
	return b.String()
}

// humanize converts a snake_case identifier like "repetition_lock" into a
// Title Case phrase like "Repetition Lock" for display. Empty segments
// (leading, trailing, or double underscores) are dropped.
func humanize(s string) string {
	parts := strings.Split(s, "_")
	out := parts[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		rs := []rune(p)
		rs[0] = unicode.ToUpper(rs[0])
		out = append(out, string(rs))
	}
	return strings.Join(out, " ")
}

// clampPercent turns a 0..1 confidence into a 0..100 integer, rounding to
// nearest. Values outside [0, 1] are clamped — a defensive measure against
// a misbehaving companion model.
func clampPercent(c float64) int {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 100
	}
	return int(c*100 + 0.5)
}
