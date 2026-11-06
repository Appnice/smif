-- ADR-0021: a prompt for a busy or waiting session waits here until the
-- session is idle. Written by the daemon from the human's call only, never
-- from a message. name and agent keep what the human called the session,
-- because a gone session has no presence row to read them from.
CREATE TABLE session_queue (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  agent      TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  text       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  state      TEXT NOT NULL DEFAULT 'queued',
  error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX session_queue_state ON session_queue(state, id);
