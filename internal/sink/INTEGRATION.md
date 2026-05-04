# Sink integration — wiring sketch

## 1. Config change (`internal/state/config.go`)

Add `Sinks` field to Config struct:

```go
type Config struct {
    // ... existing fields ...
    CompanionTimeoutSeconds  int           `json:"companion_timeout_seconds"`
    Sinks                    []sink.SinkConfig `json:"sinks,omitempty"`
}
```

Default() returns `Sinks: nil` — no sinks configured means no overhead.

Example config.json with attest consuming via Unix socket:

```json
{
  "buffer_size": 10,
  "sinks": [
    {
      "type": "unix_socket",
      "path": "/tmp/attest/myproject.sock"
    }
  ]
}
```

## 2. Capture wiring (`cmd/capture.go`)

The sink emission goes OUTSIDE the state lock, after buffer.Append
succeeds but before the flush check. The seq is assigned inside the
lock; the sink receives it after.

```go
func Capture(args []string) int {
    // ... existing preamble (stdin read, config load, hook parse) ...

    entry, err := buildBufferEntry(ev, cwd, cfg)
    // ... existing nil/error checks ...

    // --- NEW: open sinks once per invocation ---
    sinks, sinkErr := sink.OpenAll(cfg.Sinks)
    if sinkErr != nil {
        captureLogNonFatal(errorsLog, derrors.Wrap("capture", "open sinks", sinkErr))
        // Continue — sink failure never blocks capture
    }
    defer sinks.Close()

    var shouldFlush bool
    err = state.Update(statePath, func(s *state.State) error {
        s.BufferSeq++
        entry.Seq = s.BufferSeq
        if entry.SessionID == "" {
            entry.SessionID = s.SessionID
        }
        if err := buffer.Append(bufferPath, *entry); err != nil {
            return fmt.Errorf("append buffer: %w", err)
        }
        s.BufferCount++
        if s.BufferCount >= cfg.BufferSize && !s.FlushInProgress {
            shouldFlush = true
            s.FlushInProgress = true
            s.BufferCount = 0
        }
        return nil
    })
    if err != nil {
        captureLogNonFatal(errorsLog, derrors.Wrap("capture", "update state", err))
        return 0
    }

    // --- NEW: emit to sinks after lock released ---
    if sinkErr == nil {
        sinkEvent := sink.Event{
            Type:      sink.EventCapture,
            Seq:       entry.Seq,
            Timestamp: entry.TS,
            SessionID: entry.SessionID,
            Host:      cfg.Host,
            HookKind:  "PostToolUse",
            ToolName:  ev.ToolName,
            RawInput:  raw,  // full untruncated hook payload
        }
        if err := sinks.Emit(sinkEvent); err != nil {
            captureLogNonFatal(errorsLog, derrors.Wrap("capture", "sink emit", err))
        }
    }

    // ... existing flush spawn logic ...
}
```

Key: `RawInput: raw` passes the original bytes read from stdin — the
full hook JSON with untruncated tool_input. This is the lossless data
attest needs to compute content hashes and build in-toto subjects.

## 3. TaskCapture wiring (`cmd/task_capture.go`)

Same pattern — emit after the task.md / task_updates.jsonl write:

```go
func TaskCapture(args []string) int {
    // ... existing preamble ...

    // --- NEW: emit task event to sinks ---
    sinks, sinkErr := sink.OpenAll(cfg.Sinks)
    if sinkErr != nil {
        logNonFatal(errorsLog, derrors.Wrap("task-capture", "open sinks", sinkErr))
    }
    defer sinks.Close()

    // ... existing task.md / task_updates.jsonl writes ...

    if sinkErr == nil {
        sinkEvent := sink.Event{
            Type:      sink.EventTask,
            Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
            SessionID: ev.SessionID,
            Host:      cfg.Host,
            HookKind:  "UserPromptSubmit",
            RawInput:  raw,
        }
        if err := sinks.Emit(sinkEvent); err != nil {
            logNonFatal(errorsLog, derrors.Wrap("task-capture", "sink emit", err))
        }
    }

    return 0
}
```

## 4. TaskToolCapture wiring (`cmd/task_tool_capture.go`)

```go
// After appending to tasks.jsonl:
sinkEvent := sink.Event{
    Type:      sink.EventTaskTool,
    Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
    SessionID: ev.SessionID,
    Host:      cfg.Host,
    HookKind:  "PostToolUse",
    ToolName:  ev.ToolName,
    RawInput:  raw,
}
```

## 5. Flush wiring (`cmd/flush.go`) — optional, lower priority

After the summarizer produces a log entry:

```go
sinkEvent := sink.Event{
    Type:      sink.EventLog,
    Seq:       logEntry.Seq,
    Timestamp: logEntry.TS,
    SessionID: logEntry.SessionID,
    Host:      cfg.Host,
    HookKind:  "flush",
    RawInput:  logEntryJSON,  // the full log.jsonl entry
}
```

## 6. Companion wiring (`cmd/companion.go`) — optional, lower priority

After the companion assessment completes:

```go
sinkEvent := sink.Event{
    Type:      sink.EventCompanion,
    Timestamp: assessment.TS,
    SessionID: sessionID,
    Host:      cfg.Host,
    HookKind:  "companion",
    RawInput:  assessmentJSON,
}
```

## What attest receives

With this sink, attest's daemon listening on the Unix socket gets a
stream of NDJSON events like:

```json
{"type":"task","ts":"2026-05-04T02:30:00Z","session_id":"abc123","host":"claude","hook_kind":"UserPromptSubmit","raw_input":{"session_id":"abc123","transcript_path":"/...","cwd":"/project","prompt":"Fix the auth middleware"}}
{"type":"capture","seq":1,"ts":"2026-05-04T02:30:05Z","session_id":"abc123","host":"claude","hook_kind":"PostToolUse","tool_name":"Edit","raw_input":{"session_id":"abc123","cwd":"/project","tool_name":"Edit","tool_input":{"file_path":"src/auth.go","old_string":"func Check(...)","new_string":"func Check(...) error"}}}
{"type":"capture","seq":2,"ts":"2026-05-04T02:30:08Z","session_id":"abc123","host":"claude","hook_kind":"PostToolUse","tool_name":"Write","raw_input":{"session_id":"abc123","cwd":"/project","tool_name":"Write","tool_input":{"file_path":"src/auth_test.go","content":"package auth\n\nfunc TestCheck(t *testing.T) {\n..."}}}
```

From this, attest can:
- Extract `file_path` from `raw_input.tool_input` for in-toto subjects
- Hash file contents at `file_path` for `subject.digest`
- Use the most recent `task` event's `prompt` as the `reason` field
- Use `session_id` as `agent.session_id`
- Derive `turn_id` from the sequential `seq` numbers
- Use `host` to populate `agent.id` (claude-code, opencode)
- Detect the agent version from `raw_input` or by shelling out

## Performance impact

- JSONL sink: one json.Marshal + one file append per event. ~0.5ms.
- Socket sink: one json.Marshal + one dial + one write + one close.
  ~2-5ms when attest daemon is listening. ~1ms fast-fail when it's not.
- No sinks configured: zero overhead (noop sink).
- All within the 200ms PostToolUse budget.
