# ADR-0019: session view for other coding agents, through harness providers

**Status:** accepted, 2026-11-05. No agent other than Claude Code ran on this Mac, and the owner has no
access to them, so every file path and field for them comes from their source and docs, not from a real session.

**Context.** smif's session view reads Claude Code's own files: `~/.claude/sessions/<pid>.json`, the transcript
and hooks. The other agents store this differently, and most of them do not record on disk that they wait for
an approval. The same view for those agents needs a layer between the daemon and each agent's files.

**Decision.**

- A `Provider` in `internal/harness/<name>` holds what is specific to one agent. Required: `Discover` (pid,
  session id, cwd, name) and `Read` (turns, steps, final answer). Optional: `State`, `Hooks`, `Fork`. A provider
  that has no `Fork` has no side questions. Each provider also gives its `Info` (name, process name, verified).
  Two more optional parts: `Stamp`, a cheap check for change, so `session.list` reads only what changed, and
  `Check`, the format check for `smif doctor`.
- Claude Code becomes the first provider, with no change in what it does: acceptance scenarios A1 to A15 stay
  green. The `presence` table and `SessionView` get a `harness` field. The foreground check of ADR-0012 asks the
  provider for its process name in place of the fixed `claude`.
- Three levels of support, shown in the UI:

  | Level | Needs | The human gets |
  |---|---|---|
  | **Full** | A provider | State, steps, final answer, notifications, typing, a fork where one is safe |
  | **herdr** | The agent runs in a herdr pane | Name, folder, herdr's state, focus. Typing only for an agent a real session checked. No steps, no answer |
  | **Listed** | Its process is recognised | Name and folder, state `unknown`, no typing |

  The herdr level reads `herdr agent list`, so an agent herdr knows shows up without a provider. Its state comes
  from herdr's screen rules, so the UI marks it as such. A prompt there goes through `agent prompt`, which refuses
  an agent at an approval or question dialog.
- How a scan works (`internal/daemon/scan.go`). It reads `herdr session list`, `agent list` and `pane process-info`
  on every running herdr session, then `ps`. `working` maps to busy, `blocked` to waiting, `idle` and `done` to idle
  (only for an agent in the table of the safety rule below), every other word to unknown. Before each prompt smif finds the pane by pid again and asks `agent get`: only idle
  or done goes on, so a list that is a few seconds old cannot type. The result lives 2 s, and a new scan runs beside
  `session.list`; a call waits at most 200 ms, and only when the result is older than 6 s. No herdr, no server or a
  failing call gives no rows and no error. Only human calls start a scan: the status line and the notifications
  read the full sessions alone, so the herdr and Listed levels give no notification.
- A scanned row has no `presence` row. Its id is a hash of the herdr session and terminal id, or of the pid, so it
  stays the same from scan to scan. A pid or tty in common with a better row means one session: the row shows once,
  at the better level. A herdr row of an agent whose provider is unverified stays read only.
- The Listed table is herdr's process table (`internal/harness/listed.go`), with Claude Code left out. Only a process
  on a terminal counts: a process without one is a server or a daemon.
- State comes from the best source an agent has: service or API, then hook, then the transcript's end, always
  behind a check that the process lives.
- **New safety rule:** smif types only when the agent's state source can tell a pending approval from idle. Where
  it cannot, the state is `unknown`, not `idle`, and smif does not type. Typing into an agent that only looks idle
  could answer an approval prompt, and smif never answers one (ADR-0012). At the herdr level this needs a table
  (`internal/harness/herdr.go`, from the source reading): for an agent with no idle rule and no state source that
  covers permissions, herdr's idle only means that no screen rule matched, so smif maps idle and done to unknown.
  opencode, kilo, pi, omp, mastracode and kimi have no idle screen rule: only herdr's integration for them sets
  their state. So their idle counts only when `herdr integration status` says `current`, read with a 5 s cache for
  the rows and the check before typing (`herdrTurn`). `not installed`, `outdated`, `needs repair` and an unreadable
  answer all give unknown. `outdated` is left out because a file with no herdr code at that path reads
  `outdated (legacy < vN)`. The status shows only that the file is in place, not that the agent loaded it. `agent
  explain` cannot be the signal: a state set with `pane report-agent` leaves its output the same.
- **Unchecked herdr agents are read-only (amended 2026-11-05).** An agent with no provider takes no typing until a
  real session checks it, even when herdr's idle counts by the table above. Those screen rules come from a source
  reading and fake panes, and a wrong idle could answer an approval prompt. `herdrChecked` in
  `internal/daemon/herdr.go` lifts this per agent, with a `docs/compat.md` row. The table and the integration check
  still decide the state a row shows.
