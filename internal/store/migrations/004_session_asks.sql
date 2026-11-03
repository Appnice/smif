-- ADR-0013: a question for a busy interactive session goes to a worker that
-- resumes a fork of that session (its context, never the session itself).
-- Written by the daemon only, never from a draft.
CREATE TABLE session_asks (
  message_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  cwd        TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX session_asks_session ON session_asks(session_id, created_at);
