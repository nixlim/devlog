# DevLog CLI Reference

Death-spiral prevention system for Claude Code agents.

```
devlog <command> [args...]
devlog --help
```

## Commands

| Command | Summary |
|---------|---------|
| [init](#init) | Initialize `.devlog/`, verify git, set session ID |
| [capture](#capture) | Buffer a diff entry; trigger flush if threshold met |
| [task-capture](#task-capture) | Record user's prompt as task/update |
| [task-tool-capture](#task-tool-capture) | Record TaskCreate/TaskUpdate tool calls |
| [check-feedback](#check-feedback) | Output pending companion feedback or exit silently |
| [flush](#flush) | Run Haiku summarizer on buffered diffs |
| [companion](#companion) | Run Sonnet anti-pattern assessment |
| [status](#status) | Show current state: counters, last companion, health |
| [log](#log) | Print the dev log narrative |
| [reset](#reset) | Clear all state for a fresh session |
| [config](#config) | Get/set tunable parameters |
| [install](#install) | Install hooks into Claude Code settings.json |
| [uninstall](#uninstall) | Remove hooks from Claude Code settings.json |

---

## init

Initialize `.devlog/` directory, verify the project is a git repository, and generate a session ID.

```
devlog init [--force] [--project DIR]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--force` | bool | `false` | Regenerate session ID and reset counters even if state.json exists |
| `--project` | string | `.` | Project root directory |

**Behavior:**

- Validates the project is a git repository.
- Creates `.devlog/` directory.
- Generates a 16-character lowercase hex session ID (64 bits entropy).
- Writes `state.json` with the session ID and start timestamp.
- Writes `config.json` with defaults (only if the file doesn't exist).
- Running twice without `--force` is a safe no-op — the existing session ID is preserved.
- With `--force`: resets session entirely (new ID, cleared counters, updated timestamp).

**Exit codes:** 0 success, 1 error, 2 usage error.

**Files written:** `.devlog/state.json`, `.devlog/config.json`

---

## capture

Buffer a diff entry from a tool call. When the buffer hits the configured threshold, spawns a background flush.

```
devlog capture
```

Invoked by Claude Code's **PostToolUse** hook (matcher: `Edit|Write|Bash`). Reads hook input from stdin.

**Flags:** None.

**Behavior:**

- Must complete in <200ms. Never fails the working agent — errors are logged to `.devlog/errors.log` and the command exits 0.
- Reads the hook payload from stdin (JSON).
- Tool-specific handling:
  - **Edit:** Records `file_path` and a truncated old→new detail string (max 200 chars).
  - **Write:** Records `file_path` and content length.
  - **Bash:** Runs `git diff --stat HEAD`. If the tree changed, captures `git diff HEAD` (truncated to 2000 chars). If clean, records the command with `changed=false`.
- Assigns a sequence number under a file lock, appends to `buffer.jsonl`.
- When buffer count reaches `buffer_size` (default 10): sets `flush_in_progress` flag, resets buffer counter, spawns a detached `devlog flush` process.

**Exit codes:** Always 0.

**Files touched:** `.devlog/buffer.jsonl` (append), `.devlog/state.json` (locked update), `.devlog/errors.log` (on error).

---

## task-capture

Record the user's prompt as the original task or a course correction.

```
devlog task-capture
```

Invoked by Claude Code's **UserPromptSubmit** hook (matcher: empty — fires on every user message). Reads hook input from stdin.

**Flags:** None.

**Behavior:**

- First non-empty prompt per session: written atomically to `.devlog/task.md`.
- Subsequent prompts: appended to `.devlog/task_updates.jsonl` as `{ts, session_id, prompt}`.
- Creates `.devlog/` if missing.
- All I/O failures are non-fatal (logged to `errors.log`).

**Exit codes:** Always 0.

**Files touched:** `.devlog/task.md` (atomic write), `.devlog/task_updates.jsonl` (append), `.devlog/errors.log` (on error).

---

## task-tool-capture

Record TaskCreate and TaskUpdate tool calls.

```
devlog task-tool-capture
```

Invoked by Claude Code's **PostToolUse** hook (matcher: `TaskCreate|TaskUpdate`). Reads hook input from stdin.

**Flags:** None.

**Behavior:**

- Only records TaskCreate and TaskUpdate tools; other tools are silently ignored.
- Appends one JSONL entry to `.devlog/tasks.jsonl`: `{ts, tool, tool_input}`.
- Uses flock-protected append for atomicity across concurrent writers.
- All errors are non-fatal (logged to `errors.log`).

**Exit codes:** Always 0.

**Files touched:** `.devlog/tasks.jsonl` (locked append), `.devlog/errors.log` (on error).

---

## check-feedback

Output pending companion feedback. If no feedback is pending, exits silently.

```
devlog check-feedback
```

Invoked by Claude Code's **PreToolUse** hook (matcher: `.*` — fires before every tool call).

**Flags:** None.

**Behavior:**

- Must be near-instant (<50ms budget).
- Common path: 2 syscalls (Getwd + Stat on feedback.md).
- If `.devlog/feedback.md` is empty or missing: exits 0 with no output.
- If feedback.md has content:
  - Prints content to stdout (Claude Code injects this into the agent's context).
  - Archives the entry to `.devlog/feedback_archive.jsonl`.
  - Truncates `feedback.md` to zero bytes.
- All I/O failures are silent (logged to `errors.log`).

**Exit codes:** Always 0.

**Files touched:** `.devlog/feedback.md` (read + truncate), `.devlog/feedback_archive.jsonl` (append), `.devlog/errors.log` (on error).

---

## flush

Drain the diff buffer through the Haiku summarizer into the dev log.

```
devlog flush [--dry-run] [--project DIR]
```

Typically spawned automatically by `capture` when the buffer threshold is reached. Can also be run manually.

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | bool | `false` | Print the prompt that would be sent to Haiku, then exit without mutating state |
| `--project` | string | `.` | Project root directory |

**Behavior:**

- Sets `flush_in_progress` flag before work begins (under lock). Concurrent invocations that find the flag already set exit 0 as a no-op.
- Reads buffer entries; if empty, returns immediately.
- Reads `task.md` and prior log entries for context.
- Invokes the Haiku model via `claude -p` subprocess.
- On success: appends a log entry to `log.jsonl`, archives buffer entries to `buffer_archive.jsonl`, bumps log counters.
- When `log_since_companion >= companion_interval`: spawns a detached `devlog companion` process.
- On summarizer failure: buffer is NOT archived (retries on next flush).
- The `flush_in_progress` flag is always cleared on exit, even on error.

**Exit codes:** 0 success or already in progress, 1 error, 2 usage error.

**Files touched:** `.devlog/state.json` (locked), `.devlog/buffer.jsonl` (read), `.devlog/buffer_archive.jsonl` (append), `.devlog/log.jsonl` (append), `.devlog/task.md` (read), `.devlog/errors.log` (on error).

---

## companion

Run the Sonnet anti-pattern assessment on the current dev trajectory.

```
devlog companion [--dry-run] [--project DIR]
```

Typically spawned automatically by `flush` when the log threshold is reached. Can also be run manually.

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run` | bool | `false` | Build and print the prompt without invoking Sonnet |
| `--project` | string | `.` | Project root directory |

**Behavior:**

1. Gathers inputs: `task.md`, `task_updates.jsonl`, `log.jsonl`, `buffer_archive.jsonl`, `tasks.jsonl`. Missing files render as `(none)` in the prompt.
2. Builds the Sonnet prompt via `prompt.BuildCompanionPrompt()`.
3. If `--dry-run`: prints the prompt and exits.
4. Acquires `companion_in_progress` guard. If already in progress, prints "skipping" to stderr and exits 0.
5. Invokes the Sonnet model via `claude -p` subprocess.
6. Parses the JSON result. Commits the verdict to `state.json` and resets `log_since_companion`.
7. If the verdict is **DRIFTING** or **SPIRALING**: formats and writes intervention to `feedback.md`.
8. On error: guard is released, `log_since_companion` is NOT reset (retries on next threshold crossing).

**Anti-patterns detected:**

| Pattern | Description |
|---------|-------------|
| Repetition Lock | N+ consecutive changes to same file/module with no progress |
| Oscillation | Alternating between two approaches (A→B→A→B) |
| Scope Creep Under Failure | Each attempt touches more files than the last |
| Mock/Stub Escape | Creating test doubles that simulate success without solving the real problem |
| Undo Cycle | Reverting changes from 2-3 attempts ago |
| Confidence Escalation | Repeated claims of "found root cause" followed by failure |
| Tangential Resolution | Fixing something adjacent to the actual problem |

**Intervention format** (written to feedback.md when DRIFTING/SPIRALING):

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
[DevLog Companion — Trajectory Assessment]

STATUS: SPIRALING (confidence: 85%)

PATTERN DETECTED: Repetition Lock
EVIDENCE: [specific log entries]

REFRAME: Strategic question to ask instead
ACTION: Concrete next step
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

**Exit codes:** 0 success or dry-run, 1 error.

**Files touched:** `.devlog/state.json` (locked), `.devlog/task.md` (read), `.devlog/task_updates.jsonl` (read), `.devlog/log.jsonl` (read), `.devlog/buffer_archive.jsonl` (read), `.devlog/tasks.jsonl` (read), `.devlog/feedback.md` (write on DRIFTING/SPIRALING), `.devlog/errors.log` (on error).

---

## status

Show session state, counters, and health checks.

```
devlog status
```

**Flags:** None.

**Output:**

```
Session
  ID:              a1b2c3d4e5f67890
  Started:         2026-04-23T10:30:00Z
  Buffer entries:  3
  Log entries:     7
  Since companion: 2
  Last companion:  ON_TRACK (confidence: 90%)

Health
  git:     OK   (.git found)
  claude:  OK   (claude in PATH)
  .devlog: OK   (.devlog/ exists)
```

**Behavior:**

- Prints session summary from `state.json` (if present).
- Runs three health checks:
  - **git:** `.git` directory exists in cwd or ancestors.
  - **claude:** `claude` binary is on PATH.
  - **.devlog:** `.devlog/` directory exists and is readable.
- Respects the `NO_COLOR` environment variable — when set to any non-empty value, ANSI color codes are omitted.

**Exit codes:** 0 all health checks pass, 1 any health check fails.

**Environment variables:** `NO_COLOR` — disable colored output.

---

## log

Print the dev log narrative.

```
devlog log [--json] [--tail N] [--project DIR]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | bool | `false` | Emit raw `log.jsonl` contents instead of formatted view |
| `--tail` | int | `0` (all) | Show only the last N entries |
| `--project` | string | `.` | Project root directory |

**Behavior:**

- Default: prints one line per log entry as `#<seq> [<timestamp>] <summary>`.
- Missing or empty log file: prints `(no entries)` and exits 0.
- With `--json`: dumps the JSONL file verbatim.
- With `--tail N`: shows only the last N entries.
- Timestamps are always RFC3339 UTC.

**Exit codes:** 0 success, 1 I/O error, 2 usage error (e.g. `--tail` < 0).

**Files read:** `.devlog/log.jsonl`

---

## reset

Clear all state for a fresh session.

```
devlog reset [--yes] [--keep-log] [--project DIR]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--yes` | bool | `false` | Skip y/N confirmation prompt (required for scripted use) |
| `--keep-log` | bool | `false` | Preserve `log.jsonl` (keeps narrative history) |
| `--project` | string | `.` | Project root directory |

**Behavior:**

- Truncates: `buffer.jsonl`, `feedback.md`, `task.md`, `task_updates.jsonl`, `tasks.jsonl`, and `log.jsonl` (unless `--keep-log`).
- Zeroes state counters: BufferCount, BufferSeq, LogCount, LogSeq, LogSinceCompanion, FlushInProgress, CompanionInProgress.
- Preserves: SessionID, StartedAt, LastCompanion.
- Without `--yes`: prompts for y/N confirmation (anything other than y/Y exits 1).

**Exit codes:** 0 success, 1 rejected/error, 2 usage error.

---

## config

Get or set tunable parameters.

```
devlog config                  # list all
devlog config <key>            # get one
devlog config <key> <value>    # set one
```

**Flags:** None.

**Configuration keys:**

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `buffer_size` | int | `10` | Diffs before triggering summarizer |
| `companion_interval` | int | `5` | Log entries before triggering companion |
| `summarizer_model` | string | `claude-haiku-4-5-20251001` | Model for summarization |
| `companion_model` | string | `claude-sonnet-4-6` | Model for anti-pattern detection |
| `summarizer_context_entries` | int | `5` | Prior log entries included in summarizer prompt |
| `companion_log_entries` | int | `25` | Log entries shown to companion |
| `companion_diff_entries` | int | `50` | Archived diffs shown to companion |
| `enabled` | bool | `true` | Master on/off switch |
| `max_diff_chars` | int | `2000` | Max characters per diff in buffer |
| `max_detail_chars` | int | `200` | Max characters for Edit old/new summaries |
| `host` | string | `claude` | Host backend: `claude` or `opencode`. Selected automatically by `devlog install`; hook commands read it to pick the right payload parser. |
| `host_command` | string | `claude` | Path to the host CLI binary (e.g. `claude`, `opencode`). Used by `flush` and `companion` to spawn the model. |
| `claude_command` | string | `claude` | **Deprecated.** Legacy alias for `host_command`. On first load, if `claude_command` is present and `host_command` is absent, the value is mirrored into `host_command` automatically. Still written so older binaries keep working. |
| `summarizer_timeout_seconds` | int | `60` | Timeout for Haiku invocation |
| `companion_timeout_seconds` | int | `120` | Timeout for Sonnet invocation |

**Validation:** Rejects negative timeouts, empty model names, and zero/negative buffer sizes. Boolean values accept `true`/`false`/`yes`/`no`/`on`/`off`/`0`/`1`.

**Exit codes:** 0 success, 1 unknown key or validation error, 2 usage error.

**Files touched:** `.devlog/config.json` (read on load, write on set).

---

## install

Install DevLog hooks into the chosen host's settings (Claude Code `settings.json` or OpenCode `opencode.json` + plugin directory).

```
devlog install [--host claude|opencode] [--settings PATH]
               [--summarizer-model ID] [--companion-model ID]
               [--host-command PATH] [--claude-command PATH]
               [--plugin-dir DIR] [--opencode-config PATH]
               [--project DIR]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--host` | string | (auto-detected) | Host backend: `claude` or `opencode`. When empty, both hosts are probed via their `Detect()` implementations; if exactly one is present it is used, if both are present `claude` wins with a hint about `--host opencode`, and if neither is present installation fails with install links. |
| `--settings` | string | (resolved) | Path to Claude Code `settings.json`. Claude host only. |
| `--summarizer-model` | string | (config default) | Override the summarizer model ID written to `.devlog/config.json`. |
| `--companion-model` | string | (config default) | Override the companion model ID written to `.devlog/config.json`. |
| `--host-command` | string | (config default) | Path to the host CLI binary (e.g. `claude`, `opencode`). Persisted to `host_command`. |
| `--claude-command` | string | — | Deprecated alias for `--host-command`. Accepted for backward compatibility; `--host-command` wins when both are provided. |
| `--plugin-dir` | string | `.opencode/plugins` | OpenCode plugin output directory. OpenCode host only. |
| `--opencode-config` | string | `opencode.json` | OpenCode config path. OpenCode host only. |
| `--project` | string | cwd | Project root used to locate `.devlog/`. |

**Settings path resolution (Claude host):**

1. `--settings` flag (if provided)
2. `CLAUDE_SETTINGS_PATH` environment variable (if set)
3. `$HOME/.claude/settings.json` (user-scoped default)

**Hook entries written (Claude host):**

| Hook Kind | Matcher | Command |
|-----------|---------|---------|
| UserPromptSubmit | *(empty)* | `devlog task-capture` |
| PostToolUse | `Edit\|Write\|Bash` | `devlog capture` |
| PostToolUse | `TaskCreate\|TaskUpdate` | `devlog task-tool-capture` |
| PreToolUse | `.*` | `devlog check-feedback` |

**OpenCode host:** writes the bundled TypeScript plugin shim to `<plugin-dir>/devlog.ts` and registers it in `<opencode-config>`. The plugin forwards OpenCode events (`chat.message`, `tool.execute.before/after`, `todo.updated`) into the same `devlog` subcommands the Claude hooks invoke.

**Behavior:**

- Creates the settings file and parent directory if missing.
- Idempotent: existing entries matching the same (matcher, command) pair are never duplicated.
- Pre-existing unrelated hooks are preserved in place.
- Write is atomic (temp file + rename).
- Persists the resolved host, host command, and optional model overrides to `.devlog/config.json`.

**Exit codes:** 0 success, 1 error, 2 usage error.

**Environment variables:** `CLAUDE_SETTINGS_PATH`, `HOME`.

---

## uninstall

Remove DevLog hook entries from Claude Code's settings.json.

```
devlog uninstall [--settings PATH]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--settings` | string | (resolved) | Path to Claude Code settings.json |

**Behavior:**

- Removes every entry whose `command` field begins with `devlog ` (or is exactly `devlog`).
- Unrelated hooks are preserved in place.
- Idempotent: missing settings file or no matching hooks is a successful no-op.
- Settings path resolution is identical to `install`.
- Write is atomic (temp file + rename).

**Exit codes:** 0 success, 1 error, 2 usage error.

**Environment variables:** `CLAUDE_SETTINGS_PATH`, `HOME`.

---

## File Reference

All files are created under `.devlog/` in the project root (initialized by `devlog init`).

| File | Format | Description |
|------|--------|-------------|
| `state.json` | JSON | Session metadata: ID, timestamps, counters, guard flags, last companion verdict |
| `config.json` | JSON | Tunable parameters (overlaid on defaults) |
| `buffer.jsonl` | JSONL | Raw diff entries from Edit/Write/Bash tool calls |
| `buffer_archive.jsonl` | JSONL | Buffer entries archived after flush |
| `log.jsonl` | JSONL | Summarized dev log entries (output of Haiku) |
| `task.md` | Markdown | Original user prompt (first message of session) |
| `task_updates.jsonl` | JSONL | Subsequent user prompts (course corrections) |
| `tasks.jsonl` | JSONL | Captured TaskCreate/TaskUpdate tool calls |
| `feedback.md` | Markdown | Pending companion intervention (consumed by check-feedback) |
| `feedback_archive.jsonl` | JSONL | Archived feedback entries |
| `errors.log` | Text | Non-fatal hook errors |

---

## Global Contracts

**Hook safety:** All hook commands (`capture`, `task-capture`, `task-tool-capture`, `check-feedback`) exit 0 on error. Failures are logged to `errors.log` and never block the working agent.

**Atomicity:** State mutations use cross-process flock on `state.json`. Buffer and task writes use sidecar `.lock` files.

**Concurrency guards:** `flush` sets `flush_in_progress`; `companion` sets `companion_in_progress`. Concurrent invocations that find the flag set exit 0 as a no-op.

**Graceful degradation:** Missing input files (task.md, log.jsonl, etc.) are treated as empty — commands never fail because optional context is absent.