- **Unverified providers are read-only.** A provider that no real session has checked shows sessions and is
  marked `unverified`, and smif neither types into it nor forks it. Only a row in `docs/compat.md` with the
  version checked lifts this, in the provider's code.
  `session.list` carries the mark (`unverified: true`), and the UI and `smif sessions` show it. A focus stays
  allowed: it sends no key. An unverified provider also takes over from the herdr level: a herdr row of that
  agent keeps herdr's state but stays read only, so a Codex in a herdr pane no longer takes typing. The same holds
  for OpenCode, Copilot and Droid. herdr has no idle rule for Copilot or Droid, so this also keeps smif from typing
  into their approval dialogs.
- `smif doctor` reports each provider on its own line. A verified provider's changed format is a failure. An
  unverified provider's format is `unchecked`, also when the files differ from the source reading, because the
  fault can lie in the reading.
- Codex keeps no live file, so no file links a pid to a session. A `codex` process on a terminal owns the rollout
  in its folder that was written since the process started; `codex resume <id>` names its session in argv. Two
  processes in one folder take the rollouts newest to newest, which can swap them when the newer one resumed an
  older session. A process with no such rollout stays at the herdr or Listed level. `.jsonl.zst` rollouts are
  skipped: they are cold, and reading them needs a new dependency.
- OpenCode V2 has one server for every TUI and no link from a TUI's pid to a session, so a TUI matches like Codex: it
  owns the session in its folder that was updated since it started. State comes from `session_v2`'s time columns
  behind a live server, which `ps` shows as `<self> serve --service`. smif uses the database, not the server's HTTP
  routes: they need the password in `service.json`, and an unverified provider has no need to hold one. So smif never
  opens that file, cannot see a pending approval, and gives `unknown` for an open tool. The source reading proposed
  the `pid` in `service.json` for the liveness check; the process list replaces it, because a read of that file puts
  the password in a buffer. The session id `ses_...` holds `_`, so `harness.ValidID` allows it.
- Copilot holds `inuse.<pid>.lock` in the session's folder, so a lock names the process. A lock can outlive a crash:
  the pid must be alive, be `copilot`, sit on a terminal and not have started after the lock. Its log records
  permissions, so `waiting` comes from the log. It does not record a question to the human, so an open tool stays
  `unknown`. `assistant.turn_end` ends one model call, so only a turn whose message asked for no tool gives `idle`.
- Droid keeps no live file and matches like Codex, with `--resume` and `--cwd` read from argv. A `droid exec` transcript
  has no mark, so a TUI in the same folder can take it.
- From a transcript's end, a tool call with no result gives no state: a running tool looks the same as an
  approval prompt. The row shows `unknown` and the step. Codex with approval policy `never` is `busy` there,
  unless the call is `request_user_input`.
- A fork is used only on a route that leaves the source session unchanged. These change the source and are not
  used: `crush run --session`, `copilot -p --resume`, `gemini --resume`, `codex exec resume`.
- A fork's own hooks and sessions are tagged and hidden from the list.
- One `smif hook` handler takes `--harness <name>` with a small field map per agent, because most agents use
  Claude Code's hook names.
- SQLite stores are read with `mode=ro`. A file that holds a secret stays closed (OpenCode `service.json`), or its
  secret stays out of every struct, log and UI (Qwen `ipcToken`: the owner accepted on 2026-11-05 that the token sits in a read buffer during the parse, because no other file links a pid to a session). Kilo `daemon.json` and Auggie
  `~/.augment/session.json` have no provider, so smif does not read them.
- Mail, workers and `smif_ask` stay on Claude Code. This ADR covers the session view only.

**Order.**

1. The provider layer and the Claude provider. No new agent.
2. The herdr level and the Listed level. Every agent herdr knows shows up here.
3. Read-only, unverified providers from the source reading, on fixtures: Qwen Code, Codex.
4. The same for OpenCode V2, GitHub Copilot CLI and Droid.

Later, once someone has access: capture one real session for each provider, compare it with the fixtures, add
the `docs/compat.md` row, then add hooks, typing and forks for it. Gemini CLI and the rest stay on the herdr and
Listed levels until then.

**Consequences.** Each agent's private files can change without notice, as Claude Code's can: `smif doctor` checks
each provider's format and reports an error, not a wrong state. Live checks need each agent installed and logged
in. Tests use fixtures per provider, built from the source reading, so a green test says nothing about the real
file format. The herdr level depends on a product that overlaps with
smif: if its CLI changes, that level goes down and the rest stays.
