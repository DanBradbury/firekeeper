# Codex transcript source

Findings from local Codex data inspected for T0.4 (Codex CLI writing
`history_mode: "paginated"` rollouts, October 2026). These are the shapes seen
locally, not a published format. Codex changes them without notice.

## Which store holds the conversation

**The rollout JSONL is the complete record, so the source reads only rollouts.**

- `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<local time>-<thread uuid>.jsonl`
  holds one JSON object per line and is only ever appended to. It contains
  every model-facing item (messages, tool calls, tool outputs, reasoning), plus
  turn lifecycle, per-response token usage, and UI projections. Archived threads
  move to `$CODEX_HOME/archived_sessions/`.
- `state_5.sqlite` `threads` holds one metadata row per thread: `rollout_path`,
  `cwd`, `model`, `title`/`name`/`preview`, `git_branch`, `tokens_used`, and
  timestamps. It stores no messages. Discovery already reads it for session
  metadata.
- `thread_history_1.sqlite` (`thread_items`, `thread_turns`) is a paginated UI
  projection built *from* the rollout. `thread_history_projection_state` stores
  `next_rollout_byte_offset` and `next_rollout_ordinal` into the rollout. It
  keeps only UI item types (`userMessage`, `agentMessage`, `commandExecution`,
  `reasoning`, `fileChange`, `webSearch`). It drops developer messages, tool
  arguments in model form, and token usage, and it can lag the rollout. It is
  not used.
- `history.jsonl` and `session_index.jsonl` hold prompt history and an index.
  Neither is a full transcript.

Checked locally: summing `last_token_usage` over `token_count` records exactly
matched `threads.tokens_used` for every inspected thread, and every tool call
had a matching output.

## Envelope

```json
{"timestamp": "2026-01-02T03:04:05.123Z", "type": "<record type>", "ordinal": 0, "payload": {}, "metadata": {}}
```

- `timestamp` is RFC 3339 UTC with milliseconds and becomes `ts`. If it
  cannot be parsed, `ts` is null.
- `ordinal` matched the zero-based line index in every file inspected. The
  parser still counts lines itself, as the contract requires, because older
  rollouts have no `ordinal`.
- `metadata` is optional and shows up on some `response_item` records. Keys
  seen: `client_authored`, `user_input_order`, `retained_source`,
  `mcp_attribution`, `fallback_token_limit_override`.

## Record types and mapping

| `type` / `payload.type` | Payload keys seen | Event |
| --- | --- | --- |
| `session_meta` | `id`, `session_id`, `timestamp`, `cwd`, `originator`, `cli_version`, `source`, `thread_source`, `model_provider`, `history_mode`, `context_window`, `base_instructions{text}`, `git{branch,commit_hash,repository_url}`, `runtime_workspace_roots`, `creator_*` | `meta`. Supplies `session_id` when the file name has no uuid. |
| `turn_context` | `turn_id`, `root_turn_id`, `cwd`, `model`, `approval_policy`, `sandbox_policy`, `file_system_sandbox_policy`, `permission_profile`, `collaboration_mode`, `summary`, `current_date`, `timezone`, `workspace_roots`, ... | `meta` with `model`. Sets the model for later events. |
| `world_state` | `full`, `state{permissions, model, skills, agents_md, ...}` | `meta` |
| `token_usage_record` | `response_id`, `usage`, `turn_token_usage`, `thread_token_usage`, ids | `meta`, no tokens (this duplicates `token_count`) |
| `response_item` / `message` | `id`, `role`, `content[{type: input_text\|output_text, text}]`, `phase` (`commentary`, `final_answer`; assistant only) | `user`, `assistant` (with model), or `system` for `developer`. Text is the joined content texts. |
| `response_item` / `reasoning` | `id`, `summary[]`, `encrypted_content` | `meta`. Text is the joined summary, which was empty in every record seen. |
| `response_item` / `custom_tool_call` | `id`, `call_id`, `name` (`exec`), `status`, `input` (string) | `tool_call`; text is `input` |
| `response_item` / `function_call` | `id`, `call_id`, `name` (`wait`, `request_user_input_async`), `arguments` (JSON string) | `tool_call`; text is `arguments` |
| `response_item` / `custom_tool_call_output`, `function_call_output` | `id`, `call_id`, `output` (string, or `[{type: input_text, text}]`) | `tool_result`; `tool_name` comes from the matching call; text is truncated at 64 KiB |
| `event_msg` / `token_count` | `info{last_token_usage, total_token_usage, model_context_window}`, `rate_limits` | `meta` with tokens from `last_token_usage` |
| `event_msg` / `task_started`, `task_complete` | `turn_id`, `started_at`, `completed_at`, `duration_ms`, `last_agent_message`, ... | `meta` |
| `event_msg` / `item_completed` | `item{type: UserMessage\|AgentMessage\|CommandExecution\|Reasoning\|FileChange\|Extension, ...}`, `turn_id`, `*_at_ms` | `meta`. These duplicate `response_item`s for the UI. |
| `event_msg` / `thread_settings_applied` | `thread_id`, `thread_settings` | `meta` |
| anything else, or an undecodable line | | `meta` with empty text |

