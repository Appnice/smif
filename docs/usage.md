# Using smif

## What the rows mean

| You see | It means |
|---|---|
| `! 3m … needs you` | The session stopped on a permission prompt or a question in its tab: the row says what it asks for. `o` brings the tab to the front. |
| `▶ 4m12s … Bash npm run build` | It works on your prompt; the row shows the step that runs now. `silent 6m`: it has written nothing for 6 minutes (a long command, or stuck). |
| `● new` | An answer you have not read. The preview shows your prompt and only the final answer, no steps or tool output. |
| `tab` | Goes to the next session that needs you. |

## Facts next to an answer

Under the answer, the preview and the chat show what the transcript and git say, from your newest prompt on:

`4 files · 1 commit · tests not run since the last edit · 3 uncommitted · 412k context`

smif states these facts. It does not judge the answer, and it blocks nothing. A part that is unknown or zero stays
out, except `0 uncommitted`. Facts need no model call.

| Part | It means |
|---|---|
| `4 files` | Distinct files that Edit, Write, MultiEdit and NotebookEdit changed. A refused edit does not count. |
| `1 commit` | Bash calls with `git commit` that succeeded. |
| `tests passed after the last edit` | A test command finished with success after the last edit of a code file. |
| `tests failed after the last edit` | The newest such test failed: a non-zero exit, a timeout, or failure words in its output. |
| `tests not run since the last edit` | No test command finished after that edit. |
| (no tests part) | No edit of a code file since your prompt: nothing to test. An edit of `.md`, `.txt` or anything under `docs/` is not a code edit. |
| `3 uncommitted` | Paths in `git status --porcelain` in the session's folder. Read when the transcript changes and the session is not busy, 2 s at most. No repo or a slow git shows nothing. |
| `412k context` | Input, cache read and cache creation tokens of the newest assistant record. |

The row shows `412k ctx` when the context passes `daemon.context_warn` (default `"200k"`; a count such as `"1m"` or
`"150000"`, or `"off"`). An 80-column screen has no preview pane: the chat shows the facts there.

Test commands are `go test`, `make check`, `make test`, `pytest`, `cargo test`, `npm test`, `npm run test`, `vitest`,
`jest`, `gradle test` (also `gradlew`) and `flutter test`, outside quotes. Add your own regexes, matched against the
whole command:

```toml
[daemon]
test_commands = ['^\./ci\.sh\b', 'tox']
```

What facts cannot see:

- A test run through a script with another name, until `daemon.test_commands` names it.
- A test sent to the background: it has no result in the transcript, so it reads as not run.
- Edits through Bash (`sed -i`) and edits by subagents.
- A test run behind a pipe (`go test ./... | tail`) that fails with no failure words in the part it kept.
- The first read of a transcript covers its last 16 MB, so a longer exchange can undercount files and commits.
- Facts are for Claude Code. Other agents with a transcript show only the uncommitted count.

## Getting told

- **Notifications.** An OS notification comes when a session starts to wait in its tab, or answers after a turn
  of more than 30 s. Set `daemon.session_notify` to `"waiting"`, `"answered"` or `"none"`.
- **Status line.** With `smif install`, the status line of each session names the others:
  `! shop waits · ● docs, scraper answered`.
- **Watchdogs.** A reminder while a session still waits (`daemon.remind_waiting`, default every 10 min, at most
  3 times), and one alert when a working session writes nothing for `daemon.alert_silent` (default 10 min) or works
  longer than `daemon.alert_long_turn` (off by default).
- **Phone ping.** Set `daemon.notify_url`, and each notification above also goes to that URL as one POST. See below.

### Phone ping

