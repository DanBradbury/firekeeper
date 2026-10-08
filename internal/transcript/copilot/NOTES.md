# Copilot CLI transcript source

Findings from local GitHub Copilot CLI data inspected for T0.5 (October 2026,
five local sessions). These are the shapes seen locally, not a published
format. Copilot changes them without notice.

## Which store holds the conversation

**`events.jsonl` is the complete conversation record, so the source reads only
`events.jsonl`. `session-store.db` is not read.** The one thing that file does
not give per call is main-agent token usage; see "Tokens" below.

- `$COPILOT_HOME/session-state/<session uuid>/events.jsonl` holds one JSON
  object per line, newline-terminated, and is only ever appended to. It has
  every user, system, and assistant message, every tool start and result,
  permission prompts, turn lifecycle, model changes, and a `session.shutdown`
  summary per run. Every inspected tool call had a matching result.
- `workspace.yaml` in the same directory holds session metadata: `id`, `cwd`,
  `git_root`, `repository`, `host_type`, `branch`, `client_name`, `name`,
  `user_named`, `summary_count`, `fork_count`, `created_at`, `updated_at`, and
  some remote-task ids. Discovery (`session.LoadCopilotSession`) already reads
  it for session metadata, so the transcript source reads it only to recover
  the session id when the events file is not in a `session-state/<uuid>/`
  directory.
- The directory also holds `checkpoints/`, `files/`, `research/`, and
  `rewind-file-snapshots/`. None of them is a transcript.
- `session-store.db` tables: `sessions` (metadata), `turns` (`user_message` and
  `assistant_response` per turn, with no tool calls or system messages),
  `assistant_usage_events` (one row per main-agent model call, with tokens),
  `session_files`, `checkpoints`, `dynamic_context_items`, `forge_*`, and a
  full-text `search_index`. Everything conversational in it is a subset of
  `events.jsonl`.

## Envelope

```json
{"type": "<event type>", "data": {}, "id": "<uuid>", "timestamp": "2026-01-02T03:04:05.123Z", "parentId": "<uuid or null>"}
```

- `timestamp` is RFC 3339 UTC with milliseconds and becomes `ts`. If it cannot
  be parsed, `ts` is null.
- `id` and `parentId` link events into a chain. The parser does not use them;
  `seq` is the line index, as the contract requires.

## Event types and mapping

| `type` | `data` keys seen | Event |
| --- | --- | --- |
| `session.start` | `sessionId`, `version`, `producer`, `copilotVersion`, `startTime`, `context`, `contextTier`, `alreadyInUse`, `remoteSteerable` | `meta`. Supplies `session_id` when neither the directory nor `workspace.yaml` does. |
| `session.model_change` | `newModel`, `previousModel`, `reasoningEffort`, `contextTier`, `cause`, `source` | `meta` with `model`. Sets the model for later events. |
| `system.message` | `role` (`system`), `content`, `contentBlocks`, `metadata` | `system`; text is `content` |
| `user.message` | `content`, `transformedContent`, `messageId`, `delivery`, `interactionId`, `turnId`, ... | `user`; text is `content` (what the user typed) |
| `assistant.message` | `messageId`, `model`, `content`, `toolRequests[{toolCallId, name, arguments, type, ...}]`, `phase` (`commentary`, `final_answer`, or absent), `reasoningText`, `reasoningOpaque`, `encryptedContent`, `reasoningBlocks`, ... | `assistant` with model; text is `content`, which is empty when the message only requests tools. Tool names are recorded from `toolRequests`. |
| `tool.execution_start` | `toolCallId`, `toolName`, `arguments` (object), `toolTitle`, `model`, `shellToolInfo`, `turnId` | `tool_call`; text is `arguments` as compact JSON |
| `tool.execution_complete` | `toolCallId`, `success`, `result{content, detailedContent}`, `model`, `shellExecution{exitCode}`, `fileEdits`, `toolTelemetry`, ... | `tool_result`; `tool_name` comes from the matching start or request; text is `result.content`, truncated at 64 KiB |
| `model.model_call_success` | `kind`, `callId`, `modelCall{model, ...}`, `responseUsage`, `responseChunk`, `copilotUsage`, `quotaSnapshots`, `requestMessages`, timing | `meta` with tokens from `responseUsage` and model from `modelCall.model` |
| `session.shutdown` | `shutdownType`, `modelMetrics{<model>: {requests, usage{inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, reasoningTokens}}}`, `tokenDetails`, `codeChanges`, `currentModel`, `*Tokens`, ... | `meta` with tokens summed over `modelMetrics` |
| `assistant.turn_start`, `assistant.turn_end`, `permission.requested`, `permission.completed`, `abort`, `session.info`, `session.context_changed`, `session.permissions_changed`, `session.usage_checkpoint` | | `meta` |
| `model.turn_started`, `model.turn_ended`, `model.model_call_started`, `model.message`, `model.response`, `model.messages_snapshot`, `model.captured_assignment_context` | | `meta`. `model.message` and `model.response` repeat assistant text for auxiliary calls. |
| anything else, or an undecodable line | | `meta` with empty text |

