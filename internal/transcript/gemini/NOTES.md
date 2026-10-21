# Gemini CLI transcript source

Findings from Gemini CLI's open-source repository (`google-gemini/gemini-cli`,
checked October 2026 on `main`): the chat recorder
`packages/core/src/services/chatRecordingService.ts` and its record types in
`chatRecordingTypes.ts`, storage paths in `packages/core/src/config/storage.ts`
and `projectRegistry.ts`, the CLI flags in `packages/cli/src/config/config.ts`,
and where `packages/core/src/core/geminiChat.ts` records messages, tokens, and
tool calls. No local Gemini data was inspected. These shapes are Gemini CLI's
internal persistence format, not a published API, and they change between
releases.

## Layout

```text
$GEMINI_CLI_HOME/.gemini/              (default ~/.gemini; ~/.cache/.gemini under SANDBOX=sandbox-exec)
  projects.json                        {"projects": {"<project root>": "<short id>"}}
  tmp/<short id>/
    .project_root                      the project root, as plain text
    chats/session-<YYYY-MM-DDTHH-MM>-<first 8 of id>.jsonl   one main session log
    chats/<session id>/<subagent id>.jsonl                   one log per subagent
  tmp/<sha256 of project root>/        releases before the registry; migrated on start
```

Gemini CLI's `GEMINI_CLI_HOME` replaces the home directory, so the data is in
its `.gemini`. Both the normal and the sandbox directory are searched.

The short id is a slug of the project's base name (`[a-z0-9-]`, with a
numeric suffix on collision). Discovery maps a working directory to its
directory through `projects.json`, then any `.project_root` marker naming it,
then the sha256 name. Enumeration goes the other way: the marker, the
registry entry naming the directory, then the registry root whose sha256 is
the directory name or the log's `projectHash`. A directory none of these
explain gets no working directory.

Session ids are UUIDs, or any `[A-Za-z0-9_-]` value passed to
`--session-id`; anything else is rejected, which rules out path traversal and
glob patterns. File names carry only the first eight characters, so Locate
checks each candidate's first record for the full id.

**Only main session logs are read.** Subagent logs are their own `seq` space
and nest one level deeper, so they are never listed or uploaded.

## Log records

Since April 2026 (`feat(core): migrate chat recording to JSONL streaming`) a
session is an append-only JSONL log. Each line is one of:

- the metadata record `{sessionId, projectHash, startTime, lastUpdated,
  kind, directories?}`, always the first line;
- a message record `{id, timestamp, type, content, displayContent?, ...}`
  where `type` is `user`, `gemini`, `info`, `warning`, or `error`, and `gemini`
  messages add `toolCalls`, `thoughts`, `tokens`, and `model`;
- `{"$set": {...}}`, a metadata update (`lastUpdated`, `summary`,
  `memoryScratchpad`, `directories`, or a resumed `sessionId`);
- `{"$patch": {id?, content?, toolCalls?, updates?, removeIds?, orderIds?}}`,
  which rewrites earlier messages' content or tool results, removes messages,
  or reorders them;
- `{"$rewindTo": "<message id>"}`, which drops that message and everything
  after it.

**A message id can appear on several lines.** Gemini CLI writes the whole
message again when it gains token counts (`recordMessageTokens`) or completed
tool calls (`recordToolCalls`). Gemini CLI itself keeps the last copy.

Gemini CLI writes `content` as a string, one part, or a list of parts. Text
parts flagged `thought` are reasoning. Tool responses are sent back to the
model as a `user` message of `functionResponse` parts, and that message is
recorded too.

## Mapping

`seq` is the zero-based line index. Each line yields one event; `raw` is the
line.

| Line | Event |
| --- | --- |
| metadata record | `meta`; `ts` is `startTime` |
| `user` message, first copy, with `functionResponse` parts | `tool_result`; text is each response's `output` (or `error`, or the compact response JSON), truncated at 64 KiB; `tool_name` from the first response. Responses whose call id already produced a result are left out, and a message left with none is `meta`. |
| `user` message, first copy, starting with `<session_context>` or `<hook_context>` | `system` (Gemini CLI injects these) |
| other `user` message, first copy | `user`; text is `displayContent` when present (what was typed, before `@file` expansion), else `content` |
| `info`, `warning`, `error` message, first copy | `system` |
| `gemini` message | see below |
| repeated copy of a `user`, `info`, `warning`, or `error` message | `meta` |
| `$set` | `meta`; `ts` is its `lastUpdated` |
| `$patch`, `$rewindTo`, unknown records, undecodable lines | `meta` with empty text; undecodable lines are kept as a JSON string in `raw` |

