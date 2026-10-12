-- last_seen_at is when the reporter last said it saw the session (ingest or
-- heartbeat). Discovery only reports live sessions, so a session that stops
-- being seen has exited; the server marks it ENDED after a quiet period.
ALTER TABLE sessions ADD COLUMN last_seen_at TEXT;
UPDATE sessions SET last_seen_at = COALESCE(last_activity_at, started_at);
CREATE INDEX sessions_live_seen ON sessions(last_seen_at) WHERE state <> 'ENDED';
