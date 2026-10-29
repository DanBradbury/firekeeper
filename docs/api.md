# Firekeeper dashboard API (v1)

This is the open HTTP contract between Firekeeper's reporter and a dashboard
server. The reporter in this repository (`firekeeper report`, `backfill`,
`daemon`, `login`, `whoami`, `logout`) speaks exactly this API, so any server
that implements it can receive sessions. The commercial dashboard,
`firekeeper-server`, is one such server and is not part of this repository.
Servers may offer more endpoints (reading sessions, search, a web UI); only
what a client needs to send data is specified here.

The bodies shown below are also kept as golden fixtures in the Go package
[`pkg/contract`](../pkg/contract), with validators, so that a client and a
server can test against the same examples. A test fails if this document lists
an endpoint (a `###` heading of the form `` `METHOD /path` ``) or an error code
(a row of the table below) that has no fixture.

- Default server address the client assumes: `http://127.0.0.1:7777`. A
  server that is not on this machine must be reached over `https`.
- Authentication: `Authorization: Bearer TOKEN`. Tokens are ingest-scoped and
  bound to one machine id. A server may instead run open on loopback, in which
  case it ignores the header.
- Request and response bodies are JSON (`Content-Type: application/json`).
- Timestamps are RFC 3339 strings in UTC, or `null` when unknown.

## Errors

Every error is a JSON object with a standard HTTP status:

```json
{ "error": "human-readable message", "code": "machine_readable_code" }
```

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | Malformed JSON. |
| 400 | `invalid_request` | Well-formed body that breaks a rule: missing `machine.id` or `session_id`, unknown provider, role, or state, or an event whose `machine_id`/`session_id` does not match its batch. |
| 401 | `unauthorized` | Missing, malformed, expired or revoked credentials. |
| 403 | `forbidden` | The token is bound to a different machine, or its scope does not permit the request. |
| 404 | `link_invalid` | A link code that is unknown, expired, or already used. |
| 409 | `accounts_required` | `POST /v1/link/start` on a server with nobody who could approve. |
| 410 | `link_expired` | A device code polled after its 10 minutes. |
| 413 | `payload_too_large` | Body over 5 MiB. |
| 413 | `too_many_events` | More than 500 events in one ingest request. |
| 413 | `storage_limit`, `session_limit` | The server's per-account limits would be exceeded. Nothing from the request was stored, so the client may retry later. |
| 429 | `rate_limited` | Too many requests. `Retry-After` says how many seconds to wait. |
| 500 | `internal` | Server-side failure. |
| 503 | `unavailable` | The server is shutting down or busy. |

## Machine linking

`firekeeper login --server URL` connects a machine without anyone copying a
secret, using a device-code flow. Codes expire after 10 minutes and work once.

1. The machine calls `POST /v1/link/start` with its machine id. It gets a long
   `device_code` it keeps, and a short `user_code` (`XXXX-XXXX`, from an
   alphabet without look-alike characters) it shows.
2. A person signed in to the server's web page opens `/link` (the CLI prints
   the URL, with the code and a suggested machine name in the URL fragment,
   which browsers never send to the server), checks that the code matches the
   terminal, names the machine, and approves. Approval is between the person's
   browser and the server; the client never calls it.
3. The machine polls `POST /v1/link/poll` with its device code every
   `interval` seconds. The first poll after approval creates an ingest token
   for the approving account, named for the machine and bound to its id, and
   returns it. The token is created at that poll, so only the holder of the
   device code ever sees it; it is never stored in the clear.

A reused device code is refused. Starting a link and polling need no
credentials and may be rate limited per IP.

### `POST /v1/link/start`

Body `{ "machine_id": "..." }` (1 to 128 printable characters), JSON only.
Response `201`:

```json
{ "device_code": "fkd_...", "user_code": "ABCD-EFGH", "expires_in": 600, "interval": 2 }
```

`409 accounts_required` on a server that has no accounts. Servers may rate
limit starts (`429`).

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
unknown or already collected code is `404 link_invalid`.

## Identity and logout

### `GET /v1/account`

Used by `firekeeper whoami`. Any valid token may call it:

```json
{
  "id": "9f2c...", "email": "you@example.com",
  "token": { "name": "Alice's laptop", "scope": "ingest", "machine_id": "<machine id>" }
}
```

`token` describes the bearer token that made the request, without its secret.
A revoked or unknown token gets `401 unauthorized`.

### `DELETE /v1/tokens/current`

Authenticated with the token to revoke; this is how `firekeeper logout`
cleans up. `200 { "ok": true }`. An already revoked token gets `401`, which
the client treats as success.

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
  "provider": "codex | copilot | kimi | claude | opencode | gemini | cursor",
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
        "started_at": "2026-10-07T12:00:00Z", "last_activity_at": "2026-10-07T12:05:00Z",
        "files_source": "git", "files": ["/repo/src/main.go", "/repo/src/gen.go"]
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
- `state` is one of `ACTIVE`, `WAITING`, `NEEDS_INPUT`, `ENDED`, `UNKNOWN`. It
  is a best-effort reading of the latest observable provider event, not ground
  truth.
- Metadata: empty strings keep the stored values. `started_at` and `commit`
  (the commit checked out when the session started; optional) keep their
  first value. `last_activity_at` only moves forward. An empty `state` keeps the
  stored state, or `UNKNOWN` for a new session.
- Ingest also counts as a heartbeat for the machine.
- Files: a server may derive a session's changed files from the
  file-changing tool calls in newly stored events. `TouchedFiles` in
  `pkg/event` does this the same way the client does. Re-sent events are not
  scanned again.
- Git files (optional): with `files_source` `git`, `files` replaces the
  session's Git change list, even when empty. Paths are absolute or relative
  to `cwd`, at most 5,000 of them, each non-blank and at most 4,096 bytes.
  Without `files_source`, or with `tool_calls`, the stored list is kept and
  `files` must be absent. Any other `files_source` is rejected with 400.

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

## Limits

A server is expected to bound request sizes (5 MiB and 500 events per ingest
request, as above) and may bound what an account stores or how often it
ingests. The reporter splits batches to stay within the first two and, on
`429`, waits for `Retry-After`. An over-limit request must write nothing, so a
reporter can retry it later without losing or duplicating events.
