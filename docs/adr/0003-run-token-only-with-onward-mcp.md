# ADR-0003: Workers get a run token only when the profile enables onward MCP

**Status:** accepted, 2026-11-02

**What.** RFC 6.3 lists `MAILROOM_RUN_TOKEN` in every worker's environment. Mailroom mints the run-scoped
token only for profiles with `onward_mcp = true`; other workers get no token at all.

**Why.** D3 says workers talk to peers through `followups` and keeps the tool surface small. A token in the
environment of a worker with any Bash access would let it call the socket directly. Without a token the
followup path is the only way out, and the daemon controls its hop and thread.

**Trade-off.** None for the default profiles. An onward-enabled worker behaves exactly as the RFC says.
