# Claude Code on the web (cloud sessions)

Findings from the internal API behind claude.ai/code, inspected October 2026
for `backfill --claude-cloud`. The API is undocumented. These are the shapes
seen from one account, not a published contract, and Anthropic can change them
without notice. Values below are shapes only; no transcript data is recorded
here.

## Access

- Base URL `https://api.anthropic.com/v1`, read-only `GET`s.
- Headers: `Authorization: Bearer <Claude Code OAuth access token>`,
  `anthropic-version: 2023-06-01`, `anthropic-beta: ccr-byoc-2025-07-29`,
  `x-organization-uuid: <org id>`. Without the beta header `/sessions` is a 404.
- The token is the `claudeAiOauth.accessToken` of the macOS keychain item
  `Claude Code-credentials`; the organization id is `oauthAccount.organizationUuid`
  in `~/.claude.json`. Both are read, never written.
- The older `/session_ingress/session/<id>` endpoint, which community tools
  used, now answers 404.

## Endpoints

| Request | Reply |
| --- | --- |
| `GET /sessions` | `{data: [...], first_id, last_id, has_more}`. 29 sessions came back in one page with `has_more: false`. `?limit=1` was a 400, so the client sends no limit and follows `after_id` when `has_more` is true. |
| `GET /sessions/{id}` | One session object, same shape as a list item. Not used. |
| `GET /sessions/{id}/events?limit=N&after_id=<uuid>` | `{data: [event...], first_id, last_id, has_more}`. Oldest first. `limit` up to 1000 works (default 50). `after_id` continues after that event's `uuid`; `before_id` pages backward. A 1,545-event session paged completely with unique uuids. |

## Session object (fields used)

`id` (`session_01...`, not a UUID), `title`, `session_status`, `created_at`,
`updated_at`, `session_context.{cwd, model}` (cwd is empty in practice),
`session_context.sources[].url` (a `git_repository` with a GitHub URL, two
path segments: the repository),
`session_context.outcomes[].git_info.{repo, branches[]}` (repository as
`owner/name`, and branches), `external_metadata.current_branches` (a map with
an empty-string key to the branch),
`external_metadata.last_served_model`, `post_turn_summary.title`,
`configured_model`. Others seen and unused: `external_metadata.usage`
(tokens and cost), `session_url`, `tags`, `environment_id`, `origin`.
Some sessions lack `external_metadata`.

## Events

Each event is a flat object with `type`, `uuid`, `created_at`, `session_id`,
plus type-specific fields. Types seen, by frequency: `assistant`, `system`,
`user`, `control_request`, `control_response`, `env_manager_log`,
`rate_limit_event`, `result`, `active_goal`, `autocompact_state`,
`prompt_suggestion`, `control_cancel_request`.

- `user` and `assistant` carry `message` in the Anthropic messages shape
  (`role`, `content` string or blocks, and for assistant `id`, `model`, `usage`)
  plus `parent_tool_use_id` (null, or the tool call that spawned a sub-agent).
  They are the conversation, and what the importer keeps.
- Tool results are `user` events with `tool_result` blocks and a
  `tool_use_result` projection.
- The user's own prompts arrive as `user` events with string content and no
  `timestamp` (they carry `server_received_wall_ms`); the importer fills
  `timestamp` from `created_at`.
- Everything else is session plumbing and is dropped.
- There is no `cwd`, `gitBranch` or `sessionId` on events; those come from the
  list call.

## Mapping

Kept events are written as JSONL lines, one per event with the event's own
fields (`timestamp` filled, `isSidechain: true` when `parent_tool_use_id` is
set), to `~/.firekeeper/cloud/claude/<id>.jsonl`, with metadata in
`<id>.json`. `internal/transcript/claude` parses them (`claude.ReadAs`, since
the file name is not a UUID). `seq` is the line index, so a transcript is only
ever rewritten with its earlier lines unchanged, which keeps saved offsets
valid.

## Known gaps

- macOS only (keychain). Other platforms get a clear error.
- `CLAUDE_CONFIG_DIR` is not honored: the keychain item and `~/.claude.json`
  of the default install are used.
- `session_context.cwd` is an empty string on every session seen (29 of 29),
  and the reporter excludes sessions without a working directory. The
  importer therefore gives each session the virtual directory
  `/claude-cloud/<owner>/<repo>` (project = repo name). A real `cwd`, if the
  API ever sends one, wins.
- Exclusion by directory or remote cannot match cloud paths.
- Sub-agent messages are kept as meta events, not as a separate transcript.
