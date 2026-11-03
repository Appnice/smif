# ADR-0006: API and routing details the RFC leaves open

**Status:** accepted, 2026-11-02

1. **`run.diff` and `run.apply` methods.** The daemon runs the merge so it is audited as a human action
   (`human.apply`). The CLI's `diff`/`apply` call them.
2. **Who may call what.** Agents (interactive or worker tokens) may send, ask, emit, await their own tickets,
   list agents, read their own counts and fetch blobs attached to replies addressed to them. Everything that
   reads mail or changes state for the human (`thread.*`, `run.*`, `digest.get`, `message.reply/retry/cancel`,
   `admin.reload/gc`) needs the human token. Hooks and the status line use the human token.
3. **Replies.** A reply to an ask from an interactive session, the CLI or the human is stored and counted
   (`replies ready`), not routed. A reply to a worker's ask is routed through the asker's rules (the
   continuation of RFC 5.3). Replies to your own ask bypass `accept_from`.
4. **`mailroom reply`.** For an ask that a rule escalated, the text becomes the reply to the asker. For
   anything else the text goes back to the agent as a `reply` in the same thread; if the item was a worker's,
   that worker's profile runs again on it.
5. **Client-supplied envelope fields.** `from`, `id`, `hop`, `parent_id`, `origin`, `created_at` and
   `status` are rejected at ingress and audited (`message.rejected_ingress`).
6. **Daemon knobs** not named in the RFC, all with the RFC's defaults: `retry_backoff`,
   `rate_limit_per_minute`, `max_thread_messages`, `ping_pong_limit`, `ping_pong_window`, `aging_step`,
   `overload_extra_attempts` and retention durations.
7. **Paths.** `MAILROOM_CONFIG_DIR`, `MAILROOM_CONFIG`, `MAILROOM_DATA_DIR` and `MAILROOM_SOCKET` override
   the RFC locations (tests need short socket paths: macOS caps them at 104 bytes).
8. **Hooks inside workers.** User hooks also fire in worker runs; `mailroom hook` ignores calls whose
   environment has `MAILROOM_RUN_ID`, so a worker never counts as an interactive session.
