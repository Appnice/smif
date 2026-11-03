# mailroom — repo guide

Go daemon + CLI + MCP server: messaging between Claude Code sessions with hard isolation. Spec:
`docs/rfc.md` (contract), decisions `docs/adr/`, verified CLI flags `docs/compat.md`.

## Commands

- `make build` → `bin/mailroom` (CGO off, static) · `make install-local` → `~/.local/bin/mailroom`
- `make check` → gofmt gate (`make fmt-check`), vet, staticcheck, `go test -race ./...` (e2e builds the daemon with `-race`), config gates
- `make e2e` · `go test -run TestA7 ./e2e/` for one acceptance scenario · `make check-linux` (Docker; offline from the host module cache, safe in a worktree)
- `mailroom` (= `mailroom ui [--interval 1s]`) live human view; in a pipe plain `mailroom` prints one summary (needs a TTY; `NO_COLOR=1` drops colours, keeps bold/reverse)
  · `go test -run TestUIOverviewLive -v ./e2e/` prints it live · `go test ./internal/ui -update` rewrites goldens
- `mailroom stats [--days N] [--json]` counts per agent/day (`stats.get`, ADR-0011) · `go test -run TestStatsCountsKnownTraffic ./e2e/`
- `mailroom sessions` · `mailroom prompt <session name | agent> <text>` type into an idle session (Terminal.app, iTerm2, tmux: `terminals.go`, ADR-0012) · `go test -run TestSessionPrompt ./e2e/`
- `mailroom watch [--events] [--once] [--last N]` plain text for pipes/tmux/ssh · `go test -run TestWatchEventsLive -v ./e2e/`
- `make bench` (tiers S/R/T on the fake, $0, `bench/README.md`) · `make bench-real` (+ actual tokens on haiku via the login)
- `make cover` · `make smoke-real` (real `claude -p` on haiku, `MAILROOM_E2E_REAL=1`, ~$0.01)
- `make release-check` / `make release-snapshot` (GoReleaser `GORELEASER_VERSION` v2.18.3 via `go run`, local only, ADR-0001; must pass with `GOTOOLCHAIN=local`: v2.19.0 needs go 1.27.3)

## Layout

- `cmd/mailroom/` cobra CLI; `daemon`, `mcp`, `hook`, `statusline` are subcommands of the one binary
- `internal/ui/` `mailroom ui` (Bubble Tea v2): `home.go` the start screen (groups + preview + answer box +
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
  `MAILROOM_TOUR_DIR=<dir>` keeps each screen as text)
- `internal/daemon/` wiring: ingest + routing (`ingest.go`), scheduler loop and outcomes (`dispatch.go`),
  RPC methods (`methods.go`), retention (`gc.go`)
- `internal/store/` SQLite (modernc, WAL, synchronous=FULL), migrations, the state machine (`transitions`)
- `internal/router`, `internal/scheduler`, `internal/supervisor` (process groups), `internal/runner/{claude,command}`
- `internal/workspace` worktrees and branches · `internal/install` settings.json edits, launchd/systemd
- `testdata/fakeclaude/` scripted `claude -p` stand-in (`#fake:` directives in the message body)
- `e2e/` acceptance scenarios A1–A15, M1 kill -9, chaos; each test gets its own `/tmp/mr-*` root

## Rules

- Never call the real `claude` in automated tests; use the fake. Real calls only behind `MAILROOM_E2E_REAL=1`.
- Nothing from a message may reach argv: the runner builds argv from validated config only (R-ISO-3).
- No MCP tool may read inbound mail (D4); `internal/mcpserver` test asserts the exact tool list.
- No experimental Claude Code features (channels, `--dangerously-*`, SendMessage, Monitor): A14 greps for them.
- Every message state change goes through `store.Tx.Transition` (audited in the same transaction).
- Deviation from the RFC = an ADR in `docs/adr/` + the matching edit in `docs/rfc.md`, same commit.
- Commit author `developer@appnice.dev`; stage explicit paths.

## Gotchas

- Session prompts (`internal/daemon/sessions.go`) type into a real terminal: the `+` foreground check on the
  claude pid must pass first; e2e uses `daemon.terminal = "log:<file>"`. Only the human token sets a session's tty/pid/turn.
  `session.list` also reads Claude Code's `~/.claude/sessions/<pid>.json` (`discover.go`; e2e uses `$CLAUDE_CONFIG_DIR`).
  Answers: `presence.Conversation` keeps only the text after the last tool call; the UI draws it in `internal/ui/answer.go`.
  What a session does now: `presence.Read` → `SessionView.Activity` (steps, pending call); state cells, counts, `tab` in `internal/ui/live.go` (ADR-0014).
  Busy sessions: `session.send` / an ask to a session's name → a fork worker (`session_ask.go`, ADR-0013); `go test -run TestSessionAsk ./e2e/`.
- macOS Unix socket paths max 104 bytes: tests use `/tmp/mr-*` and `MAILROOM_SOCKET`.
- Hooks must print nothing and exit 0 fast; the status line must stay under ~300 ms.
- `--resume` only finds a session from the same cwd: sessions are kept per workspace dir (ADR-0005).
- Lock order: `Daemon.mu` before `store` (never call `wakeWaiters`/`notify` while holding `Daemon.mu`).
- UI: a new key goes into `help.go` too (the help test fails otherwise). A meaning shown by a style also needs
  text (`▶`, `>`, `[tab]`, `DAEMON DOWN`), because NO_COLOR or a dumb terminal removes the style.
- UI: Bubble Tea v2 + Lip Gloss v2 (`charm.land/...`). `View()` returns `tea.View` (alt screen set there);
  tests read `Render()`. Keys are `tea.KeyPressMsg` (match on `String()`, text in `.Text`); a paste is
  `tea.PasteMsg` and only the reply box takes it. Without a TTY, v2 needs `tea.WithWindowSize` to draw.
  `TestStartupSendsNoQuery` checks the real binary starts and quits fast in a silent PTY.
