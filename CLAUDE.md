# smif — repo guide

Go daemon + CLI + MCP server: messaging between Claude Code sessions with hard isolation, and a session view for coding agents. Spec:
`docs/rfc.md` (contract), decisions `docs/adr/`, verified CLI flags `docs/compat.md`. The README stays short: detail goes in `docs/{usage,messages,agents,safety,numbers}.md`.

## Commands

- `make build` → `bin/smif` (CGO off, static) · `make install-local` → `~/.local/bin/smif`
- `make check` → gofmt gate (`make fmt-check`), vet, staticcheck, `go test -race -count=1 ./...` (e2e builds the daemon with `-race`; `-count=1` because go test cannot see that build, so a cached e2e would hide a daemon change), config gates
- `make e2e` · `go test -run TestA7 ./e2e/` for one acceptance scenario · `make check-linux` (Docker; offline from the host module cache, safe in a worktree)
- `smif` (= `smif ui [--interval 1s]`) live human view; in a pipe plain `smif` prints one summary (needs a TTY; `NO_COLOR=1` drops colours, keeps bold/reverse)
  · `go test -run TestUIOverviewLive -v ./e2e/` prints it live · `go test ./internal/ui -update` rewrites goldens
- `smif stats [--days N] [--json]` counts per agent/day (`stats.get`, ADR-0011) · `go test -run TestStatsCountsKnownTraffic ./e2e/`
- `smif sessions` · `smif prompt [--when-idle] <session name | agent> <text>` type into an idle session (Terminal.app, iTerm2, tmux: `terminals.go`, ADR-0012; herdr: `herdr.go`, ADR-0018) · `go test -run TestSessionPrompt ./e2e/` · `go test -run TestHerdrLevel ./e2e/`
- `smif notify test` one ping to `daemon.notify_url` (no daemon needed)
- `smif watch [--events] [--once] [--last N]` plain text for pipes/tmux/ssh · `go test -run TestWatchEventsLive -v ./e2e/`
- `make bench` (tiers S/R/T on the fake, $0, `bench/README.md`) · `make bench-real` (+ actual tokens on haiku via the login)
- `make cover` · `make smoke-real` (real `claude -p` on haiku, `SMIF_E2E_REAL=1`, ~$0.01)
- `make release` builds `dist/` from the tag on HEAD and publishes nothing, steps in `docs/releasing.md` · `make release-check` / `make release-snapshot` (GoReleaser `GORELEASER_VERSION` v2.18.3 via `go run`, local only, ADR-0001; must pass with `GOTOOLCHAIN=local`: v2.19.0 needs go 1.27.3)

## Layout

- `cmd/smif/` cobra CLI; `daemon`, `mcp`, `hook`, `statusline` are subcommands of the one binary
- `internal/ui/` `smif ui` (Bubble Tea v2): `home.go` the start screen (groups + preview + answer box +
  live worker card), `prompt.go` the `:` prompt (completion from agent intents); the six-panel overview is the
  `v` dashboard (tests start there with `Options{Dashboard: true}`); `Source` polls the socket as the human, `Actions` (reply,
  cancel, retry, thread) write, `Inspector` (inbox list, run, diff, transcript tail, apply) feeds the inbox,
  run screen (`run.go`) and transcript/diff renderers (`transcript.go`); `events.go` follows `audit.since`
  (EVENTS panel, `watch --events`), `digest.go` the digest screen, `watch.go` the `watch` summary; model tests
  use fakes and 120x40 goldens; `layout_test.go` renders every screen at 80x24 and 160x50
  (`testdata/layout/`); `theme.go` holds every style (`SetupColor`, NO_COLOR), `help.go` the key list
  (`TestHelpListsEveryKey`), `layout.go` the fitting (footer, header, columns); live tests `e2e/ui_test.go`
  (overview), `e2e/ui_actions_test.go` (actions), `e2e/ui_cancel_test.go` (cancel a running run), `e2e/ui_run_test.go` (inbox, thread, run, diff, apply),
  `e2e/ui_events_test.go` (events, digest, `watch`), `e2e/ui_tour_test.go` (every screen and action, five agents;
  `SMIF_TOUR_DIR=<dir>` keeps each screen as text)
  `parts.go` the prompt box's parts (ADR-0020: paste, image, dropped file, warnings, `@path` prompt), `partsview.go` their keys and screens · `history.go` the `R` screen (ADR-0021)
- `internal/daemon/` wiring: ingest + routing (`ingest.go`), scheduler loop and outcomes (`dispatch.go`),
  RPC methods (`methods.go`), retention (`gc.go`)