A `gemini` line emits only what earlier copies of the message did not:

- new tool calls (by id) make it `tool_call`: text is the reply text if it
  is new, then each new call's compact `args` JSON; `tool_name` is the first
  new call's name;
- otherwise new reply text makes it `assistant`;
- otherwise a first copy with only thoughts is `meta` with the thought
  subjects and descriptions, as other readers keep thinking out of the
  conversation;
- anything else (a copy that only adds tokens) is `meta`.

Tool results inside `toolCalls[].result` are not emitted from JSONL logs,
because the `functionResponse` user message that follows carries the same
result. A call whose response was never recorded, such as one in a batch the
user declined, has its result only in `raw`.

`$patch` and `$rewindTo` are kept as `meta`. Events for messages they rewrite
or retract stay in the transcript, as the log records them. Legacy
`$set.messages` checkpoints are `meta` too, so messages that appear only
inside one are not emitted.

Every `gemini` event carries the latest model seen.

## Tokens

`tokens` is `{input, output, cached, thoughts, tool, total}` from Gemini's
usage metadata (`promptTokenCount`, `candidatesTokenCount`,
`cachedContentTokenCount`, `thoughtsTokenCount`, `toolUsePromptTokenCount`).
The prompt count includes cached tokens, so events carry `input = input -
cached + tool`, `output = output + thoughts`, and `cache = cached`, which add
up to `total`. Negative counts are clamped to zero. Counts are attached to the
first copy of a message that has them and never again.

## Legacy `.json` files

Releases before April 2026 rewrote one JSON document per session,
`session-<time>-<id prefix>.json`, holding the metadata fields and a
`messages` array. Gemini CLI migrates one to a `.jsonl` beside it when the
session is resumed; the `.jsonl` then wins in Locate, Enumerate, and
discovery.

These files are read whole, up to 64 MiB. `seq` is the message index and `raw`
is the compact message. They record tool results only on the call, so a
message's `tool_call` text also holds its calls' results (function response
output, else `resultDisplay`). The returned offset is the file size; a read
from any other offset re-reads every message, and the reporter drops seqs it
already sent. Backfill's pending estimate counts lines, which overstates a
pretty-printed file.

## Discovery and state

Discovery classifies a process as Gemini CLI by its executable: `gemini`, or
node/bun running `bin/gemini`, `gemini.js`, or a script inside
`@google/gemini-cli` (Gemini CLI relaunches itself as a node child with
memory flags, which groups under the parent). The `mcp`, `extensions`,
`skills`, `hooks`, and `gemma` subcommands are not sessions. Model names such
as `gemini-2.5-pro` in another harness's flags do not match.

A runtime started with `--session-id <id>`, or `--resume <uuid>`, maps to that
session. Otherwise its working directory maps to the project directory, and
the most recently written logs there go to the newest processes first. Gemini
CLI writes a log's metadata record at startup, so a new session has a log
before its first prompt. `--resume latest` or an index resumes into the
newest log, which is the one picked.

Discovery reads the first record and the last 4 MiB. Title is the latest
`summary` in that window, model the latest `gemini` message's, and state
follows the latest message:

| Latest | State |
| --- | --- |
| `user` message (prompt or tool response) | `ACTIVE` |
| `gemini` message with tool calls, or without reply text | `ACTIVE` |
| `gemini` message with reply text | `WAITING` |
| `error` message, or `$rewindTo` | `WAITING` |
| `info`, `warning`, updates | unchanged |

Gemini CLI records a reply when its stream ends and tool calls only once
they complete. A reply whose text precedes tool calls therefore reads as
`WAITING` while the calls run, and a call waiting for approval reads as
`ACTIVE`: `NEEDS_INPUT` is never reported.

## Files touched

`transcript.TouchedFiles` reads `toolCalls` of `gemini` message records named
`replace` or `write_file` (`args.file_path`). Repeated copies of a message
list the same paths again. Shell commands are not file changes.

## Not covered

- Token usage of the Usage tab: Gemini CLI is not one of its providers.
- Subagent logs, as above.
- `--session-file` runs that load a session from an arbitrary JSON file.
