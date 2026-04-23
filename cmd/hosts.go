package cmd

// Blank-import the Claude host so its init() registers with internal/host.
// Every cmd function that calls host.Lookup relies on this. Additional
// backends (e.g. OpenCode) get added here too.
import (
	_ "devlog/internal/host/claude"
	_ "devlog/internal/host/opencode"
)
