# ADR-0021: Harness view, facts, queue, background sessions, history, phone ping

**Status:** accepted, 2026-11-05

**What.** The trial shows the session view is the product: 109 prompts typed, 48 tabs shown, 1 fork ask, 3 messages
since 2026-11-03. Six additions make that view more useful. None of them calls a model. None of them answers a
permission prompt.

**Why these.** Forums and issues name review, lost sessions and walking away as the pains. The owner's own 21 days:
peak 15 sessions at once; 28 of 37 sessions with edits ended with no test after the last edit, 23 of 37 with no
commit after it; 27 of 53 sessions span more than a day; peak context median 169k, 14 sessions over 400k.
Overlapping edits were rare (3 in 21 days), so collision detection is out. `claude agents` shows only background
sessions; smif shows only interactive ones.

## 1. Any coding agent, grouped by harness

- `b` on the home screen toggles the SESSIONS group: by root folder (default) or by harness. Each harness has a
  header with its count. The choice stays for the UI run. `/` also matches the harness name.
- A row names its harness unless it is a full-level claude row (already so in `live.go`).
- `[harnesses.<name>]` in the config adds an agent with no code: `process = ["omnis"]`, optional `command = "omnis"`.
  It shows at the Listed level. A name that a built-in provider owns may set `command` only. Validate the names.
- `n` asks which harness when more than one is available: claude, plus each known or configured harness with a
  command that `exec.LookPath` finds. Left and right change it. Claude stays the default and the only one with
  `--name`. `session.new` takes a harness *name*. The daemon builds argv from the config or the built-in table,
  never from the call (R-ISO-3). An unknown name is refused.

## 2. Facts next to an answer

`SessionView.Facts` (nil when unknown): what the transcript and git show, from the newest human prompt on.

- `Files` distinct files that Edit, Write, MultiEdit and NotebookEdit touched; `Commits` Bash calls with `git commit`.
- `Tests`: `passed`, `failed` or `none`. It is `passed` or `failed` only when a test command ran after the last edit
  of a non-doc file (`.md`, `.txt` and `docs/` do not count), from its tool result. No such edit: empty, shown as nothing.
  Test commands: a built-in pattern (`go test`, `make check`, `make test`, `pytest`, `cargo test`, `npm test`,
  `npm run test`, `vitest`, `jest`, `gradle test`, `flutter test`) plus `daemon.test_commands` (list of regexes).
- `Dirty`: changed paths from `git status --porcelain` with `GIT_OPTIONAL_LOCKS=0`, 2 s timeout, read when the
  transcript stamp changes and the session is not busy. Any failure leaves it out.
- `ContextTokens`: input + cache read + cache creation of the newest assistant record.

An optional `harness.Factser` gives the transcript part, Claude only. Read appended bytes only (per-file cursor).
Do not parse a whole large transcript each poll.

The preview shows one line under the answer, for example
`4 files · 1 commit · tests not run since the last edit · 3 uncommitted · 412k context`. A row shows context only
over `daemon.context_warn` (default `200k`, `off` turns it off). The UI states facts. It does not judge the answer.

## 3. Send when idle

A busy or waiting session takes a prompt in a queue. This amends ADR-0013: `enter` in the box of a busy or waiting
session queues, `ctrl+f` asks a read-only fork (where a fork can run). `smif prompt --when-idle` does the same.

- Migration 006, table `session_queue`: id, session id, text, created, state (`queued`, `sent`, `cancelled`,
  `expired`, `gone`), error. Every state change writes an audit row in the same transaction.
- `session.queue`, `session.queue.cancel`: human token only. No MCP tool. Inbound mail never reaches it.
- The watcher sends the oldest item 2 s after the session reads idle, with the same gates as `session.prompt`:
  idle again, foreground check, not read only. One item per idle. If a gate refuses, the item stays and shows why,
  retried at most every 30 s.
- An item expires after 24 h. A session that ended turns its items `gone`, with a notification.
- A row shows `⧖ 2 queued`. The chat lists the items, and `x` removes one. Parts flatten into the line when queued.
  Parts need an idle session today, so the 24 h cleanup of `paths.PartsDir()` must keep files of queued items.

Built 2026-11-05. The choices this section left open:

- `x` is a letter while the box has the keys. So `ctrl+q` moves the cursor to the items, and there `x` removes one.
  `x` on a home row opens the chat with the cursor there. The saved prompts (`p`) and `: prompt` follow enter.
