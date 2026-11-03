-- ADR-0012: the session's name (claude --name, /rename) and the human's last
-- prompt, so the human can tell sessions in one repo apart.
ALTER TABLE presence ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE presence ADD COLUMN last_prompt TEXT NOT NULL DEFAULT '';
