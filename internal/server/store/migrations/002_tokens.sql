CREATE TABLE tokens (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    scope       TEXT NOT NULL CHECK (scope IN ('ingest', 'read')),
    machine_id  TEXT NOT NULL DEFAULT '',
    hash        TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL,
    revoked_at  TEXT
);