Usage objects have `input_tokens`, `cached_input_tokens`,
`cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens`, and
`total_tokens`, where `total = input + output` and `input` already includes
cached tokens. Events carry `input = input_tokens - cached_input_tokens`,
`cache = cached_input_tokens`, and `output = output_tokens`, so the three
counts add up to `total_tokens`.

## Reading

- `Locate` returns `meta.RolloutPath` when discovery found the rollout open
  (via `lsof`) and its uuid matches the session. Otherwise it searches
  `sessions/*/*/*/` and `archived_sessions/` for a rollout whose file-name uuid
  matches, using the same `session.ThreadIDFromRolloutPath` rule as the
  open-file mapping. It honors `CODEX_HOME`.
- `Read` offsets are byte positions just after a newline. A trailing line
  without a newline is still being written; it is not consumed, and the
  offset stops before it. There is no size or tail limit.
- `seq` is the line index. A read from a nonzero offset re-scans the earlier
  lines to count them and to recover the current model and the call id to tool
  name map. Only lines that mention `turn_context`, `call_id`, or
  `session_meta` are decoded during that scan.
- An offset past the end or in the middle of a line is an error. The reporter
  resets to 0 when a file shrinks, as the local state contract describes.
- An undecodable line keeps its bytes in `raw` as a JSON string so that `raw`
  stays valid JSON.

## Enumeration

`Enumerate` (T2.5) lists every rollout under `sessions/*/*/*/` and
`archived_sessions/` for backfill, including ended threads. In order, per
rollout:

1. `stat` only: the file name must carry a thread uuid, the file must be
   newer than `ModifiedAfter`, and an empty file is counted as skipped.
2. `state_5.sqlite` `threads` (one `sqlite3 -readonly -json` query for the
   schema, one for the rows): `name`/`title`, `cwd`, `model`, `git_branch`,
   `created_at[_ms]`, `updated_at[_ms]`. `preview` and `first_user_message`
   hold conversation text and are not read.
3. If the row gave a `cwd`, `Skip` runs now, before the rollout is opened.
4. Only if `cwd`, branch, model, or start time is still missing, the first
   64 KiB of the rollout are read for `session_meta` (`cwd`, `git.branch`,
   `timestamp`) and `turn_context` (`model`). A head with no decodable line is
   counted as corrupt.
5. If `Skip` has not run yet, it runs now.

Title and last activity come only from the database. Event counts and token
totals stay zero. A thread present in both directories is listed once, from
the newer file.

## Known gaps

- **Injected context looks like user input.** Codex sends AGENTS.md contents
  and `<environment_context>` as `role: user` messages, and they map to `user`.
  Real input carries `metadata.user_input_order`, but older rollouts have no
  metadata at all, so the parser does not use it to tell the two apart.
- **Reasoning text is mostly unavailable.** `encrypted_content` is opaque and
  `summary` was empty in all local data.
- **UI projections stay meta.** `item_completed` holds nicer command output
  (`aggregated_output`, `exit_code`, `parsed_cmd`) and file-change diffs, but
  emitting them as tool events would duplicate the `response_item`s. Diffs
  from `FileChange` are only in `raw`.
- **Only some shapes are interpreted.** `local_shell_call`, `web_search_call`,
  `compacted`, and legacy `event_msg` types such as `user_message`,
  `agent_message`, `exec_command_end`, and `error` did not appear in local data
  and fall through to `meta`. So do older, un-enveloped rollouts, whose lines
  are bare items without `payload`. Support for these should come with real
  samples.
- **Seq is per file.** A forked thread starts a new rollout with its own uuid;
  the source does not link it to its parent (`session_meta.parent_thread_id`).
- **Mid-file reads cost a prefix scan.** A read from offset N reads N bytes to
  rebuild seq and state. That stays cheap at the sizes seen locally (about
  1 MiB), but it grows with the rollout. The source could cache parser state by
  offset later.
- **`machine_id` is empty.** `Read` sees only a path; the reporter fills in
  `machine_id`.
- **No redaction here.** `raw` is the line as written. `internal/redact` runs
  before upload.
