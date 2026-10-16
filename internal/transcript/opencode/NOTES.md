# OpenCode transcript source

Findings from OpenCode's source (`anomalyco/opencode`, `dev` branch,
October 2026): the drizzle tables in `packages/core/src/session/sql.ts`, the
message schema in `packages/schema/src/session-message.ts`, the database path
in `packages/cli/src/database-path.ts`, and the migrations under
`packages/core/src/database/migration/`. No local OpenCode data was inspected,
so the fixture is built from those definitions. These are the shapes the code
writes, not a published format. OpenCode changes them often (more than forty
migrations in 2026), so check here first when OpenCode output looks wrong.

## Where sessions live

All of OpenCode's state is in one SQLite database:

- `$OPENCODE_DB` when set, as an absolute path or relative to the data
  directory. `:memory:` stores nothing, and Firekeeper then finds no sessions.
- Otherwise `opencode.db` in `$XDG_DATA_HOME/opencode`, which defaults to
  `~/.local/share/opencode`, on macOS too.
- Builds from a non-release channel write `opencode-<channel>.db` instead.
  Firekeeper does not look for those; set `OPENCODE_DB` to read one.

The database is opened with `sqlite3 -readonly -json`, never written. It runs
in WAL mode while OpenCode is open; a read-only connection still sees
committed rows in the WAL.

Because many sessions share one file, a transcript path is
`<database>#<session id>` (`session.OpenCodeTranscriptPath`). The source
implements `reader.Stater`, so the reporter and backfill ask it for a
session's size and change time instead of stat-ing that path, and keep a read
offset per session in `state.json` under that path.

## Legacy layout

OpenCode 1.19.1, among others, still writes the layout from before
`session_v2`, and Firekeeper reads it too. `session_v2` plus `session_message`
win when both layouts are present, since OpenCode copies the old tables into
the new ones on upgrade. Otherwise `session`, `message`, and `part` are used:

- `session`: same columns as `session_v2`, same meaning.
- `message`: `id`, `session_id`, `time_created`, `time_updated`, and `data`, the
  message JSON: `role` (`user` or `assistant`), `time.created`,
  `time.completed`, `modelID` (assistant) or `model.modelID` (user), `tokens`.
  It has no type or seq column. The row's `rowid`, which only grows, is the
  event `seq`, so offsets, `Stat`, and `Pending` work as described below.
- `part`: `message_id`, `data`. Part types `text`, `reasoning`, and `tool`
  (`tool`, `state.status`, `state.input`, `state.output`, `state.error` as
  a string) become the content of the event, as for the current layout. `step-start`,
  `step-finish`, `patch`, `file`, and `compaction` parts are kept only in raw.

Differences from the current layout:

- Role is `user` or `assistant`; there are no shell, synthetic, or idle
  messages. Compaction is a part, not a message.
- Without idle markers, every message except the session's last counts as
  finished; the last assistant message is finished once `time.completed` is
  set, or after an hour unchanged.
- `raw.data` is the message JSON plus derived `text`, `model`, and `content`,
  and the original part rows in `parts`. A user message's `summary` (a diff of
  touched files, up to hundreds of KiB) is left out, because `sqlite3 -json`
  takes seconds to print values that large.
- Pages are 50 messages, not 500.
- SQLite reuses the highest rowid after that message is deleted, so after a
  revert a new message can get a seq an earlier read already used. The server
  keeps the first copy, as for any reverted message.
- Malformed `data` JSON in a message or part becomes a `meta` event or is
  skipped from `content`, and does not fail the page.

## Tables read

The current tables date from the `20260804233008_loose_psylocke` migration
(August 2026). Earlier releases wrote `session`, `message`, and `part` (see "Legacy layout");
on upgrade OpenCode copies those into the tables below. A database with neither layout returns `ErrUnsupportedSchema`; Firekeeper
does not read the older tables or the JSON `storage/` directory that came
before the database.

- `session_v2`: one row per session. Read: `id` (`ses_` plus 26 letters and
  digits), `parent_id` (set for subagent sessions), `directory` (the working
  directory), `title` (may be null), `model` (JSON `{"id", "providerID",
  "variant"?}`), `tokens_input`, `tokens_output`, `tokens_reasoning`,
  `tokens_cache_read`, `tokens_cache_write`, `time_created`, `time_updated`
  (Unix milliseconds). There is no Git branch column.
- `session_message`: one row per message. Columns `id` (`msg_...`),
  `session_id`, `type`, `seq`, `time_created`, `time_updated`, and `data`, the
  message JSON without its `id` and `type`. `(session_id, seq)` is unique.

Discovery and enumeration also read the latest message's `type` and
`time_updated`, never message text.

