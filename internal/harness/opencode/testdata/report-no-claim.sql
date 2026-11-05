-- Built from the research report, not a real session: time_busy is the newest, with no claim.
INSERT INTO session_v2 (id, directory, title, time_created, time_updated, time_busy, time_idle, time_suspended)
VALUES ('ses_0a1b2c3d4e5f', 'CWD', 'Fix the test', NOW, NOW, NOW - 100, NOW - 5000, NULL);
INSERT INTO session_message VALUES ('msg_1', 'ses_0a1b2c3d4e5f', 'user', 1, NOW - 200, NOW - 200, '{"text":"fix the test"}');
