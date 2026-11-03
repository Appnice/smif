# ADR-0008: Audit feed over the socket (`audit.since`), bounded digest read, `mailroom watch`

**Status:** accepted, 2026-11-02

1. **What.** A new human-only daemon method `audit.since` returns audit rows after a cursor. The `mailroom ui`
   EVENTS panel and `mailroom watch --events` follow it. `digest.get` gets an optional `until`. A new command
   `mailroom watch` prints a plain-text summary or the event stream for a pipe, a tmux pane or ssh.
2. **Why a method, not the JSONL file.** The SQLite `audit` table is the source of truth (ADR-0004); the
   JSONL file can miss the newest rows after a crash, and it rotates and gzips by day, so a file follower must
   handle day changes and compressed files (`mailroom tail` does only the first). The table has a gap-free
   cursor (`seq`). A socket client also needs no read access to the data directory. The table already holds
   every row the file holds.
3. **Contract.** Params `{after, limit, from_end}`. Rows with `seq > after`, oldest first, at most `limit`
   (default 200, max 1000); `more` says that further rows exist. `from_end: true` returns the newest `limit`
   rows (0: none) and puts `cursor` at the head, so a follower starts at "now". Every page carries `head`, the
   newest seq; `head < cursor` means a new database, and the client starts again from the end. Details pass
   the same token redaction as the JSONL file; a detail that fails the redaction round trip is dropped.
4. **Access (D4).** `audit.since` is human-only like `thread.*` and `digest.get` (ADR-0006 item 2): audit
   details carry summaries and human questions. No MCP tool calls it, so no agent gets a new inbound read path.
5. **Polling.** The UI reads the feed in its 1 s poll; `watch --events` polls every second by default. This
   is a human screen, not agent polling. A long-poll (`wait_ms`) is possible later if load matters.
6. **Digest `until`.** `digest.get` with `until > 0` lists and advances only items with `handled_seq <=
   until`. The UI's digest screen peeks first (no advance), and its "mark read" key passes the `cursor` of
   what it showed. An item handled between the peek and the key press stays unread. Without `until` the
   method works as before.
7. **`mailroom watch`.** Without `--events`: one compact summary every `--interval` (default 5 s): agents,
   running runs, needs-you items, dead letters, the last `--last` events. With `--events`: the last `--last`
   events, then one line per event. While the daemon is down it prints a notice (once per outage for
   `--events`) and retries; the event cursor survives a daemon restart, so no event is skipped. SIGINT and
   SIGTERM end it with exit 0. `--once` prints one round and exits 1 when the daemon is down.
8. **Tests.** `internal/store` (`AuditTail`, `AuditHead`), `internal/ui` (feed paging and reset, event
   format, EVENTS panel, digest screen, summary golden), `e2e/ui_events_test.go`: the live EVENTS panel and
   digest mark-read, `audit.since` refused for an agent, paging, the bounded advance, and `mailroom watch
   --events` started before the daemon, over a daemon restart, ended by SIGINT.