- The table also keeps `agent` and `name`, because a gone session has no row to name it in the notification.
- An item turns `sent` before the first key, so a crash or a cancel cannot type it twice. It goes back to `queued`
  only when the terminal call fails. A failure after the keys went in leaves it `sent`, the row shows the existing
  `unsent` mark, and no next item goes until a new turn clears that mark.
- A session counts as ended after 5 s out of the live list. One missed look does not end its items.
- A failed or hung herdr call gives no rows, and a scan result does not show why. So the 5 s count starts for a
  herdr row only when the process of that row ended. The watcher keeps the row's pid in memory. While the process
  lives, the item stays `queued`, and when the row comes back it reads idle for 2 s again. Full sessions keep the
  5 s rule. If the process lives and the row never comes back, the item expires after 24 h. Two limits: a daemon
  restart clears the pids, so an outage right after a restart still uses the 5 s rule, and so does a row lost
  before the first look of the watcher. A reused pid reads as live until the 24 h expiry.
- The UI runs the parts cleanup, so it keeps every file that a queued line in its snapshot names.
- Sent, cancelled, expired and gone rows leave with `daemon.message_retention`. A refusal writes an audit row only
  when the reason changes.

## 4. Background sessions

`claude agents --json --all` is the source (seen on 2.1.318). A row has `id`, `cwd`, `kind`, `startedAt`, `sessionId`,
`name`, optional `pid` and `status`, and `state`: `working`, `blocked`, `done` and others. Unknown states read `unknown`.

- The Claude provider adds rows with `kind = "background"` to Discover. It reads the list at most every 5 s, in the
  daemon, with a 5 s timeout. A failed or missing command gives no rows and no error.
- `SessionView.Background` marks them. `blocked` is `waiting`; `working` is `busy`; `done` and `idle` are `idle`.
  Done rows are hidden unless newer than 24 h, and an unread answer keeps them.
- Reading uses the normal transcript route, so answers, steps and facts work. A background row has no tab: smif
  never types. `o` opens `claude attach <id>` in a new tab through `terminal.Open`, with the id checked against the list.
- Notifications and `tab` treat them like any session.

Built 2026-11-05. The choices this section left open:

- `Discover` reads only the last result, because it runs under the status line. One goroutine runs the command, and
  the 5 s count from the start of a run. The first list after a start has no background rows.
- The list decides a background row's state, also over a newer hook report. Its time is when a read first saw that state.
- A done row's time is its last answer, else its start. The list gives no end time, and Claude Code 2.1.318 writes
  to a transcript long after the end (`docs/compat.md`), so the file's time is no answer.
- An unread answer is the UI's seen mark older than the answer. With no mark the UI never showed the session, and
  the row goes after 24 h. Otherwise a first run would list every old session.
- A row with a bad `id` or `sessionId` gives no row, because the id reaches argv. A session with an interactive row
  keeps that row, because it has a tab.
- `session.attach` is a new human-only RPC. The call names the session and a terminal, as in `session.new`. A missing
  folder is refused. No MCP tool. Also no fork: a background row refuses `session.send` and an ask to its name.
- `o` opens the tab in the UI's own terminal, else in the daemon's default.

## 5. History and restart

- `session.history`: Claude Code sessions from `~/.claude/projects/*/*.jsonl`, newest first, no subagents, no
  `sdk-cli` entrypoint. Each row: id, folder, name or first prompt (one line), last time. Index in memory by mtime.
  Read only the head and the tail of each file.
- `session.search`: text search in user and assistant text, newest file first, stop at 50 hits or 10 s.
- `R` opens the history screen: `/` filters, `ctrl+s` searches the text, `enter` reopens the session.
- `session.resume`: human only. The id must be in the index. Runs `claude --resume <id>` in the session's folder
  through `terminal.Open`. A missing folder is refused.
- Restart: the daemon writes the live interactive sessions to `open-sessions.json` in the data dir, on change. At
  start, sessions in that file whose process is gone are the restore list. So are two or more sessions that vanish
  within 10 s while the daemon runs (a terminal crash). The home screen shows `12 sessions closed · R reopens`.
  The history screen shows them first, marks a session that was busy as `died working`, and `A` reopens all of them,
  at most 20, 500 ms apart. A session closed alone with `/exit` is not on the list.

Built 2026-11-05. The choices this section left open:

