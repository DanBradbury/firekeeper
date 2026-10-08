# Firekeeper dashboard API (v1)

The dashboard server (`firekeeper serve`) accepts session metadata and
transcript events from reporters and serves them to dashboard clients. This
document describes the v1 HTTP API as implemented in `internal/server/api`.
The contract it follows lives in `AGENTS.md` under "Reporting and dashboard".

- Default listen address: `127.0.0.1:7777`.
- Authentication: bearer tokens or browser sessions, each tied to one
  account. A server with no tokens and no accounts is open, which is only
  acceptable on loopback. See [Accounts and tenancy](#accounts-and-tenancy).
- Request and response bodies are JSON (`Content-Type: application/json`),
  except `/v1/stream`, which is `text/event-stream`.
- Timestamps are RFC 3339 strings in UTC, or `null` when unknown.

## Errors

Every error is a JSON object with a standard HTTP status:

```json
{ "error": "human-readable message", "code": "machine_readable_code" }
```

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | Malformed JSON, query parameter, or `uid`. |
| 401 | `unauthorized` | Missing, malformed, expired or revoked credentials. |
| 401 | `invalid_credentials` | Wrong email or password at login. |
| 403 | `forbidden` | The token's scope does not permit the request. |
| 403 | `session_required` | Token management or link approval was tried without a signed-in browser session. |
| 403 | `csrf_failed` | A cookie-authenticated write without a valid `X-CSRF-Token`, or from another origin. |
| 403 | `signup_closed`, `invite_required`, `invite_invalid` | Signup refused; see below. |
| 409 | `email_taken` | An account with that email exists. |
| 409 | `accounts_required` | `POST /v1/link/start` on a server with no accounts, so nobody could approve. |
| 404 | `link_invalid` | A link code that is unknown, expired, or already used. |
| 410 | `link_expired` | A device code polled after its 10 minutes. |
| 415 | `unsupported_media_type` | Login or signup without a JSON `Content-Type`. |
| 429 | `rate_limited` | Too many failed attempts from this IP or for this email, or too many ingest requests from this account. `Retry-After` says how many seconds to wait. |
| 400 | `invalid_request` | Well-formed body that breaks a rule: missing `machine.id` or `session_id`, unknown provider, role, or state, an invalid cursor, or an event whose `machine_id`/`session_id` does not match its batch. |
| 404 | `not_found` | Unknown session. |
| 413 | `payload_too_large` | Body over 5 MiB. |
| 413 | `too_many_events` | More than 500 events in one ingest request. |
| 413 | `storage_limit` | The request would take the account past its stored-bytes limit. Nothing from it was stored. |
| 413 | `session_limit` | The request would take the account past its session limit. Nothing from it was stored. |
| 500 | `internal` | Server-side failure. Details are not exposed. |
| 503 | `unavailable` | Stream requested while the server is shutting down, or too many link codes pending. |

## Accounts and tenancy

Every machine, session, event and token belongs to one account. Each request
authenticates as exactly one account, and the server filters every query on
it: lists, reads, search (`q`), usage, machines, ingest, heartbeat and the
stream. Another account's data is not merely hidden from lists; asking for it
by `uid` returns `404`, the same as a session that does not exist. Two
accounts can use the same `machine_id` and `session_id`. Each sees and writes
only its own copy, and ingest into the same ids from another account never
touches yours.

Credentials, in order of precedence:

1. `Authorization: Bearer TOKEN`. Tokens are created with
   `firekeeper serve token create [--account EMAIL]` and belong to that
   account, or by linking a machine (see [Machine linking](#machine-linking))
   or on the [Tokens](#tokens) page. Ingest tokens only call `/v1/ingest` and
   `/v1/heartbeat`; read tokens only read. Any token may also call
   `GET /v1/account` and `DELETE /v1/tokens/current`.
2. The `fk_session` cookie set by login or signup. It is `HttpOnly`,
   `SameSite=Lax`, `Secure` when the request came over HTTPS (or through a
   proxy sending `X-Forwarded-Proto: https`), and lasts 14 days from login.
   Browser sessions are read-only for transcript data, but they manage the
   account's tokens and approve machine links. Every non-GET request made with the cookie
   must also send the session's `X-CSRF-Token` and, when the browser sends an
   `Origin` header, it must match the host. Logging out, expiry, or disabling
   the account also ends an open `/v1/stream`.

**Single-user mode.** While no token and no account exists (apart from the
built-in `default` account, which has no email or password and cannot sign
in), no credentials are needed and every request acts as `default`. Data and
tokens from before accounts existed belong to `default`. Creating the first
account or token turns authentication on; `default`'s tokens keep working.

Passwords are stored as argon2id hashes. Login and signup are rate limited
per IP and per email. A failed login does not reveal whether the email has an
account.

### `POST /v1/auth/signup`

```json
{ "email": "you@example.com", "password": "at least 10 characters", "invite_code": "fki_..." }
```

Who may sign up is set by `firekeeper serve --signup`:

| Mode | Behavior |
| --- | --- |
| `closed` (default) | Always `403 signup_closed`. |
| `invite` | Needs an unused, unexpired `invite_code` (`403 invite_required` / `invite_invalid`). Each code works once. Create one with `firekeeper serve invite create`. |
| `open` | Anyone who can reach the server. |

Email is lowercased and must be a bare address. Passwords must be 10 to 256
characters. Response `201`, plus the session cookie:

```json
{ "account": { "id": "9f2c...", "email": "you@example.com" }, "csrf_token": "..." }
```

Signup is limited to 5 attempts per IP and 5 per email per minute, successful
or not. The body must be JSON.

### `POST /v1/auth/login`

Body `{ "email": "...", "password": "..." }`. Response `200` with the same body
and cookie as signup. Ten failed attempts per IP, or ten for one email, in a
minute lock that IP or email out for the rest of the minute (`429`).

### `POST /v1/auth/logout`

Deletes the browser session and clears the cookie. Needs the cookie and
`X-CSRF-Token`. Response `200`: `{ "ok": true }`. Callers not using a browser
session get `400`.

### `GET /v1/account`

```json
{
  "id": "9f2c...", "email": "you@example.com", "single_user": false, "csrf_token": "...",
  "usage": { "machines": 1, "sessions": 12, "events": 3400, "tokens": 1, "stored_bytes": 8400000 },
  "limits": { "max_bytes": 1073741824, "max_sessions": 5000, "ingest_per_minute": 120 }
}
```

`csrf_token` appears only for browser sessions, so a page can recover it after
a reload. In single-user mode the account is `{"id": "default", "email": "",
"single_user": true}`. Any valid credential may call this, ingest tokens
included. For a bearer token the response also carries a `token` object, the
same fields as in [Tokens](#tokens) without a secret, which is how
`firekeeper whoami` finds the machine name. `usage` is what the account stores now (`tokens`
counts active ones); `limits` are the server's per-account limits, where `0`
means no limit. See [Limits](#limits).

### `DELETE /v1/account`

Permanently deletes the caller's account and everything it owns: its
tokens, browser sessions, machines, sessions, changed-file records and events,
including their search index entries. Invites the account issued or used are
removed too (a used invite stays unusable). Other accounts are unaffected, and
the email can be registered again.

```json
{ "confirm": "you@example.com" }
```

`confirm` must be the account's email (case-insensitive). The request needs a
browser session and its `X-CSRF-Token`; bearer tokens, including read tokens,
get `403`, and so does the built-in `default` account, which has no owner to
sign in as. A wrong or missing `confirm` is `400 invalid_request`. Response
`200`: `{ "deleted": true }`, with the session cookie cleared. It cannot be
undone. The operator can do the same from the server with
`firekeeper serve admin delete-account`.

### `GET /v1/account/export`

Streams everything stored for the caller's account as JSON Lines
(`application/x-ndjson`, sent as an attachment named `firekeeper-export.jsonl`).
It works with a browser session or a read token. Each line is a JSON object
with a `type`:

| `type` | Fields |
| --- | --- |
| `account` | `id`, `email`, `created_at`. Always the first line. |
| `machine` | The [Machine](#machine) fields plus `last_heartbeat_at` and `session_count`. |
| `token` | `id`, `name`, `scope`, `machine_id`, `created_at`, `revoked_at`. Metadata only: secrets are never stored, and hashes are not exported. |
| `session` | The [Session](#session) fields, without `files`. |
| `file` | `machine_id`, `session_id`, and the changed-file fields from `GET /v1/sessions/{uid}`. |
| `event` | The [Event](#event) object, as `GET /v1/sessions/{uid}/events` returns it. |

Sessions follow the account's machines and tokens, in `(machine_id,
session_id)` order. Each is followed by its files and then its events in `seq`
order. The export is not a snapshot: data ingested while it runs may or may
not be included. If it fails after the first byte, the last line is
`{"type":"error", ...}` and the export is incomplete.

## Limits

`firekeeper serve` limits each account so one cannot fill the disk or flood
ingest. Set them with `--max-bytes`, `--max-sessions` and
`--max-ingest-per-minute`; `0` turns one off.

| Limit | Default | Counts | When exceeded |
| --- | --- | --- | --- |
| Stored bytes | 1 GiB | Newly stored event `text` plus `raw`, in bytes | `413 storage_limit` |
| Sessions | 5000 | Stored sessions | `413 session_limit` |
| Ingest requests | 120 per minute | `POST /v1/ingest` requests in a rolling minute, accepted or not | `429 rate_limited` with `Retry-After` |

An over-limit request writes nothing: the whole batch is rolled back, so a
reporter can retry it later without losing or duplicating events. Only newly
stored data counts, so re-sending what the account already holds always
succeeds, and an account that is over a limit because it was lowered can still
update the sessions it has. The built-in `default` account (single-user data)
is never limited. Heartbeats are not rate limited.

## Machine linking

`firekeeper login --server URL` connects a machine without anyone copying a
secret, using a device-code flow. Codes expire after 10 minutes and work once.

1. The machine calls `POST /v1/link/start` with its machine id. It gets a long
   `device_code` it keeps, and a short `user_code` (`XXXX-XXXX`, from an
   alphabet without look-alike characters) it shows.
2. A person signed in to the dashboard opens `/link` (the CLI prints the URL,
   with the code and a suggested machine name in the URL fragment, which
   browsers never send to the server), checks that the code matches the
   terminal, names the machine, and approves with `POST /v1/link/approve`.
3. The machine polls `POST /v1/link/poll` with its device code every
   `interval` seconds. The first poll after approval creates an ingest token
   for the approving account, named for the machine and bound to its id, and
   returns it. The token is created at that poll, so only the holder of the
   device code ever sees it; it is never stored in the clear.

Only hashes of the codes are stored. A reused device code, or a user code that
was already approved (by anyone, so a second account cannot take it over), is
refused. Starting a link and polling need no credentials and are limited per
IP; approval is limited per IP and per account (ten wrong codes a minute lock
the approver out for the rest of the minute).

### `POST /v1/link/start`

Body `{ "machine_id": "..." }` (1 to 128 printable characters), JSON only.
Response `201`:

```json
{ "device_code": "fkd_...", "user_code": "ABCD-EFGH", "expires_in": 600, "interval": 2 }
```

Ten starts per IP per minute (`429`). `409 accounts_required` on a server
that has no accounts.

### `POST /v1/link/poll`

Body `{ "device_code": "fkd_..." }`. While nobody has approved, `200`
`{ "status": "pending" }`. After approval, once:

```json
{
  "status": "approved",
  "token": "fk_...",
  "token_id": "3f9a1c2e",
  "account": { "id": "9f2c...", "email": "you@example.com" },
  "machine": { "id": "<machine id>", "name": "Alice's laptop" }
}
```

`410 link_expired` after 10 minutes, whether or not it was approved. An
unknown or already collected code is `404 link_invalid`; ten of those from one
IP in a minute lock the poller out (`429`).

### `POST /v1/link/approve`

Body `{ "user_code": "ABCD-EFGH", "machine_name": "Alice's laptop" }`. Needs a
browser session and its `X-CSRF-Token`; no bearer token may approve.
`user_code` ignores case and separators. `machine_name` is 1 to 64 printable
characters, and a bad one is a `400` that does not use up the code. Response
`200`: `{ "ok": true, "machine": { "id": "...", "name": "..." } }`.

## Tokens

The Tokens page of the web UI (`/#/tokens`) manages the signed-in account's
tokens through these endpoints. All but the last need a browser session
(`403 session_required` otherwise), and a token of another account is
`404`, never listed.

### `GET /v1/tokens`

```json
{ "tokens": [ {
  "id": "3f9a1c2e", "name": "Alice's laptop", "scope": "ingest",
  "machine_id": "<machine id>", "created_at": "2026-10-08T17:52:53Z",
  "last_used_at": "2026-10-08T18:03:00Z", "revoked_at": null
} ] }
```

Newest first, revoked tokens included. `last_used_at` is accurate to a minute
and `null` for a token that has never authenticated a request. The secret and
its hash are never returned.

### `POST /v1/tokens`

Body `{ "name": "ci-runner", "scope": "ingest", "machine_id": "ci-box" }`.
`name` is 1 to 64 printable characters; `scope` is `ingest` or `read`;
`machine_id` is required for `ingest` and ignored for `read`. Response `201`
with the fields above plus `"token": "fk_..."`, the secret. It is shown here
once and cannot be recovered. The response is `Cache-Control: no-store`.

### `DELETE /v1/tokens/{id}`

Revokes an active token of the account. `200 { "ok": true }`, or `404` if
there is no such active token. The token is rejected from its next request.

### `DELETE /v1/tokens/current`

Authenticated with the token to revoke, of any scope; this is how
`firekeeper logout` cleans up. `200 { "ok": true }`. A browser session has no
token and gets `400`.

## Shared objects

### Machine

```json
{ "id": "string", "name": "string", "hostname": "string", "os": "string", "version": "string" }
```

`id` is required on every write. On upsert, empty strings keep the stored
values.

### Event

One normalized transcript record. The JSON shape is pinned by
`docs/event.schema.json`.

```json
{
  "machine_id": "string",
  "session_id": "string",
  "provider": "codex | copilot | kimi | claude",
  "seq": 0,
  "ts": "RFC3339 or null",
  "role": "user | assistant | tool_call | tool_result | system | meta",
  "text": "string",
  "tool_name": "string or null",
  "model": "string or null",
  "tokens": { "input": 0, "output": 0, "cache": 0 },
  "raw": {}
}
```

Events are keyed by `(machine_id, session_id, seq)`. `seq` must not be
negative.

### Session

Returned by the read endpoints:

```json
{
  "uid": "m1:0199-abc",
  "machine_id": "m1",
  "session_id": "0199-abc",
  "provider": "codex",
  "cwd": "/path/to/repo",
  "project": "repo",
  "branch": "main",
  "commit": "4f5ef2f0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6",
  "model": "gpt-5",
  "state": "ACTIVE | WAITING | NEEDS_INPUT | ENDED | UNKNOWN",
  "title": "string",
  "started_at": "RFC3339 or null",
  "last_activity_at": "RFC3339 or null",
  "event_count": 42,
  "tokens": { "input": 0, "output": 0, "cache": 0 }
}
```

`event_count` and `tokens` are always recomputed from stored events, never
taken from a request. `state` describes the latest observable provider event.
It is a best-effort reading, not guaranteed ground truth.

### Session uid

A session's `uid` is `<machine_id>:<session_id>`. Escape it when you put it in
a path (for example, `m1:a%2Fb` for session `a/b`). The server splits the uid
at its first `:`, so a machine id must not contain `:`. A session id may.

## Write endpoints

### `POST /v1/ingest`

Upserts session metadata and appends events. Each request runs in a single
transaction.

```json
{
  "machine": { "id": "m1", "name": "laptop", "hostname": "laptop.local", "os": "darwin", "version": "0.4.0" },
  "sessions": [
    {
      "meta": {
        "session_id": "0199-abc", "provider": "codex", "cwd": "/repo", "project": "repo",
        "branch": "main", "commit": "4f5ef2f…", "model": "gpt-5", "state": "ACTIVE", "title": "Fix parser",
        "started_at": "2026-10-07T12:00:00Z", "last_activity_at": "2026-10-07T12:05:00Z"
      },
      "events": [ { "provider": "codex", "seq": 0, "role": "user", "text": "...", "tokens": {"input": 0, "output": 0, "cache": 0}, "raw": {} } ]
    }
  ]
}
```

- Limits: at most 500 events across all sessions, and at most 5 MiB of body.
  A request over either limit is rejected with 413. Reporters split larger
  batches.
- Idempotency: a stored event key is never overwritten. Re-sending an event
  counts it as a duplicate.
- Metadata: empty strings keep the stored values. `started_at` and `commit`
  (the commit checked out when the session started; optional) keep their
  first value. `last_activity_at` only moves forward. An empty `state` keeps the
  stored state, or `UNKNOWN` for a new session.
- Ingest also counts as a heartbeat for the machine.
- Files: newly stored events are scanned for file-changing tool calls (see
  `GET /v1/sessions/{uid}`). Re-sent events are not scanned again.

Response `200`:

```json
{ "accepted": 3, "duplicates": 0 }
```

### `POST /v1/heartbeat`

Marks the machine online and updates session state.

```json
{
  "machine": { "id": "m1" },
  "sessions": [ { "session_id": "0199-abc", "state": "WAITING", "last_activity_at": "2026-10-07T12:06:00Z" } ]
}
```

`state` is required and must be a valid state. Sessions the server has never
ingested are ignored. `last_activity_at` only moves forward.

Response `200`: `{ "ok": true }`.

## Read endpoints

### `GET /v1/sessions`

Lists sessions, newest `last_activity_at` first. Sessions with no activity
time come last. Ties are broken by `machine_id`, then `session_id` (both
descending), so the order is stable.

| Parameter | Meaning |
| --- | --- |
| `machine` | Exact machine id. |
| `provider` | Exact provider. Must be a valid provider. |
| `project` | Exact project name. |
| `state` | Exact state. Must be a valid state. |
| `q` | Case-insensitive substring of the title, or a full-text phrase match on event text. FTS operators are not interpreted. |
| `limit` | Page size, default 50. Values above 500 are clamped to 500, and values below 1 are rejected. |
| `cursor` | The `next_cursor` from the previous page. Treat it as opaque. |

Filters combine with AND. Response `200`:

```json
{ "sessions": [ /* Session */ ], "next_cursor": "eyJr..." }
```

`next_cursor` is omitted on the last page. Pagination is keyset-based, so
pages neither skip nor repeat rows when sessions are added. A session whose
activity changes while you are paging can still move to a page you have
already read.

### `GET /v1/sessions/{uid}`

Returns one Session plus `files`, the files its tool calls changed, ordered
by path. A malformed uid gets 400, and an unknown session gets 404.

```json
{
  "uid": "m1:0199-abc",
  "...": "the Session fields above",
  "files": [
    { "path": "/etc/hosts", "absolute": true, "first_seq": 9, "last_seq": 9, "changes": 1 },
    { "path": "src/main.go", "absolute": false, "first_seq": 4, "last_seq": 12, "changes": 3,
      "url": "https://github.com/me/repo/blob/4f5ef2f…/src/main.go" }
  ]
}
```

- `path` is relative to the session's `cwd`, or absolute (`absolute: true`)
  when the file is outside it. Paths come from events that were redacted
  before upload, so they carry the same `~` and `[REDACTED:...]` rewrites.
- `first_seq` and `last_seq` are the first and last events that changed the
  file; `changes` counts those events.
- `url` is present only when the server has a file link template
  (`serve --file-link`), the path is relative and unredacted, and every
  placeholder the template uses has a value.
- Detection reads Claude Code, Codex, and Copilot tool calls. It is best
  effort: files changed by arbitrary shell commands are not listed.
- `files` is always an array, and is not included by `GET /v1/sessions`.

### `GET /v1/sessions/{uid}/events`

Returns a session's events in `seq` order.

| Parameter | Meaning |
| --- | --- |
| `after_seq` | Return events with `seq` greater than this value. The default, `-1`, starts from the first event. |
| `limit` | Default 200. Values above 1000 are clamped to 1000, and values below 1 are rejected. |

Response `200`:

```json
{ "events": [ /* Event */ ], "has_more": false }
```

To page through, pass the last returned `seq` as `after_seq`. Unknown sessions
get 404.

### `GET /v1/machines`

Lists the account's machines ordered by `id`:

```json
{
  "machines": [
    { "id": "m1", "name": "laptop", "hostname": "laptop.local", "os": "darwin", "version": "0.4.0",
      "last_heartbeat_at": "2026-10-07T12:06:00Z", "session_count": 4,
      "state_counts": { "ACTIVE": 1, "ENDED": 3 }, "last_activity_at": "2026-10-07T12:05:12Z" }
  ]
}
```

`state_counts` maps each session state present on the machine to its count
(`{}` when it has none), and `last_activity_at` is its newest session
activity (`null` when it has none). Clients decide whether a machine is online
from `last_heartbeat_at`; the dashboard treats 90 seconds without a heartbeat
as offline.

### `GET /v1/usage`

Token sums from stored events, for usage charts.

| Parameter | Meaning |
| --- | --- |
| `from` | Start of the range: a `YYYY-MM-DD` date (start of that UTC day) or an RFC 3339 time. Default: 30 days before `to`. |
| `to` | End of the range: a `YYYY-MM-DD` date includes that whole UTC day; an RFC 3339 time is exclusive. Default: the end of today (UTC). |
| `group_by` | Comma-separated keys from `day`, `model`, `provider`, `machine`, `project`, each at most once. Empty returns only totals. |

The range must be non-empty and at most 400 days. `day` is the event's UTC
date. `model` is the event's model, falling back to its session's model, and
may be `""` when neither is known. `machine` is the machine id, and `project`
is the session's project.

Only events with a timestamp count; events with `ts: null` cannot be placed
in a range and are left out, so totals here can be lower than a session's
`tokens`. Each counter is summed as stored, so whether `input` already
includes `cache` depends on the provider.

```json
{
  "from": "2026-10-01T00:00:00Z",
  "to": "2026-10-05T00:00:00Z",
  "group_by": ["day", "model"],
  "priced": true,
  "rows": [
    { "group": { "day": "2026-10-01", "model": "gpt-5" },
      "tokens": { "input": 100, "output": 10, "cache": 50 }, "cost": 0.000205 },
    { "group": { "day": "2026-10-02", "model": "o3" },
      "tokens": { "input": 1000, "output": 100, "cache": 0 }, "cost": 0, "unpriced_tokens": 1100 }
  ],
  "totals": { "tokens": { "input": 1100, "output": 110, "cache": 50 }, "cost": 0.000205, "unpriced_tokens": 1100 }
}
```

Rows are sorted by `day` ascending when grouped by day, then by total tokens
descending. Days with no usage are omitted; clients fill gaps. An empty range
returns `"rows": []` with zero totals.

Cost appears only when the server has a per-model price table (per million
tokens of each kind). Firekeeper ships no prices. Then `priced` is `true`,
and every row and the totals carry `cost`, computed from priced models only.
`unpriced_tokens`, when present, counts the tokens from models with no price
that `cost` leaves out. Without a table, `priced` is `false` and `cost` is
never sent.

## Live stream

### `GET /v1/stream`

A server-sent events stream of store changes. The server sends
`: connected` when the stream opens and `: keepalive` every 15 seconds.

```
event: event.appended
data: {"uid":"m1:0199-abc","machine_id":"m1","session_id":"0199-abc","seq":41}
```

| Event | Data | Meaning |
| --- | --- | --- |
| `session.updated` | `uid`, `machine_id`, `session_id` | A session's metadata, state, or totals may have changed. |
| `event.appended` | `uid`, `machine_id`, `session_id`, `seq` | New events were stored. `seq` is the highest new seq in that ingest. Seqs can arrive out of order, so it is not a high-water mark. |
| `machine.status` | `machine_id` | The machine sent an ingest or heartbeat. |

Events are notifications, not data. Clients refetch with the read endpoints,
for example `GET /v1/sessions/{uid}/events?after_seq=<last seq you hold>`.

**Delivery is best effort.** The server drops notifications rather than slow
down ingest:

- If a client falls more than 256 notifications behind, the server closes
  its connection.
- If the server-wide notification buffer fills, notifications are discarded
  for every client.

**No resume.** Events carry no `id:` and the server ignores `Last-Event-ID`,
because notifications are not stored. After any reconnect, clients refetch the
session list and the events of any open session, then continue from the
stream. A dropped connection is a cue to resync, not an error.

The stream also ends when the server shuts down. Because streams are
long-lived, a graceful shutdown should close the store, which ends every
stream, before waiting on in-flight requests.
