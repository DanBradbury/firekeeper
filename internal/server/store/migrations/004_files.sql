-- T4.3: the commit checked out when a session started, and the files its
-- tool calls changed. Paths are relative to the session's cwd, or absolute
-- when outside it, as they arrived (already redacted by the reporter).
ALTER TABLE sessions ADD COLUMN "commit" TEXT NOT NULL DEFAULT '';

CREATE TABLE session_files (
    machine_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    path       TEXT NOT NULL,
    first_seq  INTEGER NOT NULL,
    last_seq   INTEGER NOT NULL,
    changes    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (machine_id, session_id, path),
    FOREIGN KEY (machine_id, session_id) REFERENCES sessions(machine_id, session_id)
);
