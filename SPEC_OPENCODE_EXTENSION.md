# Design proposal — multi-host devlog (Claude Code + OpenCode)

## Feasibility: yes

OpenCode is a good fit. It has every primitive devlog depends on, just under different names:

| devlog need | Claude Code | OpenCode |
|---|---|---|
| Pre-tool hook | `PreToolUse` | `tool.execute.before` |
| Post-tool hook | `PostToolUse` | `tool.execute.after` |
| User-prompt hook | `UserPromptSubmit` | `chat.message` |
| Todo hook | `PostToolUse(TaskCreate\|TaskUpdate)` | `todo.updated` event |
| Headless LLM call | `claude -p … --output-format json` | `opencode run … --format json` |
| Model config | bare id (`claude-haiku-4-5-…`) | `provider/id` (`anthropic/claude-haiku-4-5-…`) |
| Settings file | `~/.claude/settings.json` | `~/.config/opencode/opencode.json` + `opencode.json` |

One architectural twist: OpenCode hooks are **TypeScript plugins that run in-process**, not shell commands. That's the main integration seam that needs real work.

## Architecture: introduce a `Host` abstraction

Today, two modules are hardwired to Claude Code:

- `internal/claude/runner.go:95-156` — builds `claude -p` argv, parses Claude's JSON envelope.
- `cmd/install.go:27-38, 50-99` — hardcoded hook schema and `~/.claude/settings.json` resolution.

Refactor plan:

```
internal/host/
  host.go          // interface: Detect, Install, Uninstall, RunLLM
  claude/host.go   // current runner.go + install.go moved here
  opencode/host.go // new
  registry.go      // name → constructor
```

Interface (rough shape):

```go
type Host interface {
    Name() string                      // "claude" | "opencode"
    Detect() (bool, string, error)     // present? + version
    Install(opts InstallOpts) error    // wire hooks in host's settings
    Uninstall() error
    RunLLM(ctx, model, prompt, timeout) (*Response, error)
    NormalizeModel(s string) string    // bare id → provider/id for OpenCode
}
```

`internal/claude` stays as-is but is consumed through the interface. The `claude.Runner`, `Response`, sentinel errors survive — they become implementation details of `host/claude`.

## OpenCode host: a tiny TypeScript shim

OpenCode plugins can shell out via the `$` helper. That means we don't rewrite hook logic in TypeScript — we ship a ~40-line shim that forwards events to the existing `devlog` Go binary.

```ts
// .opencode/plugins/devlog.ts (embedded in Go binary, written on install)
export const DevLog: Plugin = async ({ $ }) => ({
  "tool.execute.before": async (input) => {
    await $`devlog check-feedback`.stdin(JSON.stringify(input));
  },
  "tool.execute.after":  async (input, output) => {
    if (["edit","write","bash"].includes(input.tool)) {
      await $`devlog capture`.stdin(JSON.stringify({input, output}));
    }
  },
  "chat.message": async (input) => {
    await $`devlog task-capture`.stdin(JSON.stringify(input));
  },
  event: async ({ event }) => {
    if (event.type === "todo.updated") {
      await $`devlog task-tool-capture`.stdin(JSON.stringify(event));
    }
  },
});
```

`devlog install --host opencode` writes this file into `.opencode/plugins/devlog.ts` (project) or `~/.config/opencode/plugins/devlog.ts` (global) and adds the plugin reference to `opencode.json`. The hook-payload JSON shape differs from Claude Code's, so each `devlog` subcommand that parses stdin (`capture`, `task-capture`, `check-feedback`) gains a host-aware parser — shared type in `internal/hookinput/` dispatching on a `Host` field set at install time.

## Auto-detect + CLI surface

`devlog install` (no args) runs `Detect()` on every registered host, then:
- Exactly one found → install there, print which.
- Both found → pick Claude (backwards compatible) and log a hint about `--host opencode`.
- Neither found → error with install links for both.

New flags on `install`:

```
devlog install \
  [--host claude|opencode]            # override detection
  [--summarizer-model ID]             # default claude-haiku-4-5-20251001
  [--companion-model ID]              # default claude-sonnet-4-6
  [--claude-command PATH]             # existing, rename to --host-command
```

These land in `.devlog/config.json` so re-install is idempotent. Treat model strings as **opaque** — devlog never parses them; the host normalizes at invoke time (`NormalizeModel`). For OpenCode, if the user passes `claude-haiku-4-5-20251001`, `NormalizeModel` prepends `anthropic/`; if they pass something already containing `/`, it's passed through.

## Config changes (`.devlog/config.json`)

```jsonc
{
  "host": "claude",                             // NEW
  "host_command": "claude",                     // was "claude_command"
  "summarizer_model": "claude-haiku-4-5-20251001",
  "companion_model":  "claude-sonnet-4-6",
  // ... existing fields unchanged
}
```

Migration: if `claude_command` is present and `host_command` is absent, copy it over and stamp `host: "claude"`.

## Risks / open questions

1. **OpenCode hook payload shape is under-documented.** Event payloads are reverse-engineered from TS types, not a schema. We should pin an OpenCode minor version and capture real payloads before committing to the shim's JSON contract.
2. **Tool-name matchers differ.** Claude Code matches on regex against tool names (`Edit|Write|Bash`); OpenCode has lowercase names (`edit`, `write`, `bash`) and no matcher config — we filter inside the plugin. A config mismatch (user customizes matcher in Claude, doesn't realize OpenCode filter is hard-coded in the shim) is a paper cut.
3. **Plugin trust model.** OpenCode plugins run in-process with full Node access. Security-conscious users may balk; the MCP route is an escape hatch if we need one later, but MCP can't intercept tool calls pre-execution the way plugins can.
4. **Todo parity.** OpenCode's `todo.updated` fires for its built-in todo tool; if the user is using a third-party todo MCP, we won't see those events. Same caveat already exists on Claude Code.
5. **Auth.** devlog relies on the host CLI's own session — no API key handling. Works symmetrically for both.

## Suggested phasing

1. **Refactor only, no behavior change.** Extract `internal/host/` interface; move existing code into `host/claude`; `install.go` becomes a thin wrapper that selects `host/claude` unconditionally. Prove tests still pass.
2. **Config surface.** Add `host` field; migration; rename `claude_command` → `host_command` with backwards-compat read.
3. **OpenCode host.** Implement `host/opencode`, including the embedded TS shim (use `//go:embed`), `opencode run` runner, and `NormalizeModel`.
4. **Auto-detect + flags.** Update `devlog install` dispatcher and new flags.
5. **Docs + CLI_REFERENCE update.**


## References

- OpenCode docs: https://opencode.ai/docs/
- OpenCode plugins: https://opencode.ai/docs/plugins/
- OpenCode commands: https://opencode.ai/docs/commands/
- OpenCode agents: https://opencode.ai/docs/agents/
- OpenCode config: https://opencode.ai/docs/config/
- OpenCode CLI: https://opencode.ai/docs/cli/
- GitHub: https://github.com/sst/opencode (redirects to anomalyco/opencode)
