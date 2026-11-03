# Claude Code compatibility (RFC 10.2)

**Verified against:** Claude Code **2.1.316**, macOS (darwin/arm64), 2026-11-02.
**How:** `claude --help` plus three live `claude -p --model haiku` calls in a scratch git repo
(total cost about $0.03). `mailroom doctor` re-runs the help-based part on every machine and warns when the
installed version differs from 2.1.316.

| Item | Result on 2.1.316 | What Mailroom does |
|---|---|---|
| `-p --output-format stream-json` | Works; needs `--verbose`. First event `{"type":"system","subtype":"init","session_id":…}`, last `{"type":"result",…}` | Parses the stream line by line; transcript written raw |
| `--resume <id>` in `-p` mode | Continues the session; the result carries the **same** `session_id` | First run omits it and stores the init `session_id` |
| `--session-id <uuid>` | Listed | Not needed |
| `--append-system-prompt` | Works in `-p` | Policy + memory.md go here |
| `--allowedTools` / `--disallowedTools` with `Bash(git log:*)` | Accepted as separate arguments; disallowed tools disappear from the tool list | Each pattern is its own argv element |
| Denied tool in headless mode | With `--permission-prompts none`: denied at once, listed in `permission_denials`, the run continues and finishes. No hang | Always passes `--permission-prompts none` (ADR-0002) |
| `--max-turns` | Hidden from `--help`, accepted (live call) | Passed for every run |
| `--permission-mode` values | Listed: acceptEdits, auto, bypassPermissions, manual, dontAsk, plan. `default` is hidden but accepted (live call); a bogus value fails with a clear error | Config allows default, acceptEdits, plan, bypassPermissions |
| `--mcp-config` + `--strict-mcp-config` | With an empty config the worker has 0 MCP servers (without strict it loaded all 12 user/claude.ai servers) | Always passes both |
| Native structured output `--json-schema` | Result event has `structured_output` (validated object); the model is forced to call a StructuredOutput tool | Used; fenced ```json block is the fallback |
| Minimal mode | `--safe-mode`: CLAUDE.md, skills, plugins, hooks and MCP servers off (27 tools, 0 MCP). `--bare` requires `ANTHROPIC_API_KEY`, breaks subscription auth | `minimal = true` → `--safe-mode` (ADR-0002) |
| `--effort` | Listed (low … max) | Passed when the profile sets it |
| `--max-budget-usd` | Listed (`-p` only) | Passed from `max_cost_usd` |
| Result fields | `total_cost_usd`, `usage` (input/output/cache tokens), `num_turns`, `is_error`, `subtype`, `session_id`, `permission_denials`, `terminal_reason` all present | Budget sums `total_cost_usd`; tokens = input + cache + output |
| Hook payload | JSON on stdin with `session_id`, `cwd`, `hook_event_name` (Claude Code hooks reference) | `mailroom hook <event>`; falls back to the process cwd |
| Status line stdin | JSON with `workspace.current_dir` and `session_id` | Falls back to the process cwd |
| Slash command shell execution | `` !`cmd` `` in the command body with `allowed-tools: Bash(...)` frontmatter | `~/.claude/commands/mailroom.md` |
| Worker hooks | User and project hooks fire in worker runs unless disabled | `--settings '{"disableAllHooks":true}'` (profile `hooks = false`, the default); `mailroom hook` also ignores calls with `MAILROOM_RUN_ID`. Checked live 2026-11-02: a project Stop/SessionStart hook in the worker repo did not fire (`TestRealWorkerConfinement`) |
| `--name` | Listed (`-n, --name`) | Worker sessions are named `mailroom <agent>` |
| `--tools` + path deny rules | Checked live 2026-11-02: tools left out of `--tools` do not exist for the model (WebSearch absent); `Bash(*--output*)` denies `git log --output=…`; `Read(//abs/path/**)` denies Read **and** read-only Bash such as `cat` on that path. All denials appear in `permission_denials`; structured output still works | Base names of `allowed_tools`; deny rules on Mailroom's config and data dirs |
| Typing into an interactive session | Checked live 2026-11-03 in Terminal.app: AppleScript `do script "<text>" in <tab>` puts the text in the Claude Code input but does not send it (it arrives as a paste); a separate `do script ""` is Enter and sends it. `ps -o stat=` shows `+` for the claude process in the tab's foreground | `session.prompt`: text, 0.4 s, Enter; the `+` check first (ADR-0012) |
| Session files | Seen 2026-11-03 on Claude Code 2.1.302–2.1.317 (not documented): each running session writes `$CLAUDE_CONFIG_DIR/sessions/<pid>.json` (default `~/.claude`) with `pid`, `sessionId`, `cwd`, `kind` (`interactive` or `bg`), `name` (`--name`, `/rename`), `status` (`idle`, `busy`) and `statusUpdatedAt` (ms). A suspended claude and its resumed copy can share a `sessionId` | `session.list` reads them (interactive, live pid, newer process wins); a missing or changed file leaves the hooks alone (ADR-0012) |
| Transcript tool records | Seen 2026-11-03 on Claude Code 2.1.317 (not documented): `~/.claude/projects/<folder>/<sessionId>.jsonl` assistant records carry `{"type":"tool_use","id","name","input"}` blocks; the result comes back in a later user record as `{"type":"tool_result","tool_use_id"}`. A call without a result is the tool that runs, or the permission or question a waiting session shows. The call reaches the file about 1–3 s after it starts (checked live on 2.1.316: absent at once, present after 3 s while the command still ran) | `session.list` activity: steps, the pending call, the transcript's mtime (ADR-0014) |
| Forking a session | Checked live 2026-11-03 on Claude Code 2.1.316: `claude -p --resume <id> --fork-session` in the session's folder answers from that session's context, reports a new `session_id`, and leaves the original transcript unchanged | Session asks (ADR-0013); the fork runs in the session's folder because `--resume` finds a session only there |
| launchd PATH | launchd agents start with a minimal PATH | The plist sets PATH from where `claude`, `git` and `gh` were found at install; `mailroom init` writes the absolute `claude` path into `claude_command` |

Raw probe outputs are not kept in the repo (they contain account metadata); rerun with `make smoke-real`
(the real-Claude smoke test, `MAILROOM_E2E_REAL=1`) to check a new version.
