package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Response is the parsed envelope emitted by `claude -p --output-format json`.
//
// Only fields consumed by devlog are modeled. Claude Code may add new
// fields over time; json.Unmarshal tolerates those silently.
type Response struct {
	// Type is the envelope discriminator (typically "result" on success).
	Type string `json:"type"`
	// Subtype narrows the type further (e.g. "success", "error_max_turns").
	Subtype string `json:"subtype"`
	// Result is the model's generated text. For a structured-output
	// prompt this is itself a JSON string that the caller parses again.
	Result string `json:"result"`
	// SessionID is claude's own session identifier (unrelated to devlog's).
	SessionID string `json:"session_id"`
	// Model is the resolved model name ("claude-haiku-4-5-20251001" etc.).
	Model string `json:"model"`
	// DurationMS is total wall-clock time reported by claude.
	DurationMS int `json:"duration_ms"`
	// DurationAPIMS is the time spent inside the API call.
	DurationAPIMS int `json:"duration_api_ms"`
	// NumTurns is the number of agent turns taken (devlog pins this to 1).
	NumTurns int `json:"num_turns"`
	// IsError indicates claude itself reported an error (distinct from a
	// non-zero exit code).
	IsError bool `json:"is_error"`
	// TotalCostUSD is the cost of this invocation if reported.
	TotalCostUSD float64 `json:"total_cost_usd"`
	// Raw is the exact stdout bytes from claude. Retained so error
	// messages can include the full payload without a second exec.
	Raw []byte `json:"-"`
}

// ParseResponse parses the JSON output emitted by `claude -p
// --output-format json`. The output may be either a single result object
// or a JSON array of streaming events (init, assistant, rate_limit,
// result). In array form, the last element with type "result" is used.
// Leading/trailing whitespace is tolerated; any other malformed input
// returns ErrInvalidJSON.
//
// An envelope with IsError=true is still returned to the caller — the
// caller decides whether to surface that as a hard failure or let the
// empty-result check in Run catch it.
func ParseResponse(data []byte) (*Response, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: stdout was empty", ErrInvalidJSON)
	}

	trimmed := bytes.TrimSpace(data)

	if len(trimmed) > 0 && trimmed[0] == '[' {
		return parseArrayResponse(data, trimmed)
	}

	var resp Response
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	resp.Raw = append([]byte(nil), data...)
	return &resp, nil
}

func parseArrayResponse(raw, trimmed []byte) (*Response, error) {
	var events []json.RawMessage
	if err := json.Unmarshal(trimmed, &events); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	for i := len(events) - 1; i >= 0; i-- {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(events[i], &probe) == nil && probe.Type == "result" {
			var resp Response
			if err := json.Unmarshal(events[i], &resp); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
			}
			resp.Raw = append([]byte(nil), raw...)
			return &resp, nil
		}
	}

	return nil, fmt.Errorf("%w: no result element in array", ErrInvalidJSON)
}
