-- files_source says where a session's changed-file list comes from: 'git'
-- when the reporter sent the repository's change list since the session's
-- start commit, '' when only the files its tool calls edited are known.
-- session_git_files holds the latest Git list, replaced on each ingest that
-- carries one. Paths are stored like session_files paths.
ALTER TABLE sessions ADD COLUMN files_source TEXT NOT NULL DEFAULT '';

CREATE TABLE session_git_files (
    account_id TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    PRIMARY KEY (account_id, machine_id, session_id, path),
    FOREIGN KEY (account_id, machine_id, session_id)
        REFERENCES sessions(account_id, machine_id, session_id)
);
