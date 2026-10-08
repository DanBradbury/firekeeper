# Claude Code transcript source

Findings from local Claude Code data inspected for T4.1 (Claude Code CLI on
Linux, October 2026; 17 session files, about 2,900 records). These are the
shapes seen locally, not a published format. Claude Code changes them without
notice.

## Where transcripts live

**One JSONL file per session is the complete record, so the source reads only
those files.**

- `$CLAUDE_CONFIG_DIR/projects/<encoded cwd>/<session uuid>.jsonl`, with
  `CLAUDE_CONFIG_DIR` defaulting to `~/.claude`. The directory name is the
  session's working directory with every character other than an ASCII letter
  or digit replaced by `-` (`/home/u/my.repo` becomes `-home-u-my-repo`).
  Files are only appended to.
- `<encoded cwd>/<session uuid>/tool-results/` holds large tool outputs that
  Claude Code moved out of the transcript. The transcript keeps the
  model-facing `tool_result`, which is what the source reads; the side files
  are not read.
- `<encoded cwd>/memory/` holds the memory feature's Markdown files. Not read.
- No `subagents/` directories and no `isSidechain: true` records appeared
  locally. See "Known gaps".

## Envelope

Conversation records (`user`, `assistant`, `system`, `attachment`) share:

```json
{"type": "...", "uuid": "...", "parentUuid": "... or null", "timestamp": "2026-01-02T03:04:05.123Z",
 "sessionId": "<uuid>", "cwd": "...", "gitBranch": "...", "version": "x.y.z", "entrypoint": "cli",
 "userType": "external", "isSidechain": false}
```

Optional keys seen: `isMeta`, `promptId`, `requestId`, `sessionKind` (`bg` for
background sessions), `session_id` (a duplicate of `sessionId`),
`permissionDecision`, `sourceToolAssistantUUID`, `toolUseResult`, and on
assistant records `apiBlockIndex`, `effort`, `perTurnEffort`, `advisorModel`,
`thinkingDurationMs`, `wireToolInputs`, `wireIngestContext`,
`serverClassifierRequest`.

Bookkeeping records (`mode`, `permission-mode`, `ai-title`, `last-prompt`,
...) carry only `type`, `sessionId`, and one or two fields. Most have no
`timestamp`, so their `ts` is null.

- `timestamp` is RFC 3339 UTC with milliseconds. If it is missing or cannot be
  parsed, `ts` is null.
- `seq` is the zero-based line index, as the contract requires. `uuid` and
  `parentUuid` form a tree that rewinds can branch, but the source does not
  use them for ordering.

## Record types and mapping

| `type` | Shape | Event |
| --- | --- | --- |
| `user`, `message.content` a string | `message{role, content}` | `user`; `system` when `isMeta` is true (context Claude Code injects, such as local-command caveats). Text is the string. |
| `user`, content has `tool_result` blocks | `content[{type: tool_result, tool_use_id, content (string or [text\|image\|tool_reference]), is_error?}]`, plus `toolUseResult` (a UI projection: `stdout`, `stderr`, `interrupted`, ...) | `tool_result`; `tool_name` from the matching `tool_use`; text is the joined text parts, truncated at 64 KiB. Images and `toolUseResult` stay in `raw`. |
| `user`, content has only `text` blocks | `content[{type: text, text}]` (prompts with attachments, the `[Request interrupted by user]` marker) | `user` (or `system` if `isMeta`) |
| `assistant` with a `tool_use` block | `message{id, model, role, content[{type: tool_use, id, name, input, caller}], stop_reason, usage}` | `tool_call`; `tool_name` is the first tool's name; text is any text blocks then each `input` as compact JSON |
| `assistant` with `text` blocks | `content[{type: text, text}]` | `assistant`; text is the joined texts |
| `assistant` with only `thinking` | `content[{type: thinking, thinking, signature}]` | `meta`; text is the thinking, as with Codex reasoning |
| `assistant`, `model: "<synthetic>"` | Claude Code's own notices (API errors, "no response requested") | mapped as above, but `model` is null |
| `system` | `subtype` (`turn_duration`, `local_command`, `away_summary`), `content` (string, optional), `level`, `durationMs`, `messageCount` | `system`; text is `content` when it is a string |
| `attachment` | `attachment{type, ...}` with types such as `date`, `environment`, `instructions`, `skill_listing`, `deferred_tools_delta`, `total_tokens_reminder`, `queued_command`, `hook_system_message`; optional `rendered`, `renderedRole` | `meta` |
| `mode`, `permission-mode`, `atis-latch`, `ai-title`, `agent-name`, `last-prompt`, `cost-state`, `pr-link`, `queue-operation`, `continued-in`, `file-history-snapshot`, `file-history-delta` | bookkeeping | `meta` |
| any record with `isSidechain: true` | as above | `meta`, keeping text, tool name, model, and tokens; see below |
| anything else, or an undecodable line | | `meta` with empty text |

Every assistant record seen held exactly one content block. Claude Code writes
one line per block of an API response, so a response with thinking, text, and
a tool call is three lines with the same `message.id` and `requestId`. A
record with several blocks is still handled: `tool_use` wins, then `text`,
then `thinking`.

