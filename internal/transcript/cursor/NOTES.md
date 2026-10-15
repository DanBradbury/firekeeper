# Cursor CLI transcript source

Cursor does not publish a storage format for its agent CLI (`cursor-agent`,
also installed as `agent`). These notes come from Cursor's CLI configuration
docs (October 2026) and from open-source tools that read the same files:
`jazzyalex/agent-sessions` (`CursorSessionParser.swift`,
`CursorSessionDiscovery.swift`, `CursorChatMetaReader.swift`, and its
`schema_drift.jsonl` fixture) and issues and pull requests in other session
indexers. No local Cursor data was inspected. Every fixture here is
synthetic. The shapes are Cursor's internal persistence format, not a
published API, and they change between releases.

## Layout

```text
$CURSOR_CONFIG_DIR                       (default ~/.cursor)
  projects/<encoded workspace path>/
    agent-transcripts/<id>/<id>.jsonl    one main transcript per chat
    agent-transcripts/<id>.jsonl         the same, as some releases wrote it
    agent-transcripts/<id>/subagents/<subagent id>.jsonl
  chats/<md5 of workspace path>/<id>/store.db   one SQLite database per chat
```

Ids are UUIDs; anything else is rejected, which rules out path traversal and
glob patterns.

Cursor's docs only say `CURSOR_CONFIG_DIR` moves `cli-config.json`. Firekeeper
assumes chats and transcripts move with it, as they live beside that file. If
a release keeps them in `~/.cursor` regardless, unset the variable or point
it at `~/.cursor`.

The Cursor editor's Agents window writes `agent-transcripts` in the same
place, so `backfill` imports those sessions as well. Its side-panel chats live
in the editor's own databases and are not read.

### Project directory names

A project directory is named after the workspace path with path separators
replaced by `-` and the leading one dropped: `/Users/me/app` becomes
`Users-me-app`. The encoding is lossy (`my-app` and `my/app` look the same),
and how Cursor treats other punctuation is not documented, so both sides are
compared as a key: lowercase ASCII letters and digits, in runs joined by a
single `-` (`session.CursorProjectKey`).

- Discovery goes from a runtime's working directory to every project
  directory with the same key.
- Enumeration goes the other way: it walks the filesystem from `/`, following
  only directories whose key is a prefix of the name's key, and accepts the
  first existing directory whose key is equal (`session.CursorWorkspace`).
  Nothing is guessed. When the workspace no longer exists, the session gets no
  working directory, and the reporter skips sessions with no working
  directory, because `.firekeeper-ignore` and `exclude` cannot be checked
  without one.

The chat database directory is the md5 of the exact workspace path, so it is
found directly once the path is known, and by id (`chats/*/<id>/store.db`)
otherwise.

## Transcript records

Each line is one JSON object:

- `{"role": "user" | "assistant", "message": {"content": [...]}}` with
  Anthropic-style content blocks: `text`, `thinking` (`thinking`),
  `tool_use` (`id`, `name`, `input`), and `tool_result` (`tool_use_id`,
  `name`, `content` as a string or text blocks). Some releases spell the tool
  blocks `tool-call` and `tool-result`. `content` can also be a plain string.
- `{"type": "turn_ended", "status": "...", "error"?: "..."}` at the end of a
  turn.

User text holds context Cursor adds (for example `<user_info>`) followed by
the typed prompt inside `<user_query>` tags.

Records carry **no timestamps, token counts, or session id**, and usually no
model. A top-level `model` is read when present.

## Mapping to events

`seq` is the zero-based line index. Offsets are byte positions after a
newline; a trailing line without one is still being written and waits for
the next read. A read from an offset first scans earlier lines to recover
tool names by `tool_use` id.

| Record | Role | Text |
| --- | --- | --- |
| user, text blocks or string | `user` | the `<user_query>` contents, or the whole text without the tags |
| user with a `tool_result` block | `tool_result` | result text, truncated past 64 KiB with `[truncated N bytes]` |
| assistant with a `tool_use` block | `tool_call` | any text, then each call's `input` as compact JSON |
| assistant with text | `assistant` | the text blocks, including unknown block types that carry `text` |
| assistant with only thinking | `meta` | the thinking |
| `system` role | `system` | the message text |
| `turn_ended` with `status: "error"` | `system` | the error |
| anything else, including unparseable lines | `meta` | empty |

`tool_name` is the block's `name` (or `tool`), falling back to the tool name
of the call a result's `tool_use_id` names. `ts` is always null and `tokens`
are always zero. Unparseable lines keep their text as a JSON string in `raw`.

## Chat database

`store.db` has tables `meta (key, value)` and `blobs (id, data)`. Row `"0"`
of `meta` is JSON, hex-encoded: `agentId` (the chat id), `name` (the title),
`createdAt` (Unix milliseconds), `lastUsedModel`, `mode`, and
`latestRootBlobId`. Firekeeper reads only that row, with
`sqlite3 -readonly -json`, and checks `agentId` against the id it expects.
`lastUsedModel` of `default` means Cursor's automatic choice and is dropped.
The `blobs` table holds the conversation as content-addressed protobuf and
is never read.

Without `sqlite3`, sessions still appear and upload; they lack a title,
model, and start time.

## Discovery and state

- A runtime is `cursor-agent` (or a shell running the `cursor-agent`
  launcher), or the bundled `node` running `index.js` under
  `.../cursor-agent/versions/<version>/`. The bare `agent` name is too
  generic to match, but its node child is still found. Helper subcommands
  (`login`, `logout`, `status`, `whoami`, `update`, `upgrade`, `mcp`,
  `about`, `help`, `create-chat`, and the shell-integration ones) are not
  sessions.
- `--resume <id>` names the chat. Otherwise the runtime's working directory
  picks its chats, from both transcripts and chat databases, since a
  database appears when a chat starts and a transcript only once it has
  messages. The newest chats go to the newest processes (higher PIDs).
  Several runtimes in one directory may be matched to the wrong chat, and a
  runtime started with `--workspace` elsewhere is matched by its process
  directory.
- State follows the last record: a prompt or tool result is `ACTIVE`, a reply
  ending in a tool call is `ACTIVE`, a reply ending in text or a
  `turn_ended` record is `WAITING`. A chat with no transcript yet is
  `UNKNOWN`. Approval prompts are not recorded, so `NEEDS INPUT` is never
  reported. Last activity is the newer of the transcript's and the chat
  database's modification times.

## Known gaps

- No timestamps or token counts: the dashboard shows no per-event times and
  no Cursor token usage, and the Usage tab has no Cursor view.
- Files an edit tool changed are not listed from tool calls, since the tool
  names and input fields are not documented; the Git-based file list still
  works.
- Subagent transcripts have their own `seq` space and are never listed or
  uploaded.
