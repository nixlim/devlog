// Package opencode bundles the OpenCode-side glue DevLog needs to ship.
// The TypeScript plugin shim is embedded into the binary so `devlog
// install` can drop it into the user's OpenCode plugin directory without
// requiring a separate download.
package opencode

import _ "embed"

// PluginSource is the canonical OpenCode plugin shim. It wires the four
// OpenCode hook points (tool.execute.before/after, chat.message, and
// the todo.updated event) to the corresponding `devlog` subcommands.
//
//go:embed devlog.ts
var PluginSource []byte
