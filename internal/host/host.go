// Package host defines the abstraction over LLM-driven "host" backends
// (Claude Code, OpenCode) that DevLog can target. Each host provides its
// own installation, detection, and LLM invocation glue; the host package
// itself is the small interface the rest of devlog sees.
package host

import (
	"context"
	"time"
)

// Response is the host-agnostic envelope returned by RunLLM. Fields mirror
// the superset of what Claude Code and OpenCode surface; host-specific
// implementations are responsible for mapping their native payloads onto
// this shape.
type Response struct {
	Type          string  `json:"type"`
	Subtype       string  `json:"subtype"`
	Result        string  `json:"result"`
	SessionID     string  `json:"session_id"`
	Model         string  `json:"model"`
	DurationMS    int     `json:"duration_ms"`
	DurationAPIMS int     `json:"duration_api_ms"`
	NumTurns      int     `json:"num_turns"`
	IsError       bool    `json:"is_error"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	Raw           []byte  `json:"-"`
}

// InstallOpts carries the knobs `devlog install` / `devlog uninstall` pass
// down to a host. Individual hosts ignore fields they don't need.
type InstallOpts struct {
	SettingsPath string
	PluginDir    string
	ConfigPath   string
	Global       bool
}

// Host is the minimal surface DevLog needs from a backend. Implementations
// live in internal/host/<name> and register themselves via init().
type Host interface {
	Name() string
	Detect() (bool, string, error)
	Install(opts InstallOpts) error
	Uninstall(opts InstallOpts) error
	RunLLM(ctx context.Context, model, prompt string, timeout time.Duration) (*Response, error)
	NormalizeModel(s string) string
}
