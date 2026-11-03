CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  thread_id       TEXT NOT NULL,
  parent_id       TEXT,
  from_agent      TEXT NOT NULL,
  from_origin     TEXT NOT NULL,
  from_run        TEXT,
  to_agent        TEXT NOT NULL,
  kind            TEXT NOT NULL,
  intent          TEXT NOT NULL,
  priority        INTEGER NOT NULL,
  state           TEXT NOT NULL,
  hop             INTEGER NOT NULL,
  dedupe_key      TEXT,
  coalesced_count INTEGER NOT NULL DEFAULT 0,
  deadline        INTEGER,
  rule_id         TEXT,
  handler         TEXT,
  profile         TEXT,
  attempt         INTEGER NOT NULL DEFAULT 0,
  extra_attempts  INTEGER NOT NULL DEFAULT 0,
  not_before      INTEGER NOT NULL DEFAULT 0,
  hold_reason     TEXT,
  reason          TEXT,
  summary         TEXT,
  human_question  TEXT,
  recurrence      INTEGER NOT NULL DEFAULT 0,
  answered_by     TEXT,
  seen            INTEGER NOT NULL DEFAULT 0,
  handled_seq     INTEGER,
  envelope_json   TEXT NOT NULL,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX messages_queue  ON messages (to_agent, state, priority, created_at);
CREATE INDEX messages_thread ON messages (thread_id);
CREATE INDEX messages_dedupe ON messages (dedupe_key, state);
CREATE INDEX messages_handled ON messages (to_agent, handled_seq);

CREATE TABLE runs (
  id            TEXT PRIMARY KEY,
  message_id    TEXT NOT NULL REFERENCES messages(id),
  agent         TEXT NOT NULL,
  handler       TEXT NOT NULL,
  profile       TEXT,
  attempt       INTEGER NOT NULL,
  state         TEXT NOT NULL,
  pid           INTEGER,
  pgid          INTEGER,
  session_id    TEXT,
  archived_session TEXT,
  workspace     TEXT,
  branch        TEXT,
  base_sha      TEXT,
  head_sha      TEXT,
  diffstat      TEXT,
  result_json   TEXT,
  usage_json    TEXT,
  cost_usd      REAL,
  input_tokens  INTEGER,
  output_tokens INTEGER,
  error         TEXT,
  transcript    TEXT,
  stderr        TEXT,
  started_at    INTEGER NOT NULL,
  ended_at      INTEGER
);
CREATE INDEX runs_message ON runs (message_id);
CREATE INDEX runs_agent_state ON runs (agent, state);

CREATE TABLE presence (
  repo       TEXT NOT NULL,
  session_id TEXT NOT NULL,
  last_seen  INTEGER NOT NULL,
  state      TEXT NOT NULL,
  PRIMARY KEY (repo, session_id)
);

CREATE TABLE cursors (
  agent         TEXT PRIMARY KEY,
  digest_cursor INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE budgets (
  agent    TEXT NOT NULL,
  day      TEXT NOT NULL,
  cost_usd REAL NOT NULL DEFAULT 0,
  tokens   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (agent, day)
);

CREATE TABLE blobs (
  sha256     TEXT PRIMARY KEY,
  size       INTEGER NOT NULL,
  mime       TEXT NOT NULL,
  refcount   INTEGER NOT NULL DEFAULT 0,
  zero_since INTEGER
);

CREATE TABLE message_blobs (
  message_id TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  name       TEXT NOT NULL,
  PRIMARY KEY (message_id, sha256, name)
);

CREATE TABLE threads (
  thread_id     TEXT PRIMARY KEY,
  frozen        INTEGER NOT NULL DEFAULT 0,
  frozen_reason TEXT,
  branch        TEXT
);

CREATE TABLE audit (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         INTEGER NOT NULL,
  event      TEXT NOT NULL,
  agent      TEXT,
  message_id TEXT,
  run_id     TEXT,
  thread_id  TEXT,
  detail     TEXT
);
CREATE INDEX audit_message ON audit (message_id);

CREATE TABLE kv (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
