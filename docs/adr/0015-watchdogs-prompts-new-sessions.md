# ADR-0015: Watchdogs, saved prompts and new sessions

**Status:** accepted, 2026-11-03. `p` on a busy session follows `s`, so it queues since ADR-0021 section 3.

**Context.** ADR-0014 tells the human once when a session starts to wait or answers. Three gaps were left:

- A wait that the human missed stays missed.
- A working session that stalls says so only in the list.
- The human types the same prompts again and again ("run the tests and report"), and starts new sessions by hand
  in a new tab.

Similar tools for parallel agents have watchdogs, playbooks and one place to start sessions. Smif takes these
three ideas and leaves the rest (an IDE, integrations, agent-to-agent plans).

**Decision.**

- **Watchdogs.** The session watcher (ADR-0014, every 2 s) also sends:
  - `session_reminder`: the session still waits, after `daemon.remind_waiting` (default `10m`), then again at two
    and three times that, so at most 3 reminders for one wait;
  - `session_silent`: a working session has written nothing to its transcript for `daemon.alert_silent` (default
    `10m`). It comes once per silence; if the session writes again, a later silence counts again;
  - `session_long`: a turn has worked longer than `daemon.alert_long_turn` (default `off`). It comes once per turn.

  Each value is a duration of 1 s or more, or `"off"`. They do not depend on `daemon.session_notify`. As in
  ADR-0014, the audit keeps the kind and the session, never the text.
- **Saved prompts.** `[prompts]` in the config maps a name (`a-z 0-9 - _`, at most 32 characters) to one line of
  text.
  - `prompts.list` gives them to the human only.
  - In the UI, `p` on a session lists them, and `enter` or `1-9` sends one the way `s` would: typed into an idle
    session, else answered by a fork.
  - In `session.send` and `session.prompt` (the UI, `smif prompt`, `: prompt`), text that starts with
    `@name` becomes the saved prompt, followed by the rest of the text. `@x` that is not the name of a saved
    prompt stays as typed, because `@README.md` is Claude Code's file mention.
- **New sessions.** `session.new {dir, name, term}` (human only) opens the configured `claude_command` with
  `--name <name>` in a new terminal, in `dir`:
  - Terminal.app: a new window (`do script`);
  - iTerm2: a new tab (or a new window when there is none);
  - tmux: `new-window -c <dir>`.

  The folder must be absolute and exist. The name may hold letters, digits, spaces and `. _ & -`, at most 40. Only
  the folder and the name come from the call, and each word is quoted for `sh`. The command itself comes from the
  config. In the UI, `n` asks for a name and opens the new session in the selected session's folder and terminal.
  The session then shows in the list through discovery (ADR-0012). It is audited as `session.new`.

**Limits.**

- Terminal.app opens a window, not a tab. A new tab would need System Events keystrokes and the Accessibility
  permission, which Smif does not ask for.
- Claude Code's own `--worktree` is not used. A worktree per session is the human's choice, in the folder they pick.
- The silent alert uses the transcript's mtime. A long command that prints nothing also counts as silent.

**Tests.**

- `e2e/sessions_test.go` `TestWatchdogsPromptsNew`:
  - silent and long once per turn, then reminders;
  - `@tests` expands and `@README.md` stays;
  - `session.new` argv, bad folder, name and terminal refused;
  - agents get neither `session.new` nor `prompts.list`.
- `internal/ui/sessions_test.go` `TestSavedPromptsAndNewSession`: the picker, digits and enter, `n` with the name and
  `esc`.
- `internal/daemon` `TestShellQuote`.
