# ADR-0005: One worker session per agent and workspace directory

**Status:** accepted, 2026-11-02

**What.** `agents/<agent>/session.json` keeps a session per workspace directory (main checkout or the
agent's worktree), not one per agent.

**Why.** Claude Code stores sessions per working directory; `--resume <id>` from another directory does not
find the session. An agent with both a read-only triage profile (main checkout) and a fix profile (worktree)
needs two sessions. A resume that fails with "No conversation found" resets the session and retries without
counting an attempt.

**Trade-off.** Memory is still shared: rotation writes one `memory.md` per agent, read by every session.
