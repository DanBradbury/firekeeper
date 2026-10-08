CREATE TABLE machines (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL DEFAULT '',
    hostname          TEXT NOT NULL DEFAULT '',
    os                TEXT NOT NULL DEFAULT '',
    version           TEXT NOT NULL DEFAULT '',
    last_heartbeat_at TEXT
);

CREATE TABLE sessions (
    machine_id       TEXT NOT NULL REFERENCES machines(id),
    session_id       TEXT NOT NULL,
    provider         TEXT NOT NULL,
    cwd              TEXT NOT NULL DEFAULT '',
    project          TEXT NOT NULL DEFAULT '',
    branch           TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'UNKNOWN',
    title            TEXT NOT NULL DEFAULT '',
    started_at       TEXT,
    last_activity_at TEXT,
    event_count      INTEGER NOT NULL DEFAULT 0,
    input_tokens     INTEGER NOT NULL DEFAULT 0,
    output_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_tokens     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (machine_id, session_id)
);
CREATE INDEX sessions_activity ON sessions(last_activity_at DESC);

CREATE TABLE events (
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
    PRIMARY KEY (machine_id, session_id, seq),
    FOREIGN KEY (machine_id, session_id) REFERENCES sessions(machine_id, session_id)
);

CREATE VIRTUAL TABLE events_fts USING fts5(
    text, content='events', content_rowid='rowid'
);

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
