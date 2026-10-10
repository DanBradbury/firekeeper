-- Usage reads only token counts, never text or raw. Without a covering index
-- the planner scans every event row for the account (the table is mostly
-- text and raw payloads). This partial index holds just the token-bearing
-- events and the columns the usage query reads, so it is answered from the
-- index alone.
DROP INDEX IF EXISTS events_ts;
CREATE INDEX events_usage ON events(account_id, ts, model, provider, machine_id, session_id, input_tokens, output_tokens, cache_tokens)
    WHERE input_tokens > 0 OR output_tokens > 0 OR cache_tokens > 0;
