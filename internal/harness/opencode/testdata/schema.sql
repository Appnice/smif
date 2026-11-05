-- Built from the research report (OpenCode source at commit 1e4ad075), not a real database.
CREATE TABLE session_v2 (
	id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT, fork_session_id TEXT, slug TEXT,
	directory TEXT NOT NULL, title TEXT, version TEXT,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_busy INTEGER, time_idle INTEGER, idle_outcome TEXT,
	time_archived INTEGER, time_suspended INTEGER
);
CREATE TABLE session_message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER, data TEXT NOT NULL
);
CREATE TABLE session_part (
	message_id TEXT NOT NULL, session_id TEXT NOT NULL, ordinal INTEGER NOT NULL, type TEXT NOT NULL,
	call_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT NOT NULL,
	PRIMARY KEY (message_id, ordinal)
);
