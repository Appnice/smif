# ADR-0014: What a session does now, and telling the human

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
- **When the human looks elsewhere.**
  - Status line: `inbox.counts` for the human also names the other live sessions that wait in their tab, and those
    with an answer the human has not seen. "Not seen" uses the UI's seen file; with no seen entry, it means an
    answer younger than 15 minutes. The status line puts them first: `! shop waits · ● docs answered`. An agent's
    counts never name sessions.
  - Notifications: the daemon looks at the sessions every 2 s. It notifies when a session starts to wait (body:
    what it asks for), and when a session that worked 30 s or more answers (body: the first line of the answer).
    A session it sees for the first time is not news. `daemon.session_notify` is `"waiting,answered"` (default),
    `"waiting"`, `"answered"` or `"none"`. The audit records only the kind and the session, never the text.
- **No setup.**
  - With no config file, the daemon runs with the defaults and no agents. The session view needs no agent; messages,
    workers and forks do. `mailroom doctor` says so instead of failing.
  - `mailroom` and `mailroom sessions` start the daemon in the background when none answers
    (`MAILROOM_NO_AUTOSTART=1` turns this off). That daemon runs until a reboot; `mailroom install` makes it a
    service.
  - `daemon.lock` in the data dir lets one daemon run per data dir: a second one stops before it opens the store.
- **Canary.** The doctor canary (ADR-0012) also checks that the tool calls in the transcripts it reads have an id and
  a name. Without them, the step that runs cannot be matched to its result.
- **Trial.** The trial ends on 16 November 2026. If its numbers (`mailroom stats`, the sessions line) show that the
  mail part is not used, it moves behind a flag; the session view stays the product.

**Limits.**

- Transcripts and session files are not a documented interface (docs/compat.md). The canary catches a change of the
  session files and of the tool records' id and name. A change elsewhere can still make `now` empty; the rows then
  fall back to "thinking".
- A session's status line redraws only when Claude Code redraws it (on that session's own activity), so an idle
  session shows the others' state as of its last redraw. The notification covers that case.
- A notification also comes for the session the human is looking at; Mailroom does not know which tab is in front.
- Claude Code writes a tool call to the transcript a second or so after it starts (checked live on 2.1.316), so the
  step that runs shows with that delay plus the poll's second.
- A long command that prints nothing also shows "silent"; the word does not mean stuck.

**Tests.**

- `internal/presence` `TestReadActivity`: steps, targets, MCP names, pending, questions, sidechains.
- `e2e/sessions_test.go` `TestSessionDiscovery`: the activity in `session.list` from a real file, with relative paths.
  `TestSessionNotify`: no notification on first sight, then waiting and answered; the counts name others, not self,
  and never for an agent; the canary on a tool call without an id.
- `e2e/autostart_test.go` `TestAutostart`: no config, `mailroom sessions` starts the daemon, a second daemon stops on
  the lock.
- `internal/statusline` `TestSegment`: the session names in the segment.
- `internal/ui/sessions_test.go`: busy, waiting and new rows, the previews (goldens), `tab`, `z`, the chat.