## Seq and offsets

`seq` is OpenCode's durable event sequence number for the event that created
the message. It only grows, survives restarts, and is never reused, but it has
gaps, because most events do not create a message. Event `seq` is OpenCode's
`seq` unchanged, so it is stable on every re-read but is not a dense index.
The offset is a seq cursor: `Read(path, n)` returns messages with `seq >= n`
and the next offset is one past the last one returned. `Stat` reports one past
the highest seq as the size.

A revert deletes every message from a boundary on. The size shrinks, so the
reporter reads the session from the start again and the server drops what it
already has. Reverted messages already uploaded stay on the dashboard. New
messages get higher seqs than the deleted ones, so they never collide with
them.

## Messages still being written

OpenCode updates a message row in place while it streams. Read stops before
the first message that is not finished and returns its seq as the offset, so
the message is uploaded once, complete, and later messages wait for it:

- `assistant` is finished once `time.completed` is set.
- `shell` is finished once `status` is not `running`.
- `compaction` is finished once `status` is not `running`.

Any message before the session's latest `idle` message belongs to an ended
turn and counts as finished. So does any message unchanged for an hour,
because OpenCode leaves a message unfinished when it exits mid-turn and only
finishes it when the session resumes. If that happens after the hour, the
dashboard keeps the version read first.

## Message types

| `type` | Role | Text |
| --- | --- | --- |
| `user` | user | `text` |
| `synthetic`, `system`, `skill` | system | `text` |
| `shell` (a `!` command) | tool_result, tool `shell` | `$ <command>` and `output.output` |
| `assistant` with any `text` content | assistant | the text parts; `tool_name` is the first tool's |
| `assistant` with only tool content | tool_call | each tool's input, text output, and error message |
| `assistant` with only reasoning | meta | the reasoning |
| `compaction` completed | system | `summary` |
| `compaction` running or failed | meta | empty |
| `model-switched` | meta | empty; `model` is set |
| `agent-switched`, `location-switched`, `idle`, anything else | meta | empty |

An assistant message is one model step, with its tool calls and their results
inside `content` (`{"type": "tool", "name", "state": {"status", "input",
"content", "error"}}`). The event contract has one event per record, so a step
is one event, and a step that both replies and calls tools reads as the reply.
Tool output in such a step is in `raw` only. Tool text over 64 KiB is
truncated with a trailing `[truncated N bytes]`.

`data` that is not JSON becomes a `meta` event with `data` kept as a string in
`raw`.

`raw` is the row: `{"id", "type", "seq", "time_created", "time_updated",
"data"}`. `ts` is `data.time.created`, else the row's `time_created`.

## Tokens and model

Assistant and compaction messages carry `tokens: {input, output, reasoning,
cache: {read, write}}` once complete. OpenCode counts `input` without cached
tokens and `reasoning` apart from `output` (its own total adds all five), so
the event has `input`, `output + reasoning`, and `cache.read + cache.write`.
The model is `model.id`, without the provider.

## Changed files

`transcript.TouchedFiles` reads tool parts in assistant `raw`: `edit` and
`write` name the file in `input.path` (`filePath` in older releases), and the
`patch` tool's `patchText` is an apply_patch envelope.

## Discovery

`internal/session/opencode.go` maps running OpenCode processes to sessions:

- `opencode`, `opencode run`, and `opencode mini` run a session. Other
  subcommands, including `serve` and `service`, which run the shared
  background server, do not.
- `--session`/`-s <id>` names the session unless `--fork` is given.
- Otherwise the runtime's working directory, from `lsof`, is matched against
  `session_v2.directory` (`session.directory` in the legacy layout), and the most recently updated top-level sessions
  there are taken, newer processes first. A runtime started with a directory
  argument (`opencode ~/src/app`) from elsewhere is not matched, and several
  runtimes in one directory may get each other's sessions.
- Title: OpenCode names a session "New session - <timestamp>" until it
  generates a title. Discovery and enumeration replace that placeholder with
  the first line of the session's first user prompt (text parts, not
  synthetic ones), cut to 80 characters (`session.OpenCodeTitle`). A real
  title is never changed. The reporter redacts titles like other metadata.
- State: `WAITING` when the latest message is `idle` or there is none yet,
  `ACTIVE` after a prompt or while a reply, shell command, or compaction is in
  progress, `UNKNOWN` otherwise. Permission and question prompts are not
  visible in these tables, so `NEEDS INPUT` is never reported.

In the legacy layout the state comes from the latest `message`: a user
message or an assistant message without `time.completed` is `ACTIVE`, a
completed assistant message is `WAITING`.

Subagent sessions (`parent_id` set) are not mapped to runtimes or enumerated
for backfill.
