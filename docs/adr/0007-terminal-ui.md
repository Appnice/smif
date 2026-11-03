# ADR-0007: Terminal UI for the human (`mailroom ui`)

**Status:** accepted, 2026-11-02

1. **What.** `mailroom ui` is a full-screen terminal view for the human: agents with queued, running,
   needs-you and dead-letter counts, spend and budget left, interactive-session presence, running runs with
   elapsed time, the needs-you queue, dead letters and recent activity. The RFC 7.3 CLI table gets a row.
2. **Data path.** The UI is a client of the daemon socket with the human token only
   (`auth.HumanPrincipal`). Each poll calls `agent.status` and `thread.list` (states `needs_human`,
   `dead_letter`, and the newest items). It adds no daemon method and no path through which an agent could
   read inbound mail (D4). It does not touch `~/.claude`.
3. **Refresh.** The UI polls about once a second (`--interval`). This is a human screen, not agent polling.
   A failed poll keeps the last data on screen, marks it stale and says `DAEMON DOWN`; the source redials
   on the next poll, so the UI survives a daemon restart.
4. **Stack.** Bubble Tea v1 (`github.com/charmbracelet/bubbletea v1.3.10`) and Lip Gloss
   (`github.com/charmbracelet/lipgloss v1.1.0`), plus `charmbracelet/x/ansi` and `x/term` for width and
   TTY checks. All are pure Go, so the `CGO_ENABLED=0` static build stays. v1 over `charm.land/*/v2`: v1
   is the long-stable API; a move to v2 is a separate change.
5. **Tests.** The model takes a `Source` interface; unit tests drive `Update`/`View` with a fake source and
   compare a 120x40 golden file (`internal/ui/testdata/`). `e2e/ui_test.go` renders the overview from a live
   daemon with the fake claude, stops the daemon (down state) and starts it again (recovery).
6. **Actions.** In NEEDS YOU and DEAD LETTERS, `enter` opens the item screen: the worker's question or the
   failure reason, the body, the thread and its runs (`thread.get`, reloaded on every poll). `a` replies
   inline (`message.reply`), `c` cancels (`message.cancel`, also from RUNNING), `t` retries a dead letter
   (`message.retry`). Cancel and retry ask y/n first; only `y` acts. All are existing human-only methods,
   so no new method. The source never sends a write twice: after a transport error it drops the connection
   and shows the error. The result of each action stays in the footer and the screen refreshes at once.
   The reply box is a small built-in editor, so `charmbracelet/bubbles` is not a dependency.
   `e2e/ui_actions_test.go` drives the model against a live daemon: reply, cancel and retry.
7. **Inspect and apply.** `enter` on RECENT ACTIVITY (or in the inbox) opens the thread screen of any
   message: every message with its body and attachments, and the runs with their results. `tab` selects a
   run; `enter` opens the run screen with three tabs: the record (`run.get`), the transcript tail and the
   branch diff (`run.diff`). `i` opens the inbox (`thread.list`, newest 200) with an agent and a state filter.
   The transcript is read from the file that `run.get` names (the last 256 KB), as `mailroom run
   --transcript` does: the UI runs on the daemon's host, so this needs no new method. `a` on a run with a
   worker branch asks y/N, then calls `run.apply`; a refusal (dirty tree) shows the daemon's reason on the
   screen. All are existing human-only methods. `e2e/ui_run_test.go` drives it with the fix profile.
8. **Events and digest.** The EVENTS panel and the digest screen (`d`, `m` marks read) use the new
   `audit.since` method and `digest.get` with `until`; see ADR-0008.
9. **Layout, colour and help.** Every screen fits 80x24 and 160x50 (`internal/ui/layout_test.go`, goldens in
   `testdata/layout/`). A cell or line too wide ends in `…`. Table columns shrink before the free-text
   column is lost, and AGENTS switches to short headers. The header drops pid and schema first. A cut footer
   keeps `? help` and `q quit`; the y/N question and the reply box show only `ctrl+c quit`. `NO_COLOR` (set
   and not empty) removes every colour but keeps bold, faint, underline and reverse; for that `cmd_ui.go`
   sets the plain ANSI profile, because Lip Gloss alone drops all styles under NO_COLOR. Each meaning a
   style shows is also text: `▶` for the focused panel, `>` for the cursor, `[1 RECORD]` for the open tab.
   `github.com/muesli/termenv v0.16.0` (pure Go, already pulled in by Lip Gloss) becomes a direct dependency
   for the profile. `?` lists every key of every screen and scrolls; `TestHelpListsEveryKey` keeps the list
   complete.
10. **No terminal query at start.** Bubble Tea v1.3.10 calls `lipgloss.HasDarkBackground()` in its package
    `init`. termenv then writes OSC 11 and DSR to the terminal and waits up to 5 s. A terminal that does not
    answer holds every `mailroom` command for 5 s, and `mailroom ui` loses the keys typed in that time.
    Package `internal/ui/noquery` calls `lipgloss.SetHasDarkBackground(true)` in its `init`, so the
    background is fixed as dark and Bubble Tea never queries the terminal. `theme.go` imports it.
    The fix depends on the Go init order by import path (Go 1.21 and later): `github.com/appnice/...` sorts
    before `github.com/charmbracelet/bubbletea`, so the `noquery` init runs first. A module rename that
    sorts after `charmbracelet`, or a move of the call into a later package, brings the query back.
    Do not use lipgloss `AdaptiveColor`: the background is not detected, so it always picks the dark
    variant. `TestUIUsesNoAdaptiveColor` fails on it, and the `TestStartupSendsNoQuery` e2e test runs the
    real binary on a PTY that answers nothing.

## Update 2026-11-02: Bubble Tea v2

Items 4 and 10 are superseded. The UI now uses Bubble Tea v2.1.1 and Lip Gloss v2.0.6 (`charm.land/...`).
v2 reads each key press as its own event and a paste as one `PasteMsg`, so the v1 work-around that split
multi-rune key events is gone. v2 does not query the terminal at start, so `internal/ui/noquery` is gone too;
`TestStartupSendsNoQuery` still checks the real binary in a silent PTY. Colours: NO_COLOR keeps only text
attributes in the styles, and v2 downsamples the rest to the terminal's profile.
