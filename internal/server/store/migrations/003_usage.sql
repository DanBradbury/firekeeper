-- Usage charts read token sums for a time range; index the range scan.
CREATE INDEX events_ts ON events(ts);
