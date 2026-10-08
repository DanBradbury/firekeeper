-- T5.4: per-account storage accounting. stored_bytes is the size of the
-- account's event text plus raw payloads, kept up to date by ingest so the
-- storage limit never has to scan the events table.
ALTER TABLE accounts ADD COLUMN stored_bytes INTEGER NOT NULL DEFAULT 0;

UPDATE accounts SET stored_bytes = COALESCE((
    SELECT SUM(length(CAST(text AS BLOB)) + length(CAST(raw AS BLOB)))
    FROM events WHERE events.account_id = accounts.id), 0);