With [ntfy](https://ntfy.sh), install the app, subscribe to a long random topic, and add the topic to the config:

```toml
[daemon]
notify_url = "https://ntfy.sh/your-long-random-topic"
```

Run `smif reload`, then `smif notify test`. The phone shows `smif test ping`. Any server that takes a POST also works.

- **Events.** The same as the OS notifications, reminders and mail events included. `daemon.session_notify` and the
  watchdog keys choose them. `daemon.notify = "none"` turns off the OS notifications only, so the phone still gets them.
- **What it sends.** The body is a name and what happened: `shop waits for you`, `shop answered`, `shop still waits
  for you`. The headers are `Title: smif` and `Tags: bell`. The name is the agent or the folder, and it holds no prompt,
  answer or command ([safety.md](safety.md)).
- **The URL.** `https`, or `http` to localhost. Anything else fails `smif config validate`, and a redirect is not followed.
- **Failures.** One POST has 5 s, with no retry: a ping that fails is lost, and the daemon log says why, without the
  URL. A slow server does not delay smif. At most 16 POSTs run at once, and further pings are lost while they run.
- **One way.** A reply from the phone does nothing.

## Writing to a session

`enter` opens a chat with the selected session.

| The session is | Your text |
|---|---|
| **idle** | Is typed into its tab and sent, as if you typed it there (Terminal.app, iTerm2, tmux or herdr). |
| **busy** or **waiting** | Waits in a queue, and is typed when the session is idle. `ctrl+f` instead asks a fork of the session (`claude -p --resume … --fork-session`): it knows what the session knows, works read-only, and leaves the session untouched. A fork needs an agent for the folder (`smif init`). |

smif never answers a permission prompt for you.

### The queue

The row of a session with queued prompts shows `⧖ 2 queued`, and the chat lists them, oldest first.

- The oldest item goes in 2 s after the session reads idle, with the same checks as a typed prompt: idle again,
  claude in the foreground of its tab, not read only. One item goes in per idle.
- If a check refuses, the item stays, shows why, and tries again at most every 30 s.
- If text may sit unsent in the session's input, the next item waits until you look (`o`).
- In the chat, `ctrl+q` moves to the list. `x` on a row of the home screen goes there too. `↑↓` choose, `x` removes
  one, `esc` goes back to the text.
- An item expires after 24 h. When the session ends, its items are gone, and a notification says so.
- A session at a permission prompt reads `waiting`, so nothing goes in until you answer in its tab.
- Parts go in the queue as `@path` in the line. Their files stay while the item is queued.
- A partly typed line in the session's input stays there, and the queued text goes behind it.
- `/clear` gives a session a new id, so its queue reads as gone.

smif never answers a permission prompt for you.

## Background sessions

Sessions from `claude --bg` show too, with `[background]` on the row. smif reads `claude agents --json --all` at most
every 5 s. `blocked` reads waiting, `working` busy, `done` and `idle` idle, and any other word unknown. Answers, steps,
facts, notifications and `tab` work as for any session.

A background session has no tab, so smif never writes to it: no typing, no queue and no fork. `o` opens
`claude attach <id>` in a new tab. A done session leaves the list after 24 h, unless its answer is unread. If the
command fails or changes, the rows go away and nothing else breaks. `smif doctor` says why.

## History and restart

`R` opens the history: Claude Code sessions from `~/.claude/projects`, newest first, with no SDK runs and no subagents.

- `/` filters by name, folder or id. `ctrl+s` searches the text of your prompts and Claude's answers for the same
  words, newest session first. It stops at 50 hits or 10 s, and the title says so.
- `enter` opens `claude --resume <id>` in a new tab, in the session's folder. A session that still runs (`open` on the
  row), or a folder that is gone, is refused.

smif keeps the live sessions in `open-sessions.json` in the data dir. A session goes on the restore list when:

- the daemon starts and the session's process is gone, or
- two or more sessions vanish within 10 s, as in a terminal crash.

The home screen then shows `12 sessions closed · R reopens`. In `R` these sessions come first, and a session that was
busy reads `died working`. `A` reopens them, at most 20, 500 ms apart; `A` again takes the rest. A session you close
alone with `/exit` is not on the list. A session leaves the list when it runs again, or after 24 h. A session with no
transcript has nothing to resume, so it is not listed. This covers Claude Code only.

## Saved prompts and new sessions

Prompts you send often go in the config, and `p` on a session sends one (`@tests` in any prompt stands for it):

```toml
[prompts]
tests = "run the tests and report what failed"
review = "review your last change for bugs, then list them"
```

`n` opens a new session: `claude --name <name>` in the selected session's folder, in a new Terminal.app window,
iTerm2 tab, tmux window or herdr workspace. When another coding agent is on the daemon's PATH, `n` shows
`← claude →`. Left and right change the agent. Claude Code stays the default and is the only one that takes a name.
With a single agent, `n` asks only for the name, and left and right move the text cursor. See
[agents.md](agents.md#add-an-agent) for which agents show here.

## The terminal UI

- The list shows the items that need you first, then your sessions, running workers, the queue, the items done
  today and dead letters (each group only when it has rows). The right pane shows the selected item. For a running worker, it shows the live tool calls.
- `a` answers, `c` cancels, `t` retries, `e` marks as read. `enter` opens the thread or the run: record,
  transcript and branch diff. On a run, `a` merges the branch after a y/N question.
- The SESSIONS group shows every open Claude Code session, and the other agents smif finds ([levels](agents.md)).
  It groups them by root folder, in a fixed order. Each row has its state: `!` waiting, `▶` working, `●` new answer,
  `○` idle. It also shows what the session does now, or the first line of its last answer. Sessions need no config: an agent is needed only for messages, workers and forks.
- `b` groups the sessions by harness: each agent gets a header with its count, also a single agent. `b` again goes
  back to folders. The choice stays until you quit. `/` also matches the harness name, so `/codex` keeps the Codex rows.
- `tab` goes to the next row that needs you. `z` hides the quiet sessions. Empty groups take no room.
- `p` sends a saved prompt to the session. `n` opens a new session in its folder, and asks which agent when there is a choice.
- `R` opens the history of past sessions, the closed ones first ([History and restart](#history-and-restart)).
- `enter` on a session opens the chat. `s` writes from the preview. Idle: typed in. Busy or waiting: queued, and
  `ctrl+f` asks a fork, which answers in the chat. `o` (in the chat `ctrl+o`) brings its tab to the front, or opens `claude attach` for a `[background]` session. `y` (in the chat `ctrl+y`) copies
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
| Prompt an idle session | `smif prompt api "run the tests"` types into the session with that name (Terminal.app, iTerm2, tmux or herdr). A busy session gets a fork |
| Prompt when it is idle | `smif prompt --when-idle api "run the tests"` puts the text in the queue |
| Live view for tmux or ssh | `smif watch` |
| See what smif did | `/smif` in a session, or `smif stats` |
| Try the phone ping | `smif notify test` posts one ping to `daemon.notify_url`. The daemon does not have to run |

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
