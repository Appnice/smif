# ADR-0010: Home screen first, `smif` opens it, `:` prompt

**Status:** accepted, 2026-11-02.

**Context.** The first UI (ADR-0007) opened on six panels. Smif is idle most of the time and now and then
something waits for the human, so six tables of zeros were noise, and the one thing to act on had to be found.
Three directions were drawn (cockpit, inbox, feed with prompt); The owner picked the inbox with two parts of the feed.

**Decision.**
1. Plain `smif` in a terminal opens the UI (like lazygit, k9s); in a pipe it prints one `watch` summary.
2. The start screen is one list grouped by what it asks of the human: NEEDS YOU, RUNNING, QUEUED (queued and
   retry_wait), DONE TODAY (replies stay in their thread), DEAD; a preview of the selected item on the right,
   with an answer box for needs-you items. Agent tabs (`[` `]`, `0`-`9`) and a text filter (`/`) narrow it.
3. A running worker's preview shows a live card: its last tool calls from the transcript tail (`✓` done,
   a spinner on the one in progress). The run records its transcript path at start (since `f9d30d7`).
4. `:` opens a command prompt (ask, tell, answer, retry, cancel, open, agent, filter, read, inbox, digest,
   dashboard, help, quit) with shell-like tab completion of commands, agents and the intents their rules name.
   Asks and tells go out as the human, through the same RPCs as `smif ask` / `send`.
5. The six-panel overview stays as the dashboard (`v`).

**Data.** The source adds thread.list for running, queued, retry_wait and done, and a digest.get peek (no
advance) for the unread mark; `e` marks read with the shown cursor (ADR-0008). No new daemon method; D4 holds.