- `internal/store/` SQLite (modernc, WAL, synchronous=FULL), migrations, the state machine (`transitions`)
- `internal/router`, `internal/scheduler`, `internal/supervisor` (process groups), `internal/runner/{claude,command}`
- `internal/harness/` Provider interface and registry (ADR-0019): required `Info`, `Discover`, `Read`; optional `Stater`, `Hooker`,
  `Forker`, `Stamper`, `Checker`, `Factser`; `ReadOnly` is the typing gate; `listed.go` the process names of the Listed level; `herdr.go` the agents whose herdr idle counts (others read `unknown`, no typing; an agent with no provider stays read only until `herdrChecked` in `internal/daemon/herdr.go` names it). `internal/harness/claude/` the Claude Code provider (session files,
  transcripts, file status, fork folder, process name). Mail, workers and `smif_ask` stay on `internal/runner/claude`.
  `internal/harness/{qwen,opencode,copilot,droid}/` read-only providers from a source reading (unverified, `docs/compat.md`), `codex/` the same but verified on 0.166.0, so it types when idle; their `testdata/report-*` are hand-built from that reading. `read.go` holds the shared transcript helpers (`ScanTail`, `TurnLog`, `FileCache`); `proc.go` the process list and `MatchProcs`, for agents with no file that links a pid to a session
- `internal/runner/codex` the read-only `codex exec` worker, `harness = "codex"` in a profile (ADR-0022)
- `internal/workspace` worktrees and branches · `internal/install` settings.json edits, launchd/systemd
- `testdata/fakeclaude/` scripted `claude -p` stand-in (`#fake:` directives in the message body), also `claude agents` (`agents.json` in `$SMIF_FAKE_LOG`) · `testdata/fakeherdr/` herdr stand-in (`SMIF_HERDR=<path>`, `""` = no herdr)
- `e2e/` acceptance scenarios A1–A15, M1 kill -9, chaos; each test gets its own `/tmp/mr-*` root

## Rules

- Never call the real `claude` or `codex` in automated tests; use a fake. Real calls only behind `SMIF_E2E_REAL=1`.
- Nothing from a message may reach argv: the runner builds argv from validated config only (R-ISO-3).
- No MCP tool may read inbound mail (D4); `internal/mcpserver` test asserts the exact tool list.
- No experimental Claude Code features (channels, `--dangerously-*`, SendMessage, Monitor): A14 greps for them.
- Every message state change goes through `store.Tx.Transition` (audited in the same transaction).
- Deviation from the RFC = an ADR in `docs/adr/` + the matching edit in `docs/rfc.md`, same commit.
- Commit author `developer@appnice.dev`; stage explicit paths.

## Gotchas

