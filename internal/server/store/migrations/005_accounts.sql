-- Accounts and tenancy. Every machine, session, event and token belongs to
-- an account. Rows that exist before this migration go to the default
-- account, which has no email or password and so cannot sign in; it is the
-- owner in single-user mode.

CREATE TABLE accounts (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    disabled      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO accounts(id, email, password_hash, created_at, disabled)
VALUES ('default', '', '', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), 0);

CREATE TABLE invites (
    code_hash  TEXT PRIMARY KEY,
    created_by TEXT NOT NULL REFERENCES accounts(id),
    used_by    TEXT REFERENCES accounts(id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

-- Browser sessions. id_hash is the SHA-256 of the cookie value, so a leaked
-- database cannot be replayed as cookies.
CREATE TABLE web_sessions (
    id_hash    TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX web_sessions_expiry ON web_sessions(expires_at);

-- Rebuild the data tables with the account in every key. FTS and the
-- triggers that feed it go first; the FTS index is rebuilt at the end.
DROP TRIGGER events_ai;
DROP TRIGGER events_ad;
DROP TRIGGER events_au;
DROP TABLE events_fts;
DROP INDEX events_ts;
DROP INDEX sessions_activity;

ALTER TABLE session_files RENAME TO session_files_old;
ALTER TABLE events RENAME TO events_old;
ALTER TABLE sessions RENAME TO sessions_old;
ALTER TABLE machines RENAME TO machines_old;
ALTER TABLE tokens RENAME TO tokens_old;

CREATE TABLE machines (
    account_id        TEXT NOT NULL REFERENCES accounts(id),
    id                TEXT NOT NULL,
    name              TEXT NOT NULL DEFAULT '',
    hostname          TEXT NOT NULL DEFAULT '',
    os                TEXT NOT NULL DEFAULT '',
    version           TEXT NOT NULL DEFAULT '',
    last_heartbeat_at TEXT,
    PRIMARY KEY (account_id, id)
);

CREATE TABLE sessions (
    account_id       TEXT NOT NULL,
    machine_id       TEXT NOT NULL,
    session_id       TEXT NOT NULL,
    provider         TEXT NOT NULL,
    cwd              TEXT NOT NULL DEFAULT '',
    project          TEXT NOT NULL DEFAULT '',
    branch           TEXT NOT NULL DEFAULT '',
    "commit"         TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'UNKNOWN',
    title            TEXT NOT NULL DEFAULT '',
    started_at       TEXT,
    last_activity_at TEXT,
    event_count      INTEGER NOT NULL DEFAULT 0,
    input_tokens     INTEGER NOT NULL DEFAULT 0,
    output_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_tokens     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, machine_id, session_id),
    FOREIGN KEY (account_id, machine_id) REFERENCES machines(account_id, id)
);
CREATE INDEX sessions_activity ON sessions(account_id, last_activity_at DESC);

CREATE TABLE events (
    account_id    TEXT NOT NULL,
    machine_id    TEXT NOT NULL,
    session_id    TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    provider      TEXT NOT NULL,
    ts            TEXT,
    role          TEXT NOT NULL,
    text          TEXT NOT NULL DEFAULT '',
    tool_name     TEXT,
    model         TEXT,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_tokens  INTEGER NOT NULL DEFAULT 0,
    raw           TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(raw)),
    PRIMARY KEY (account_id, machine_id, session_id, seq),
    FOREIGN KEY (account_id, machine_id, session_id)
        REFERENCES sessions(account_id, machine_id, session_id)
);
CREATE INDEX events_ts ON events(ts);

CREATE TABLE session_files (
    account_id TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    first_seq  INTEGER NOT NULL,
    last_seq   INTEGER NOT NULL,
    changes    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, machine_id, session_id, path),
    FOREIGN KEY (account_id, machine_id, session_id)
        REFERENCES sessions(account_id, machine_id, session_id)
);

CREATE TABLE tokens (
    id         TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    name       TEXT NOT NULL,
    scope      TEXT NOT NULL CHECK (scope IN ('ingest', 'read')),
    machine_id TEXT NOT NULL DEFAULT '',
    hash       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    revoked_at TEXT
);

INSERT INTO machines(account_id, id, name, hostname, os, version, last_heartbeat_at)
SELECT 'default', id, name, hostname, os, version, last_heartbeat_at FROM machines_old;

INSERT INTO sessions(account_id, machine_id, session_id, provider, cwd, project, branch, "commit", model, state, title,
    started_at, last_activity_at, event_count, input_tokens, output_tokens, cache_tokens)
SELECT 'default', machine_id, session_id, provider, cwd, project, branch, "commit", model, state, title,
    started_at, last_activity_at, event_count, input_tokens, output_tokens, cache_tokens FROM sessions_old;

INSERT INTO events(rowid, account_id, machine_id, session_id, seq, provider, ts, role, text, tool_name, model,
    input_tokens, output_tokens, cache_tokens, raw)
SELECT rowid, 'default', machine_id, session_id, seq, provider, ts, role, text, tool_name, model,
    input_tokens, output_tokens, cache_tokens, raw FROM events_old;

INSERT INTO session_files(account_id, machine_id, session_id, path, first_seq, last_seq, changes)
SELECT 'default', machine_id, session_id, path, first_seq, last_seq, changes FROM session_files_old;

INSERT INTO tokens(id, account_id, name, scope, machine_id, hash, created_at, revoked_at)
SELECT id, 'default', name, scope, machine_id, hash, created_at, revoked_at FROM tokens_old;

DROP TABLE session_files_old;
DROP TABLE events_old;
DROP TABLE sessions_old;
DROP TABLE machines_old;
DROP TABLE tokens_old;

CREATE VIRTUAL TABLE events_fts USING fts5(
    text, content='events', content_rowid='rowid'
);
INSERT INTO events_fts(events_fts) VALUES ('rebuild');

CREATE TRIGGER events_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER events_ad AFTER DELETE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;
CREATE TRIGGER events_au AFTER UPDATE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
    INSERT INTO events_fts(rowid, text) VALUES (new.rowid, new.text);
END;
