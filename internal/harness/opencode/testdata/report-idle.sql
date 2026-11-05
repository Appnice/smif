-- Built from the research report, not a real session: a finished run.
INSERT INTO session_v2 (id, directory, title, time_created, time_updated, time_busy, time_idle, idle_outcome, time_suspended)
VALUES ('ses_0a1b2c3d4e5f', 'CWD', 'Fix the test', NOW, NOW, NOW - 900, NOW - 100, 'succeeded', NOW - 900);
INSERT INTO session_message VALUES ('msg_1', 'ses_0a1b2c3d4e5f', 'user', 1, NOW - 1000, NOW - 1000, '{"text":"fix the test","time":{"created":1793000000000}}');
INSERT INTO session_message VALUES ('msg_2', 'ses_0a1b2c3d4e5f', 'assistant', 2, NOW - 900, NOW - 600, '{"agent":"build","finish":"tool-calls","time":{"created":1793000000100,"completed":1793000000400}}');
INSERT INTO session_message VALUES ('msg_3', 'ses_0a1b2c3d4e5f', 'assistant', 3, NOW - 500, NOW - 100, '{"agent":"build","finish":"stop","time":{"created":1793000000500,"completed":1793000000900}}');
INSERT INTO session_part VALUES ('msg_2', 'ses_0a1b2c3d4e5f', 0, 'text', NULL, NOW, NOW, '{"text":"I run the tests."}');
INSERT INTO session_part VALUES ('msg_2', 'ses_0a1b2c3d4e5f', 1, 'tool', 'call_1', NOW, NOW, '{"name":"bash","state":{"status":"completed","input":{"command":"go test ./..."}}}');
INSERT INTO session_part VALUES ('msg_3', 'ses_0a1b2c3d4e5f', 0, 'text', NULL, NOW, NOW, '{"text":"The test passes now."}');