### Tokens

`message.usage` has `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`, and extras
(`cache_creation{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`,
`output_tokens_details{thinking_tokens}`, `server_tool_use`, `service_tier`,
`iterations`, `speed`). Anthropic's `input_tokens` excludes cached tokens.
Events carry `input = input_tokens`, `output = output_tokens`, and
`cache = cache_read_input_tokens + cache_creation_input_tokens`.

**Usage repeats on every line of a response.** In all 276 multi-line
responses inspected, each line carried identical usage. Tokens are therefore
attached only to the first line of each `message.id`; later lines carry zero.
A line without a `message.id` counts on its own.

`cost-state` records hold cumulative per-model totals (`modelUsage{<model>:
{inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens,
thinkingTokens, costUSD, webSearchRequests}}`). They did not match the sum of
transcript usage locally: they include requests that are not written to the
transcript, such as title generation, and they are snapshots that can lag. The
parser leaves them as `meta`; discovery uses the latest one as a best-effort
`tokens_used` for the TUI.

### Sidechains and subagents

Records with `isSidechain: true` are subagent work. They are emitted as `meta`
so that they do not read as the main conversation, but they keep their text,
tool name, model, and tokens, and `raw` keeps the record's own `isSidechain`
(and `agentId`, when present) as the tag. The parser does not add fields to
`raw`, since `raw` is the original record.

## Reading

- `Locate` returns `meta.RolloutPath` when discovery set it and its file name
  matches the session id. Otherwise it searches `projects/*/<id>.jsonl`. It
  honors `CLAUDE_CONFIG_DIR`.
- `Read` offsets are byte positions just after a newline. A trailing line
  without a newline is still being written; it is not consumed, and the
  offset stops before it.
- A read from a nonzero offset re-scans earlier lines to count them and to
  rebuild the tool-name map and the set of message ids whose usage was
  counted. Only lines containing `"assistant"` are decoded during that scan
  (plus the first lines until a `sessionId` is found, when the file name is
  not a uuid).
- An offset past the end or in the middle of a line is an error.
- An undecodable line keeps its bytes in `raw` as a JSON string.
- `session_id` comes from the file name, or the first record's `sessionId`.

## Discovery (`internal/session/claude.go`)

- **Classification.** A process is Claude Code when its executable is
  `claude` or `claude.exe`, or `node`/`bun` running
  `@anthropic-ai/claude-code/`. This is checked before the substring rules for
  other harnesses, because resumed transcript paths in Claude Code command
  lines can contain words like `codex`. Helper subcommands (`daemon`,
  `bg-*`, `mcp`, `config`, `update`, `doctor`, `install`, `plugin`, `auth`,
  ...) and the Claude desktop app are not sessions. Grouping is the same
  parent-chain grouping as other providers, and the provider is `claude`.
- **Correlation.** Claude Code does not keep its transcript open, so `lsof`
  on the file does not work. An explicit `--session-id`, or `--resume <id or
  path>` without `--fork-session`, wins. Otherwise the process's working
  directory (via `lsof -d cwd`) is encoded to its project directory and the
  most recently written unclaimed transcripts there are used. When several
  runtimes share a directory, higher PIDs get newer transcripts. That is a
  guess, and it can swap two sessions in the same directory.
- **Metadata.** The last 4 MiB of the transcript give `cwd`, `gitBranch`,
  `entrypoint` (shown as the source), the latest non-synthetic model, the
  title (`ai-title`, else `agent-name`), the last timestamp, and the state.
  The first 64 KiB give the start time. Transcript text is never kept.
- **State.** A prompt or tool result means `ACTIVE`; a pending `tool_use` means
  `ACTIVE`, or `NEEDS INPUT` for `AskUserQuestion` and `ExitPlanMode`; an
  assistant `end_turn`, a `turn_duration` record, or the interrupt marker
  means `WAITING`. Permission prompts are not written to the transcript, so a
  tool waiting for approval shows as `ACTIVE`.

## Known gaps

- **Subagent transcripts in separate files are not read.** Some Claude Code
  versions write subagent transcripts beside the session, under
  `<session>/subagents/`. None existed locally to confirm the layout or shape. Reading them under the parent's `session_id` would collide with the
  parent's `seq` values, so `Locate` skips them. Supporting them needs a seq
  scheme or a separate session id per subagent, which is a contract decision.
- **Long project paths.** Claude Code may shorten very long encoded directory
  names. Discovery's working-directory fallback then finds nothing; explicit
  ids and `Locate`'s search across all project directories still work.
- **Compaction and rewinds.** `compact_boundary` system records and summary
  records did not appear locally. They fall through to `system` or `meta`.
  Rewound branches stay in the file, so `seq` order can include abandoned
  turns.
- **Images.** Image blocks in prompts and tool results are not represented in
  text.
- **`machine_id` is empty.** `Read` sees only a path; the reporter fills it in.
- **No redaction here.** `raw` is the line as written, including prompts in
  `last-prompt` and `queue-operation` records. `internal/redact` runs before
  upload.
