# ADR-0014: What a session does now, on the home screen

**Status:** accepted, 2026-11-03.

**Context.** The home screen (ADR-0010) listed sessions with a dot and the first line of their last answer. A busy
session showed only "working · 2m", and a waiting one only "waits for you in its tab". The human could not tell
what a session was doing, what it was waiting for, or whether it was stuck, without switching tabs. Empty groups
(NEEDS YOU · 0, QUEUED · 0, DEAD · 0) took room. The agent tabs in the top line counted messages, not sessions, and a
session outside every agent's repo had no tab at all. The spend in the top line read like a bill, though on a
subscription nothing is billed.

**Decision.**

- **Activity from the transcript.** `session.list` gives each session an `activity` (`api.SessionActivity`):
  - the human's prompt that the newest exchange answers, and when it came;
  - the tool calls since then (the last 12, each with its tool, its target and whether its result came back);
  - `now`, the first call without a result: the tool that runs, or the permission or question a waiting session
    shows in its tab;
  - `wrote_at`, when the transcript last changed.

  `presence.Read` takes all of this from the same 2 MB tail as the answer. The target is the command, file, pattern,
  URL or question, on one line. Paths in the session's folder are written relative to it, and home as `~`. The
  daemon reads a transcript again only when its size or mtime changes. `session.list` is human only.
- **Rows keep their order.** Rows stay sorted by folder and name (rows must not jump). The state is a column of its
  own, readable without colour:
  - `! 4m` waiting, with what it asks for and "needs you";
  - `▶ 2m14s` working, with the step that runs, the tool count and "silent 6m" once the transcript has not changed
    for 5 minutes;
  - `● new` for an answer the human has not seen;
  - `○ idle`.
- **Top line.** It counts the sessions per state (waiting, new, working, quiet) and the messages that ask the human
  or died. It shows the agent filter only when one is set (`[ ]`, `0`). It shows `● live`, or how old the view is
  when polls stop arriving. The spend reads "≈ $X at API prices today": `total_cost_usd` as `claude -p` reports it,
  which is billed only on an API key.
- **The preview follows the state.**
  - Working: the prompt, the step that runs and for how long, the last five steps, and the asks to its fork.
  - Waiting: what it stopped on (a permission, a question, a plan), when it asked, and the steps before. `o` brings
    its tab to the front. Mailroom never answers a permission prompt.
  - Idle: your last prompt and the final answer.

  Under each preview, one line says where `s` sends the text: typed into the tab, or answered by a fork (ADR-0013).
- **Keys.**
  - `tab` and `shift+tab` move to the next or previous row that needs the human: a waiting session, a new answer, a
    question, a dead letter. With none, they move to the next group. In the chat, `tab` moves to the next session
    that needs the human.
  - `z` hides the quiet sessions (idle, answer read).
  - Empty groups take no room.
- **Live.** The UI already polls once a second (ADR-0007). Timers and steps redraw on each poll, so no push channel
  is added.

**Limits.**

- Transcripts and session files are not a documented interface (docs/compat.md). The doctor canary (ADR-0012)
  catches a change of the session files, not of the tool records. A change there makes `now` empty, and the rows
  fall back to "thinking".
- Claude Code writes a tool call to the transcript a second or so after it starts (checked live on 2.1.316), so the
  step that runs shows with that delay plus the poll's second.
- A long command that prints nothing also shows "silent"; the word does not mean stuck.

**Tests.**

- `internal/presence` `TestReadActivity`: steps, targets, MCP names, pending, questions, sidechains.
- `e2e/sessions_test.go` `TestSessionDiscovery`: the activity in `session.list` from a real file, with relative paths.
- `internal/ui/sessions_test.go`: busy, waiting and new rows, the previews (goldens), `tab`, `z`, the chat.
