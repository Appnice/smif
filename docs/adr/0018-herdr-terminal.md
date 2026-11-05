# ADR-0018: herdr is a terminal Smif can drive

**Status:** accepted, 2026-11-05.

**Context.** [herdr](https://herdr.dev) is a terminal multiplexer for coding agents. A session in a herdr pane
was listed with no terminal Smif knows, so it could not be typed into or brought to the front. herdr panes are
real ptys, so the foreground check of ADR-0012 works there unchanged. herdr has a CLI that answers JSON.

**Decision.**

- The process walk that names the terminal stops at a `herdr` process; the nearest multiplexer wins, so tmux
  inside herdr counts as tmux. The hook reports `herdr` from `$HERDR_ENV` when `$TMUX` is not set.
- A pane is found by the claude pid, not by tty: `herdr pane list` gives no tty, and a pane's id changes when it
  moves. `session list --json` gives the running herdr sessions, `pane list` and `pane process-info --pane <id>`
  the foreground processes of each pane. It is looked up on each use. Every running herdr session is searched,
  so named sessions (`--session`) work.
- Prompts go through `herdr pane run <pane> <text>`: text and Enter as one submission, in bracketed paste when
  the pane asks for it, so no `enterDelay`. It takes the rest of the line as text: a leading `-` is typed, and no
  `--` goes before it. The Enter retry of ADR-0012 is `pane send-keys <pane> enter`.
- `o` runs `agent focus` (else `workspace focus` and `tab focus`). A herdr client that runs in Terminal.app or
  iTerm2 then gets its tab brought to the front; other outer terminals stay where they are.
- `n` on a herdr session creates a workspace in the folder, named after the session, on the default herdr
  session (else the first running one), and runs `claude --name <name>` in its root pane.

**Consequences.** One prompt takes about 0.5 s (two CLI calls per pane are the cost of the lookup). Checked live
on herdr 0.9.5 and Claude Code 2.1.318; `SMIF_HERDR_REAL=1 go test -run TestHerdrLive ./internal/daemon` re-checks
open, lookup, type, Enter and focus. A remote herdr (`--machine`, `--remote`) is out of reach: Smif sees local
processes only.