- Session prompts (`internal/daemon/sessions.go`) type into a real terminal: the `+` foreground check on the
  claude pid must pass first; e2e uses `daemon.terminal = "log:<file>"`. Only the human token sets a session's tty/pid/turn.
  `session.list` asks the providers; Claude Code's are `~/.claude/sessions/<pid>.json` (`internal/harness/claude`; e2e uses `$CLAUDE_CONFIG_DIR`).
  An unverified provider, or one with no state source, is read only (`harness.ReadOnly`): no typing, no fork, `go test -run 'Provider|Unknown' ./internal/daemon/`.
  `SessionView.Unverified` is the mark. `go test -run Unverified ./internal/daemon/` · `go test -run TestUnverifiedProviderLevel ./e2e/` · doctor: `HealthView.Providers`, an unverified format is `unchecked`, never a failure.
  Codex, Qwen Code and Droid have no state field: a tool call with no result is `unknown`, because a file cannot tell it from an approval prompt. OpenCode and Copilot do the same for an open tool; Copilot's `permission.requested` gives `waiting`. Codex, OpenCode and Droid match by `ps` and folder (`harness.MatchProcs`), a machine with no such files runs no `ps`. OpenCode reads SQLite with `mode=ro`, never `immutable=1`, and never opens `service.json` (it holds a password): `ps` shows the server.
  Levels herdr and listed: `internal/daemon/scan.go` (`liveSessions` = `fullSessions` + the scan; the watchers read only `fullSessions`). `Presence.Level` "" is full; a scanned row has no presence row, steps or fork. `SMIF_NO_AGENT_SCAN=1` turns the scan off (e2e does, so the host's agents stay out of a test).
  Answers: `readTranscript` keeps only the text after the last tool call; the UI draws it in `internal/ui/answer.go`.
  Facts (ADR-0021): `harness.Factser`, `internal/harness/claude/facts.go` reads only appended bytes (`factCursor`); `Dirty` runs git in the background (`internal/daemon/facts.go`), because the status line also goes through `sessionView`. `factsLine`, `contextMark` in `internal/ui/live.go`. `go test -run TestSessionFacts ./e2e/`.
  What a session does now: `Provider.Read` → `SessionView.Activity` (steps, pending call); state cells, counts, `tab` in `internal/ui/live.go`;
  notifications, the status line's session names and the watchdogs in `internal/daemon/session_watch.go` (ADR-0014, 0015);
  saved prompts (`p`, `@name`) and new sessions (`n`, `session.new`, `terminal.Open`) in `internal/ui/quick.go` (ADR-0015). `b` groups SESSIONS by harness (`sessionSection`, `live.go`). `[harnesses.<name>]` in the config feeds the Listed level (`Config.ListedProcesses`, `harness.ListedProcessWith`) and `session.new`: the daemon builds argv in `newArgv` from `Config.HarnessCommand`, never from the call; `session.harnesses` lists what `n` offers (ADR-0021). `go test -run TestCustomHarness ./e2e/`.
- No config = defaults, no agents; `smif`/`sessions` start the daemon (`autostart.go`, off with `SMIF_NO_AUTOSTART=1`,
  which the e2e harness sets). `daemon.lock` allows one daemon per data dir. README media: `docs/img/{hero,chat}.gif` from `site/media` (ffmpeg, `site/README.md`), `flow.gif` from the site; logo `docs/brand/make.py`; macOS notification icon `docs/brand/icns.sh` → `internal/notify/smif.icns` (`Smif.app`, `internal/notify/app.go`).
  Busy sessions: `session.send` / an ask to a session's name → a fork worker (`session_ask.go`, ADR-0013); `go test -run TestSessionAsk ./e2e/`.
  Send when idle (ADR-0021): `internal/daemon/session_queue.go` (`queueLoop`, its own goroutine: typing can take 11 s; it reads `liveSessions` only with items queued), `internal/store/session_queue.go` (`QueueMove` audits each state change), `internal/ui/queue.go` (`ctrl+q`, `ctrl+f`, `x`). `session.list` fills `SessionView.Queue`, `sessionView` does not (the status line stays cheap). `cleanParts` keeps files a queued line names. `go test -run TestSessionQueue ./e2e/`.
- Background sessions (ADR-0021): `internal/harness/claude/background.go` runs `claude agents --json --all`, at most once in 5 s, in one goroutine, so `Discover` never waits. `Session.AttachID` is the mark, `Presence.AttachID` and `SessionView.Background` carry it. The list beats the hooks. A done row's time is its last answer, never the file's time. `session.attach` (`sessions.go`) takes the id from the list. `testDaemon` sets `Command` nil, so no unit test reaches the real claude. `go test -run Background ./internal/harness/claude ./internal/daemon ./internal/ui` · `go test -run TestBackgroundSessions ./e2e/`.
- History and restart (ADR-0021): `harness.Historian`, `internal/harness/claude/history.go` (head 1 MB + tail 256 KB, an index in memory by size and time; search reads whole files through `eachLine`). `internal/daemon/restore.go`: `session.history`, `.search`, `.resume`, `.closed`, `.reopen`, and `trackOpen` from `watchSessions`, which writes `open-sessions.json`. `internal/ui/history.go` is the `R` screen. `resumeSession` is the one place that builds `--resume` argv, from the index and the config. `go test -run 'History|Search' ./internal/harness/claude` · `go test -run TestHistoryRestart ./e2e/`.
- Phone ping (ADR-0021): `internal/notify/ping.go`. `Daemon.pinger` takes each event in `notify` and `notifySession`, the URL from the config each time. `Send` never waits. The body comes from `title(e)`, never `Summary`. No error or log line may name the URL: `pingError` drops the net/http text, `notifyURLProblem` the url.Parse text. `go test -run Ping ./internal/notify ./internal/daemon` · `go test -run TestPhonePing ./e2e/`.
- macOS Unix socket paths max 104 bytes: tests use `/tmp/mr-*` and `SMIF_SOCKET`.
- Hooks must print nothing and exit 0 fast; the status line must stay under ~300 ms.
- `--resume` only finds a session from the same cwd: sessions are kept per workspace dir (ADR-0005).
- Lock order: `Daemon.mu` before `store` (never call `wakeWaiters`/`notify` while holding `Daemon.mu`).
- UI: a new key goes into `help.go` too (the help test fails otherwise). A meaning shown by a style also needs
  text (`▶`, `>`, `[tab]`, `DAEMON DOWN`), because NO_COLOR or a dumb terminal removes the style.
- UI: Bubble Tea v2 + Lip Gloss v2 (`charm.land/...`). `View()` returns `tea.View` (alt screen set there);
  tests read `Render()`. Keys are `tea.KeyPressMsg` (match on `String()`, text in `.Text`); a paste is
  `tea.PasteMsg` and only the reply box takes it. Without a TTY, v2 needs `tea.WithWindowSize` to draw.
  `TestStartupSendsNoQuery` checks the real binary starts and quits fast in a silent PTY.
