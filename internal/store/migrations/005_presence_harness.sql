-- ADR-0019: which coding agent a session belongs to. Every row so far came
-- from Claude Code's hooks.
ALTER TABLE presence ADD COLUMN harness TEXT NOT NULL DEFAULT 'claude';
