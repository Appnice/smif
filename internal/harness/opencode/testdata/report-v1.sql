-- Built from the research report, not a real database: OpenCode V1 has these tables and no session_v2.
CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, title TEXT);
CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, data TEXT);
CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT);
