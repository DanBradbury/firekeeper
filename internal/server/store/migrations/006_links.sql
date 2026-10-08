-- Machine linking (device-code flow) and token last-used times.
--
-- A link code is a pair: a long device code the CLI polls with and a short
-- user code a signed-in person approves. Only hashes are stored. The ingest
-- token is created at the poll that consumes the approval, so its secret is
-- never stored in this table.

ALTER TABLE tokens ADD COLUMN last_used_at TEXT;

CREATE TABLE link_codes (
    device_hash  TEXT PRIMARY KEY,
    user_hash    TEXT NOT NULL UNIQUE,
    machine_id   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    account_id   TEXT REFERENCES accounts(id),
    machine_name TEXT NOT NULL DEFAULT '',
    approved_at  TEXT,
    consumed_at  TEXT
);
CREATE INDEX link_codes_expiry ON link_codes(expires_at);
