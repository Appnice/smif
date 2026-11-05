-- Built from the research report, not a real session: an open tool, which can be a waiting approval.
INSERT INTO session_v2 (id, directory, title, time_created, time_updated, time_busy, time_idle, time_suspended)
VALUES ('ses_0a1b2c3d4e5f', 'CWD', 'Fix the test', NOW, NOW, NOW - 100, NULL, NOW - 100);
INSERT INTO session_message VALUES ('msg_1', 'ses_0a1b2c3d4e5f', 'user', 1, NOW - 200, NOW - 200, '{"text":"fix the test"}');
INSERT INTO session_message VALUES ('msg_2', 'ses_0a1b2c3d4e5f', 'assistant', 2, NOW - 100, NOW - 100, '{"agent":"build","time":{"created":1793000000100}}');
INSERT INTO session_part VALUES ('msg_2', 'ses_0a1b2c3d4e5f', 0, 'tool', 'call_1', NOW, NOW, '{"name":"bash","state":{"status":"running","input":{"command":"go test ./..."}}}');
