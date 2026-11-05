# Using smif

## What the rows mean

| You see | It means |
|---|---|
| `! 3m … needs you` | The session stopped on a permission prompt or a question in its tab: the row says what it asks for. `o` brings the tab to the front. |
| `▶ 4m12s … Bash npm run build` | It works on your prompt; the row shows the step that runs now. `silent 6m`: it has written nothing for 6 minutes (a long command, or stuck). |
| `● new` | An answer you have not read. The preview shows your prompt and only the final answer, no steps or tool output. |
| `tab` | Goes to the next session that needs you. |

## Getting told

- **Notifications.** An OS notification comes when a session starts to wait in its tab, or answers after a turn
  of more than 30 s. Set `daemon.session_notify` to `"waiting"`, `"answered"` or `"none"`.
- **Status line.** With `smif install`, the status line of each session names the others:
  `! shop waits · ● docs, scraper answered`.
- **Watchdogs.** A reminder while a session still waits (`daemon.remind_waiting`, default every 10 min, at most
  3 times), and one alert when a working session writes nothing for `daemon.alert_silent` (default 10 min) or works
  longer than `daemon.alert_long_turn` (off by default).

## Writing to a session

`enter` opens a chat with the selected session.

| The session is | Your text |
|---|---|
| **idle** | Is typed into its tab and sent, as if you typed it there (Terminal.app, iTerm2, tmux or herdr). |
| **busy** or **waiting** | Is not typed. A fork of the session answers (`claude -p --resume … --fork-session`): it knows what the session knows, works read-only, and leaves the session untouched. Needs an agent for the folder (`smif init`). |

smif never answers a permission prompt for you.

smif never answers a permission prompt for you.

## Saved prompts and new sessions

Prompts you send often go in the config, and `p` on a session sends one (`@tests` in any prompt stands for it):

```toml
[prompts]
tests = "run the tests and report what failed"
review = "review your last change for bugs, then list them"
```

`n` opens a new session: `claude --name <name>` in the selected session's folder, in a new Terminal.app window,
iTerm2 tab, tmux window or herdr workspace.

## The terminal UI

- The list shows the items that need you first, then your sessions, running workers, the queue, the items done
  today and dead letters (each group only when it has rows). The right pane shows the selected item. For a running worker, it shows the live tool calls.
- `a` answers, `c` cancels, `t` retries, `e` marks as read. `enter` opens the thread or the run: record,
  transcript and branch diff. On a run, `a` merges the branch after a y/N question.
- The SESSIONS group shows every open Claude Code session, and the other agents smif finds ([levels](agents.md)).
  It groups them by root folder, in a fixed order. Each row has its state: `!` waiting, `▶` working, `●` new answer,
  `○` idle. It also shows what the session does now, or the first line of its last answer. Sessions need no config: an agent is needed only for messages, workers and forks.
- `tab` goes to the next row that needs you. `z` hides the quiet sessions. Empty groups take no room.
- `p` sends a saved prompt to the session. `n` opens a new session in its folder.
- `enter` on a session opens the chat. `s` writes from the preview. Idle: typed in. Busy: a fork of the session
  answers in the chat. `o` (in the chat `ctrl+o`) brings its tab to the front. `y` (in the chat `ctrl+y`) copies
  the newest code block whole; press it again for the block before.
- In the prompt box, a long paste stays whole: you see its first and last lines, its size and whether it matches
  the clipboard. `ctrl+v` attaches an image, and a dropped file works too. `ctrl+p` picks a part to read or remove.
  A possible secret or a very long line opens a check before sending. Parts go to idle sessions of Claude Code.
- `:` opens a command prompt with tab completion: `ask`, `tell`, `prompt`, `answer`, `retry`, `agent`, `filter`, `digest`.
- `[` and `]` change the agent. `/` filters. `v` opens the dashboard (agents, runs, audit log). `?` shows all keys.
- In a pipe, `smif` prints one summary. `smif watch` gives a live view for tmux or ssh.

## From a shell

| What | How |
|---|---|
| List sessions | `smif sessions` |
| Prompt an idle session | `smif prompt api "run the tests"` types into the session with that name (Terminal.app, iTerm2, tmux or herdr) |
| Live view for tmux or ssh | `smif watch` |
| See what smif did | `/smif` in a session, or `smif stats` |

## Stats

`smif stats` shows the last 14 days. Each message has exactly one outcome.

```
  inbound       11 messages (+4 repeats merged), 2 replies
  without you   4 (50% of the work)  command 1 · digest 1 · worker 2
  needed you    3  answered 1 · waiting 2 · cancelled 0
  runs          7 worker, 1 command (1 failed)
  tokens        5.2k in (incl. cache) · 250 out
```

Use `--since 2026-01-01` to set the start date. Use `--json` for scripts.
