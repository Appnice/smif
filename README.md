<div align="center">

# Mailroom

**One terminal view of all your Claude Code sessions.<br>Read only the final answers. Write to any session, busy or idle.**

[![Go](https://img.shields.io/badge/Go-1.27-00add8?logo=go)](go.mod)
[![Platforms](https://img.shields.io/badge/macOS%20%7C%20Linux-single%20binary-555)](#install)
[![License: MIT](https://img.shields.io/badge/license-MIT-d29922)](LICENSE)

</div>

Run `mailroom`. It finds every open Claude Code session on the machine, in any folder, with no setup:

```
SESSIONS · 9 · 1 new answer
  ~/work · 8
    ○ api          Done: the migration ran and the tests pass.        2m
    ◓ web          working · 4m                                       4m
    ● docs         The new page is up at /guides/install.             1m
  ~/side · 1
    ! scraper      waits for you in its tab                          12m
```

| You do | Mailroom does |
|---|---|
| Select a session | Shows your last prompt and only the final answer: no steps, no tool output, a count of tools. |
| `enter` | Opens a chat with the session: the conversation and a box to write in. |
| Write to an **idle** session | Types your text into it and presses Enter, as if you typed it in its tab (Terminal.app, iTerm2 or tmux). |
| Write to a **busy** session | Does not type. A worker resumes a fork of the session (`claude -p --resume … --fork-session`): it knows what the session knows, answers read-only, and the session is not touched. |
| `o` | Brings the session's Terminal tab to the front, for a permission prompt or a question. |

### Messages between sessions

Sessions and scripts can also send messages to a project. Mailroom sorts them with your rules, and no message
text enters a session that you type in:

<div align="center">
<img alt="Three messages arrive for one agent while you work in its session: a script answers an ask, a read-only worker examines a crash, and a decision waits for you. Your session shows only counts." src="docs/img/how-it-works.svg" width="560">
</div>

| Message | Handler | Result |
|---|---|---|
| An ask with a known answer | **command**: a script, no model | The asking session gets the answer in about 25 ms. |
| A crash, an error, a code question | **worker**: a headless `claude -p` with a locked tool set | You get a summary in `/mailroom`. A fix is a branch for you to merge. |
| An ask to a session's name | **fork** of that session | The asker gets an answer from the session's context. Nothing is typed into it. |
| Information (a build is done) | **digest** | You get one line in `/mailroom`. |
| A decision for a human | **escalate** | You get a count in the status line, an OS notification and an item in the UI. |

## Numbers

All numbers come from `make bench` on one laptop with a fake model. The benchmark costs $0. Each scenario
ran one time. The full report is in [bench/results](bench/results/2026-11-02-3711a61/report.md), and the method
is in [docs/rfc-benchmark.md](docs/rfc-benchmark.md).

| Latency | p50 | p99 |
|---|---|---|
| Ask answered by a command, 20 per second | 25 ms | 30 ms |
| Message stored and routed, 200 per second | 10 ms | 23 ms |
| Hook in your session, at that load | 10 ms | 21 ms |
| Status line, at that load | 10 ms | 15 ms |
| Mailroom time around one worker run | 28 ms | |

| Tokens | |
|---|---|
| Command handler | 0 |
| Mailroom text in one worker run | about 570 |
| Mailroom tool definitions in your session (one time) | about 500 |
| Same question, Mailroom worker vs. plain `claude -p` in the same repo | **15 450 vs. 63 922** context tokens |

| Failure test | Result |
|---|---|
| `kill -9` on the daemon with 92 messages in flight | 0 lost. All done 13 s after the restart. |
| 500 copies of one crash event | 2 worker runs |
| Two workers that always ask each other | Stopped after 4 runs |
| A marker string in 20 inbound messages | Found 0 times in your session |

## Install

You need Claude Code 2.1 (checked with 2.1.316) and git, on macOS or Linux.

**A release binary** (darwin and linux, amd64 and arm64), from [Releases](https://github.com/appnice/mailroom/releases):

```sh
curl -L https://github.com/appnice/mailroom/releases/latest/download/mailroom_$(uname -s | tr A-Z a-z)_$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/).tar.gz | tar -xz mailroom
mkdir -p ~/.local/bin && mv mailroom ~/.local/bin/
```

**Or with Go** (1.27.2 or newer): `go install github.com/appnice/mailroom/cmd/mailroom@latest`

**Or from source:** `git clone https://github.com/appnice/mailroom && cd mailroom && make build && make install-local`

Then:

```sh
mailroom          # the session view works now, with no setup
mailroom init     # optional, for messages: finds your git repos and gives each one an agent id
mailroom install  # hooks, status line, /mailroom, MCP server, launchd or systemd
mailroom doctor   # checks the setup
```

`mailroom install` makes a backup of each file that it changes. `mailroom uninstall` puts back each file
byte for byte.

## Use

| From | How |
|---|---|
| A Claude Code session | Say "ask the web agent if v2.3 is live". The MCP tools are `mailroom_ask`, `mailroom_send`, `mailroom_await`, `mailroom_agents` and `mailroom_status`. |
| A shell or a script | `mailroom ask --to shop --intent store.status --subject "Live?" --wait 30` |
| A crash monitor, CI, cron | `mailroom emit --as monitor --to shop --intent crash.report --subject "NPE" --dedupe-key npe-1 --body - < report.md` |
| The terminal UI | `mailroom` |
| All sessions | `mailroom` (the UI), or `mailroom sessions` in a shell |
| An idle session | `mailroom prompt api "run the tests"` types the prompt into the session with that name, as if you typed it there (Terminal.app, iTerm2 or tmux) |
| Your results | `/mailroom` in a session, or `mailroom stats` |

<details>
<summary><b>The terminal UI</b></summary>
<br>

<img alt="The mailroom home screen: items that need you, a running worker with its live tool calls, the queue, items done today, dead letters" src="docs/img/home.svg" width="100%">

- The list shows the items that need you first, then your sessions, running workers, the queue, the items done
  today and dead letters. The right pane shows the selected item. For a running worker, it shows the live tool calls.
- `a` answers, `c` cancels, `t` retries, `e` marks as read. `enter` opens the thread or the run: record,
  transcript and branch diff. On a run, `a` merges the branch after a y/N question.
- The SESSIONS group shows every open Claude Code session, grouped by root folder, with its name, state and the
  first line of its last answer (`●` = new, `!` = it waits for you in its tab). Sessions need no config: an agent
  is needed only for messages and workers.
- `enter` on a session opens the chat. `s` writes from the preview. Idle: typed in. Busy: a worker with the
  session's context answers in the chat. `o` (in the chat `ctrl+o`) brings its tab to the front.
- `:` opens a command prompt with tab completion: `ask`, `tell`, `prompt`, `answer`, `retry`, `agent`, `filter`, `digest`.
- `[` and `]` change the agent. `/` filters. `v` opens the dashboard (agents, runs, audit log). `?` shows all keys.
- In a pipe, `mailroom` prints one summary. `mailroom watch` gives a live view for tmux or ssh.

</details>

<details>
<summary><b>Stats: how much work Mailroom does without you</b></summary>
<br>

`mailroom stats` shows the last 14 days. Each message has exactly one outcome.

```
  inbound       11 messages (+4 repeats merged), 2 replies
  without you   4 (50% of the work)  command 1 · digest 1 · worker 2
  needed you    3  answered 1 · waiting 2 · cancelled 0
  runs          7 worker, 1 command (1 failed)
  tokens        5.2k in (incl. cache) · 250 out
```

Use `--since 2026-01-01` to set the start date. Use `--json` for scripts.

</details>

<details>
<summary><b>Configuration</b></summary>
<br>

The file is `~/.config/mailroom/config.toml`. Each agent has its own rules. The first rule that matches
the message is used.

```toml
[agents.shop]
repo = "~/dev/shop"
accept_from = ["monitor", "web"]

[[agents.shop.rules]]                # a script answers, no model
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["~/.config/mailroom/handlers/status.sh"]

[[agents.shop.rules]]                # a read-only worker examines crashes, one run for each problem a week
match = { kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "7d"

[[agents.shop.rules]]
match = { kind = "event", intent = "notice.*" }
handler = "digest"

[[agents.shop.rules]]                # all other messages wait for you
match = {}
handler = "escalate"
```

- **Agent of a session:** the repo of the session folder. For a different folder, start the session with
  `MAILROOM_AGENT=shop claude`.
- **Profiles** set the model, effort, tools, write scope (`none`, `worktree` or `direct`), turns and timeout.
  The built-in profiles are `triage`, `fix`, `answer` and `release`.
- **Command handlers** get the message as JSON on stdin. They run in the agent repo. They print
  `{"status":"done","summary":"…","reply":"…"}`. `mailroom init` installs one that answers `status.get` from git.
- To apply a change: `mailroom config validate`, then `mailroom reload`. You do not restart the daemon.

</details>

<details>
<summary><b>Safety</b></summary>
<br>

- **Your session.** No inbound message enters a session that a human drives. The only text that Mailroom types
  into a session is your own prompt. It types only into an idle session, and only when claude is in the foreground
  of its tab. A busy session, or an ask to a session's name, gets a read-only fork instead; the session is not
  touched. The session view reads Claude Code's session files and transcripts, which are not a documented
  interface: `mailroom doctor` checks their shape. Hooks print nothing. The status line
  shows counts only, for example `mail: shop 1 running · 1 needs you`.
- **Workers.** Claude Code flags lock each worker, not a prompt. `--tools` gives only the tools of the profile, so
  other tools do not exist. Hooks are off. No MCP server loads. Deny rules block the Mailroom files. A worker
  that can write gets its own git worktree, and you merge its branch. These flags are tested against the
  real CLI ([docs/compat.md](docs/compat.md)).
- **Loops.** A hop limit, a run limit for each thread, ping-pong detection, dedupe and rate limits stop runaway traffic.
- **Data.** SQLite with `synchronous=FULL`. Each state change writes an audit row in the same transaction.
- **Access.** A Unix socket with mode 0600, a peer uid check and one token for each agent. Agents cannot read
  the mail of other agents or the stats. There is no server, no cloud and no telemetry.

</details>

<details>
<summary><b>FAQ</b></summary>
<br>

**Does it cost money?** Workers use your Claude Code login. Command handlers and the digest use no model.

**Can a worker break my repo?** A read-only profile cannot write. The `fix` profile writes in its own worktree on
a `mailroom/<agent>/…` branch. You examine the diff and merge it. `mailroom apply` does not merge into a
dirty tree.

**What if the daemon stops?** All accepted messages are on disk. At restart, the daemon stops orphan workers and
tries their messages again. Hooks and the status line fail open: they print nothing and return immediately.

**How do I remove it?** Run `mailroom uninstall`. Your settings come back byte for byte. The data stays in
`~/Library/Application Support/mailroom` (or `~/.local/share/mailroom`) until you delete it.

</details>

<details>
<summary><b>Development</b></summary>
<br>

- `make check` runs gofmt, vet, staticcheck and the race tests, which include 15 acceptance scenarios on a fake
  `claude`. `make check-linux` runs the same checks in Docker.
- `make bench` runs the benchmark. `make smoke-real` makes one real haiku call.
- Documents: [spec](docs/rfc.md) · [decisions](docs/adr) · [acceptance report](docs/acceptance-report.md).

</details>

## License

MIT