- The session's folder is the first `cwd` in the file, because `--resume` finds a session only there. A later `cwd`
  can be a subfolder or a worktree.
- A row's time is its last user or assistant record, else the file's time. The file's time alone is no answer
  (section 4).
- `custom-title` beats `agent-name`, and both beat the first prompt. Claude Code writes them at any place, so a title
  only in the middle of a file over 1.25 MB does not show. The 32 titled `cli` files under 50 MB in the owner's files had it in the head
  or tail. A row with no text shows the short id. A row with no `cwd` does not show: it has no place to resume.
- The head is 1 MB and the tail 256 KB. An `sdk-` entrypoint ends the read of a file at once. A file name that is not a
  UUID is no session, so old `agent-*.jsonl` files stay out and no id can start with `-`.
- A search is one hit per record and reads no text index. A record over 16 MB is passed over. Search has no limit per
  file, so one long session can fill the 50 hits. The result says `hits` or `time` when a limit stopped it.
- `session.resume` also refuses a session that runs now: two processes would write one transcript. The row shows
  `open`.
- `A` is the new human-only RPC `session.reopen`, and `session.closed` feeds the home line. The daemon runs the
  500 ms gaps, so a UI that quits does not stop them. Past the first 20, `A` again takes the rest.
- A reopen that the daemon refuses takes the session off the list, and the screen shows why. A terminal that fails
  ends the run, and the sessions not reached stay.
- `open-sessions.json` holds Claude Code interactive rows with a pid, and the restore list too, so a second restart
  does not lose the list. A list entry leaves when its session is live again, when it is reopened, or after 24 h.
- A session whose process still runs did not vanish: `/clear` gives it a new id. A session with no transcript has
  nothing to resume, so it stays off the list. At a start, `ProcessAlive` reads a reused pid as live, so after a reboot
  such a session can be missing from the list.
- The list has no key that clears it. `A` empties it, and the rest goes after 24 h.

## 6. Phone ping

`daemon.notify_url` (empty by default) gets a POST for the same events as an OS notification, reminders included.
The body is the agent or folder name and what happened, `shop waits for you`, with no prompt, answer or command text, so
a public ntfy topic leaks only names. Headers `Title: smif`, `Tags: bell`. 5 s timeout, no retry. Only `https`, or
`http` to localhost. Never log the URL. `smif notify test` sends one. No reply and no approval goes back.

Built 2026-11-05. The choices this section left open:

- The body is the title of the OS notification with no `Smif: ` in front, so it covers every kind, mail kinds too. It
  comes from the kind and the name, and never reads the summary.
- For a session, `name` is the smif agent, else the folder, and never a title: an agent can make a title from a
  prompt (the hand-built Droid fixture has title `Fix the test` for prompt `fix the test`).
- `daemon.notify = "none"` does not turn the ping off, so a person can have only the phone. `daemon.session_notify`
  and the watchdog keys choose the events, as for the OS.
- `Pinger.Send` starts one goroutine per ping and returns at once. At most 16 run; a further ping is lost with a log
  line. A queue would hold old news for a server that is down.
- The daemon reads the URL from the config for each ping, so `smif reload` needs no new sender.
- A redirect is not followed, because it could lead from `https` to `http`. A 3xx counts as a failure.
- Any 2xx is success. The log line has only the kind and a fixed reason: `no answer in 5 s`, `the connection failed`,
  or `the server answered 503`. net/http errors quote the URL, and dial errors name the host.
- `smif notify test` posts from the CLI process, so it needs no RPC and no daemon.
- A URL may hold a user and a password. Go sends them as basic auth, and no check is needed for it.
- `unknown keys` in a config error now names the key and its line, not the source line, because a misspelt
  `notify_url` would put the URL in the audit row of a failed reload.
- `smif doctor` shows one `[info]` line that the ping is on. It shows no URL.

## Trade-offs

- `claude agents --json` is research preview: a change gives no background rows, not a failure.
- Facts read a transcript and can miss a test run through a script with another name, so `daemon.test_commands` exists.
- The queue types into a tab with no human present. The same gates apply as for a prompt, and only the human can add items.
  A partly typed line stays in the input, and the queued text lands behind it (the ADR-0012 limit). `/clear` gives a
  session a new id, so its items read as gone. A terminal call that fails after the text and before Enter can leave
  text in the input, and the next try adds the same text.
- `notify_url` is the one thing that sends data off the machine, and only when the user sets it.
