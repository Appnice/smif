# ADR-0013: A busy session's fork answers

**Status:** accepted, 2026-11-03. Amended by ADR-0021 section 3, 2026-11-05.

*Amended 2026-11-05.* In the UI box of a busy or waiting session, `enter` no longer asks a fork. It puts the text
in a queue, which types it when the session is idle (`session.queue`). `ctrl+f` in that box asks the fork, where a
fork can run. `session.send`, `smif prompt` with no flag, and an agent's ask to a session's name work as below.

**Context.** ADR-0012 lets the human type into an idle session from Smif. The human also wants to write to a
session that is working, and to open a chat with any session: list, arrows, enter, write. Text typed into a busy
Claude Code session waits in its input or breaks into the running turn, so Smif must not type there. Agents
also want to ask "the cafe session" by name, but D4 says no inbound text reaches an interactive session.

**Decision.**

- **`session.send {session_id | name, text}`** (human only). An idle session gets the text typed in
  (`session.prompt`, ADR-0012). A busy or waiting session gets a session ask: an `ask` from the human to the agent
  that owns the session's folder, intent `session.ask`, answered by a worker that resumes a fork of the session.
- **The fork.** The worker runs `claude -p --resume <session id> --fork-session` in the session's folder (claude
  finds a session only from its own folder; the folder must be inside the agent's repo). It knows what the
  session knows, gets a new session id, and leaves the session and its transcript unchanged (checked live on
  Claude Code 2.1.316, docs/compat.md). It runs with the built-in `answer` profile: read-only tools, no writes,
  8 turns. No worker session is kept or rotated and no memory file is added: each ask forks the session fresh.
- **Routing.** The daemon writes a `session_asks` row (message, session, folder) in the same transaction as the
  message (migration 004); a draft cannot set it. Such an ask is queued for that worker whatever the agent's
  rules say, but the guards (hop limit, loops, `accept_from`) still reject it.
- **Agents ask by name.** An `ask` whose `to` is no agent but the name of one live session (`claude --name`,
  `/rename`) becomes a session ask for that session's agent. The answer goes back to the asking agent. Nothing
  is typed into the session, so D4 holds. A fork can read the session's context, so an agent that the target
  agent accepts (`accept_from`) can ask about it: that is the same trust as asking its worker.
- **A session no agent owns** (a folder outside every agent's repo) has no worker: a busy one is refused with that
  reason; an idle one still takes typed text.
- **Surfaces.** `session.tail` merges the asks into the conversation by time: `ask` (from the human or an agent)
  and `worker` (the answer, or its state while it runs). In the UI, `enter` on a session opens a chat: the
  whole conversation (final answers only), newest at the bottom, and a box that has the keys at once; `enter`
  sends (typed in or to a worker, the hint says which), `↑↓ pgup pgdn` scroll, `ctrl+o` brings its tab to the
  front, `esc` goes back. `s` writes from the preview the same way.
- **Counts.** `smif stats` adds a sessions line: prompts typed into idle sessions, asks a fork answered (from
  the human, from agents) and tabs brought to the front (`session.focus` is audited).

**Limits.**

- A fork of a long session reads its whole context, so its first answer can take a while.
- The fork sees the session's transcript as it is on disk: a turn still running is not in it.
- Forks are sessions too: they show in `claude --resume` in that folder.

**Tests.** `e2e/session_ask_test.go`: busy send forks (argv, folder, a new id, nothing typed), the tail shows the
ask and the answer, idle send types, an agent's ask by name forks, agents cannot send, a session without an
agent is refused, the stats and the doctor canary. `internal/ui/sessions_test.go`: the chat, a worker turn, `s`
on a busy session.