## Tokens

**Per-call usage for the main agent is not in `events.jsonl`.** It is only in
`session-store.db` `assistant_usage_events`. The file has two token sources:

- `model.model_call_success` carries `responseUsage` for a few auxiliary model
  calls (two per session locally, a few hundred tokens each). These calls are
  not in `assistant_usage_events` or in `session.shutdown` totals.
- `session.shutdown` carries per-run totals in `modelMetrics`. Checked locally:
  they equal the sums of `assistant_usage_events` for the session exactly.

Events carry `input = prompt_tokens - cached_tokens`, `cache = cached_tokens`,
and `output = completion_tokens` for calls, and `input = inputTokens -
cacheReadTokens`, `cache = cacheReadTokens`, `output = outputTokens` for
shutdowns, matching the Codex source where input excludes cache reads and
includes cache writes. Summing event tokens over a session therefore gives the
main-agent totals plus the auxiliary calls, with no double counting.

Reading `session-store.db` for per-call tokens was rejected: it would mix a
SQLite row cursor into a JSONL source and break the line-index `seq`.

## Reading

- `Locate` returns `meta.RolloutPath` when discovery set it to an
  `events.jsonl` in that session's `session-state/<uuid>/` directory and it
  exists. Otherwise it returns `session-state/<lower-cased id>/events.jsonl`
  if it exists. It honors `COPILOT_HOME`.
- `Read` offsets are byte positions just after a newline. A trailing line
  without a newline is still being written; it is not consumed, and the offset
  stops before it. There is no size or tail limit.
- `seq` is the line index. A read from a nonzero offset re-scans the earlier
  lines to count them and to recover the session id, current model, and the
  call id to tool name map. Only lines that mention `session.start`,
  `session.resume`, `session.model_change`, `assistant.message`, or
  `tool.execution_start` are decoded during that scan.
- An offset past the end or in the middle of a line is an error. The reporter
  resets to 0 when a file shrinks, as the local state contract describes.
- An undecodable line keeps its bytes in `raw` as a JSON string so that `raw`
  stays valid JSON.

## Enumeration

`Enumerate` (T2.5) lists every `session-state/<uuid>/events.jsonl` for
backfill, including ended sessions. In order, per session:

1. `stat` only: the directory must be a uuid, the file must be newer than
   `ModifiedAfter`, and an empty file is counted as skipped.
2. `workspace.yaml`: `cwd`, `git_root`, `branch`, `name`, `repository`,
   `created_at`, `updated_at`. `session-store.db` is not read.
3. If `workspace.yaml` gave a `cwd`, `Skip` runs now, before `events.jsonl`
   is opened.
4. `workspace.yaml` has no model, so for every kept session the first 64 KiB
   of `events.jsonl` are read for `session.start` (`context.cwd`, `gitRoot`,
   `branch`, `startTime`) and the last model named by `session.model_change`
   or `assistant.message`. These only fill fields `workspace.yaml` lacks. A
   head with no decodable line is counted as corrupt.
5. If `Skip` has not run yet, it runs now.

Event counts and token totals stay zero.

## Known gaps

- **Crashed runs lose main-agent tokens.** Totals arrive only with
  `session.shutdown`. A run that is killed, or still running, has no main-agent
  tokens in its events until it shuts down cleanly. Discovery's session
  metadata still reads `session-store.db` for `TokensUsed`.
- **Resumed sessions are unverified.** Every local session had one run. It is
  not known whether a later `session.shutdown` after a resume repeats or only
  adds to earlier totals. If it repeats them, event token sums over-count.
  `session.resume` did not appear locally; the parser treats it like
  `session.start` for the session id only.
- **Injected context is not separated.** `user.message` text is `content`;
  `transformedContent`, which adds injected context, is only in `raw`.
- **Reasoning stays out of text.** `reasoningText` (sometimes present) and the
  encrypted reasoning fields are only in `raw`.
- **Failed tool results are guessed.** No failed `tool.execution_complete` was
  seen locally. Without `result.content`, text falls back to a string `error`
  or `error.message`.
- **Older layouts are not supported.** Only the `session-state/<uuid>/events.jsonl`
  layout was seen locally. Earlier Copilot CLI versions may have used other
  layouts; `Locate` does not look for them.
- **Mid-file reads cost a prefix scan**, as in the Codex source. Local files
  were under 250 KiB.
- **`machine_id` is empty.** `Read` sees only a path; the reporter fills in
  `machine_id`.
- **No redaction here.** `raw` is the line as written. `internal/redact` runs
  before upload.
