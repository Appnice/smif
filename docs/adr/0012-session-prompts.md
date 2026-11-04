# ADR-0012: The human types prompts into idle sessions

**Status:** accepted, 2026-11-03.

**Context.** The human runs one interactive Claude Code session for each project. Some of them wait for input
while others work. The human wants to see which sessions are idle and give one a new prompt from the Smif
UI, without first finding its terminal tab.

D4 says that no inbound text reaches an interactive session. A prompt that the human types is not inbound
mail. It is the human's own input, with a different route to the same session. D4 stays as it is: message
text, worker output and agent requests never go into a session.

**Decision.**

- **Turns from the hooks.** `UserPromptSubmit` reports `busy` and `Stop` reports `idle`. `SessionStart`
  reports `idle`. The `presence` table gets `turn`, `turn_at`, `tty`, `pid` and `term` (migration 002).
- **Where a session runs.** The hook walks up from its parent process to the first `claude` process. It reports
  that process's pid and tty, and `$TERM_PROGRAM` (`tmux` when `$TMUX` is set). Only the human token (which the
  hooks use) can set these fields or a turn. An agent's `presence.update` loses them, so an agent cannot point
  a session row at another terminal.
- **Names.** Several sessions often run in one repo (a workspace folder), so the agent alone does not tell them
  apart. The hook also reads the end (1 MB) of the session's transcript (`transcript_path`) for the newest
  `custom-title` record (`claude --name`, `/rename`) and `last-prompt` record. On `UserPromptSubmit` the
  prompt in the hook payload wins. The daemon keeps them as `title` and `last_prompt` (migration 003, one
  line, at most 60 and 120 characters), human token only like the terminal. The UI and `smif sessions`
  show the name first, then the agent, and the last prompt.
- **Discovery.** Claude Code keeps one file for each running session in `~/.claude/sessions/<pid>.json`
  (`$CLAUDE_CONFIG_DIR`): session id, cwd, kind, name and status (`idle`/`busy`). `session.list` reads
  these files, so every open session shows up at once, also one with no hook since Smif was installed.
  For an interactive session with a live pid it takes the pid and name from the file, the tty from `ps`, and
  the terminal app from the process parents (Terminal, iTerm2, tmux; read once for each process). The newer
  of the file status and the hook turn wins. The files are not a documented interface: when they are
  missing or change, the hooks alone decide (docs/compat.md). Background (`bg`) sessions are not listed. A session in a folder that no agent owns is listed too, without an agent (the git root of its folder stands in for the repo): the human can read and prompt it by name; it gets no hook rows, so no last prompt.
- **Live sessions.** An idle session sends no heartbeats, so `presence_ttl` cannot decide if it is open. A row
  with a pid is live while that process lives. A row without a pid uses `presence_ttl`, as before.
- **`session.list`** (human only) gives each live session with its agent, turn, terminal, and `can_prompt`
  with the reason when it is false.
- **`session.prompt {session_id | agent, text}`** (human only) types one line into one session and presses
  Enter. The text must be one line of plain text (no control characters), at most 4000 characters. The
  session must be `idle`. Its claude process must be alive, be `claude`, sit on the recorded tty and be in the
  foreground process group of that tty (`ps` stat `+`). Without this check, the text could go to a shell after
  claude exits. After the prompt, the turn is `busy` and an audit row `session.prompt` keeps the first 80
  characters. Prompts are typed one at a time.
  *Amended 2026-11-04.* Enter comes `0.5 s + 8 ms` a character after the text (at most 2 s): Claude Code
  reads the keys as a paste and keeps an Enter that comes too soon inside it. The daemon then waits for the
  turn (hook or session file): after 3 s it presses Enter once more (never the text), after 8 s the prompt is
  unsent (audit `session.prompt_unsent`, an error). With `async: true` (the UI, through `session.send`) the
  call returns once the checks pass, the session shows `busy` while the keys go in, and an unsent prompt
  shows as the session's `unsent` until its next turn.
- **Answers.** `session.tail {session_id, limit}` (human only) reads the last turns (default 20, at most 100)
  from the session's transcript, `<claude config>/projects/*/<session id>.jsonl`. A turn is the human's prompt
  or Claude's answer, and an answer holds only its final message: the text after its last tool call. Notes
  between tool calls, tool results, subagent (`isSidechain`) and meta records and system text (it starts with
  `<`) are left out; the tool calls are a count. `session.list` adds each session's last final message
  (`last_answer`, `last_answer_at`), read again only when the transcript changes. The UI shows its first line
  in the session row, `●`, a bold name and "N new answers" for answers not seen yet (an answer is seen when the human moves onto the session or opens its chat, not while the cursor only rests there; kept in `ui-seen.json` in the data folder across restarts), and in
  the preview the last prompt with its answer (earlier answers one line each; one `working` line while a
  prompt runs). `enter` shows the whole answer, `s` replies, `o` (`session.focus`, human only) brings the
  session's Terminal tab to the front. This text goes only to the human's UI, never into a session or to an
  agent, so D4 holds.
- **Typing.** `daemon.terminal = "auto"` drives the session's own terminal, found by its tty, and sends the
  text, waits 0.4 s, then sends Enter apart: text and Enter in one piece land in the Claude Code input as a paste
  and are not sent (tested on Claude Code 2.1.316 in Terminal.app). Terminal.app: AppleScript `do script text`,
  then `do script ""`. iTerm2: AppleScript `write text text newline no`, then `write text ""`. tmux (any OS):
  `tmux send-keys -t <pane> -l -- text`, then `send-keys Enter`, the pane found by `list-panes -a` on the
  default server. `o` selects the tab (AppleScript) or the pane and its window (tmux). `"none"` turns prompts
  off. `"log:<path>"` writes JSONL instead (tests). This setting needs a daemon restart.
- **Surfaces.** In the UI, a SESSIONS group on the home screen (grouped by root folder when there is more than one, then a fixed order by name, so rows do not move when a session turns idle or busy), `s` on an idle session
  opens the box under the preview, and `: prompt <name | agent> <text>`. In the CLI, `smif sessions` and
  `smif prompt <session name | agent> [--session <prefix>] <text…>`. A name (any case) picks the session
  with that name (`session.prompt {name}`); with no such session it is an agent with one idle session.

**Limits.**

- Terminal.app, iTerm2 and tmux. Only Terminal.app is checked live; iTerm2 and tmux are covered by tests of
  their commands and the log terminal. Other terminals show the reason in the UI. tmux uses the default server
  socket; a session on another socket (`tmux -L`/`-S`) cannot be reached.
- Without the session files (another Claude Code version), a session shows up only after its first hook.
- Esc on a running turn may not fire `Stop`; the session file status corrects it.
- If the human typed a partial line in the session's input, the prompt is added to it before Enter.
- macOS asks once whether Smif may control Terminal or iTerm2 (Privacy & Security → Automation). Without
  that, `session.prompt` returns that instruction.

**Tests.** `e2e/sessions_test.go`: hook to idle, list, the terminal and dead-process rules, agents forbidden and
unable to forge a terminal, bad text refused, CLI prompt typed, busy after, idle again, two audit rows.
`internal/ui/sessions_test.go`: the group, the preview, `s`, the busy refusal and `: prompt`.
`internal/store/runs_test.go`: a bare heartbeat keeps the turn and terminal.
