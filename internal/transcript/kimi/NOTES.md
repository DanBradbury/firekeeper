# Kimi Code transcript source

Findings from Kimi Code's open-source repository (`MoonshotAI/kimi-code`,
checked October 2026 at wire protocol 1.5): the generated record manifest
`packages/agent-core-v2/docs/wire-manifest.d.ts`, the loop event types in
`src/agent/contextMemory/loopEventFold.ts`, session metadata in
`src/session/sessionMetadata/sessionMetadata.ts`, Kimi's own transcript search
extractor `packages/kap-server/src/search/wireExtract.ts`, and the session
fixtures under `apps/vis/server/test/fixtures/sessions`. No local Kimi data was
inspected. These shapes are Kimi's internal persistence format, not a
published API, and they change between releases.

This reader is for Kimi Code (`kimi`, data in `~/.kimi-code`). The older
Python Kimi CLI (`~/.kimi`, `context.jsonl`) writes a different format and is
not read.

## Layout

```text
$KIMI_CODE_HOME/                       (default ~/.kimi-code; KIMI_SHARE_DIR is a fallback)
  session_index.jsonl                  {sessionId, sessionDir, workDir} per line
  sessions/<work dir key>/<session id>/
    state.json                         session metadata
    agents/main/wire.jsonl             the main agent's complete record
    agents/agent-<n>/wire.jsonl        one log per subagent
    tasks/, cron/, logs/, upcoming-goals.json
```

The work directory key is `wd_<slug>_<first 12 hex of sha256(cwd)>`. Session ids
look like `session_<uuid>`. The reader accepts any directory name made of
letters, digits, `_`, `-` and `.`, which rules out path traversal and glob
patterns.

**Only `agents/main/wire.jsonl` is read.** Each agent's log is its own `seq`
space, so subagent logs cannot share the session's idempotency key. The main
log still records `subagent.spawned`/`subagent.completed` (kept as `meta`),
but subagents' own messages, tool calls, and token usage are not uploaded.
The Usage tab, which sums every `wire.jsonl`, therefore counts more tokens
than the uploaded session for sessions that ran subagents.

## state.json

Current releases write `{id, version: 2, cwd, createdAt, updatedAt, title,
titleKind, lastPrompt, archived, agents, ...}` with **epoch-millisecond**
timestamps. Older releases (and the vis fixtures) wrote RFC 3339 strings and
`workDir`. `session.KimiSessionState` decodes both. Enumeration takes title,
working directory, and start time from it, and uses the later of `updatedAt`
and the log's mtime for activity, because state.json can lag the log.
`lastPrompt` is never read.

## Envelope

```json
{"type": "<record type>", "agentId": "main", ...payload, "time": 1791460800000}
```

- The first line is `{"type": "metadata", "protocol_version", "created_at"}`;
  `created_at` becomes `ts` for it.
- `time` is epoch milliseconds and becomes `ts`. Values below 1e12 are taken as
  seconds, as Kimi's own reader does; RFC 3339 strings are also accepted.
  Missing, non-positive, or out-of-range values give a null `ts`.
- `seq` is the zero-based line index.

## Record types and mapping

| `type` | Event |
| --- | --- |
| `context.append_message`, role `user` | `user` when the origin is something a person typed: none, `user`, `shell_command`, or `skill_activation`/`plugin_command` with `trigger: "user-slash"`. Every other origin (`injection`, `system_trigger`, `retry`, `compaction_summary`, `task`, `cron_job`, `cron_missed`, `hook_result`) is `system`. Text is the concatenated `text` parts. |
| `context.append_message`, role `assistant` | `tool_call` when it has `toolCalls` (text is the reply text plus each call's JSON-string `arguments`; tool name from the first call), else `assistant`, else `meta` holding `think` text. Live sessions do not write this form; it comes from sessions migrated from older releases. |
| `context.append_message`, role `tool` | `tool_result`; `tool_name` from the matching call; text truncated at 64 KiB |
| `context.append_message`, role `system` | `system` |
| `context.append_loop_event` / `content.part` | `text` parts are `assistant`; `think` parts are `meta` with the thinking text; other parts (images) are `meta` |
| `context.append_loop_event` / `tool.call` | `tool_call`; `tool_name` is `name`; text is compact JSON `args` |
| `context.append_loop_event` / `tool.result` | `tool_result`; `tool_name` from the matching call; text is `result.output` (a string or content parts), truncated at 64 KiB |
| `context.append_loop_event` / `step.begin`, `step.end` | `meta`. `step.end` repeats the step's usage, so it carries no tokens. |
| `llm.request` | `meta` with `model`; sets the model for later events |
| `usage.record` | `meta` with tokens and the model of the latest `llm.request` |
| `context.apply_compaction` | `system`; text is `summary`, `contextSummary`, or the summary message's text |
| `turn.prompt`, `turn.steer` | `meta` with empty text: the prompt is also written as the `context.append_message` that follows, which is the one shown |
| everything else (`config.update`, `turn.ended`, `permission.*`, `tools.*`, `goal.*`, `task.*`, `subagent.*`, `mcp.*`, ...), or an undecodable line | `meta` with empty text |

Assistant and tool call events carry the model of the latest `llm.request`.
`usage.record.model` is a configuration alias, such as the literal
`__kimi_env_model__`, not a model id; it is used only when no request was seen
and it is not a `__placeholder__`.

## Tokens

`usage` is `{inputOther, output, inputCacheRead, inputCacheCreation}`.
`inputOther` excludes cached input, so events carry `input = inputOther`,
`output = output`, and `cache = inputCacheRead + inputCacheCreation`.
Negative counts are clamped to zero.

Each `usage.record` is written once per provider call, with `usageScope:
"turn"` for loop steps and `"session"` for compaction and title calls that
have no loop step. Both are per-call deltas, so every record is counted, which
matches the Usage tab. Third-party tools disagree about `"session"` records;
Kimi's schema does not settle it.

## Files touched

`transcript.TouchedFiles` reads `tool.call` loop events named `Edit` or
`Write` (`args.path`), the same tools in migrated assistant messages
(`toolCalls[].arguments`, a JSON string), and apply_patch envelopes in any
other tool's arguments.

## Reading

- `Locate` returns `meta.RolloutPath` when discovery found the session and
  the path is that session's main log. Otherwise it searches
  `sessions/*/<id>/agents/main/wire.jsonl` and takes the most recently written
  match.
- `Read` is line-oriented like the other JSONL sources. A read from an offset
  rescans earlier lines for tool names and the current model.
- Kimi Code rewrites a log in place when a session is forked at a turn and
  when a wire protocol migration upgrades it. A rewrite that shrinks the file
  restarts from offset 0 and relies on server idempotency. One that grows it
  can leave the stored offset off a line boundary, which fails the read, or
  on a boundary past changed lines, which are then not re-sent.
- `context.undo` and `context.clear` are kept as `meta`. Events they retract
  stay in the transcript, as the log records them.
