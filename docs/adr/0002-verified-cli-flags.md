# ADR-0002: Flags added or changed after verifying Claude Code 2.1.316

**Status:** accepted, 2026-11-02 (evidence in `docs/compat.md`)

**What.**
1. Workers get `--permission-prompts none`. Anything that would prompt is denied at once, so a headless
   worker never hangs on a permission question (RFC 10.2 "headless behaviour on a denied tool").
2. Profiles gain `max_cost_usd`, passed as `--max-budget-usd`. R-RUN-1 asks for a per-run cost budget;
   the CLI enforces it, and the supervisor still enforces timeout and output size.
3. Minimal mode uses `--safe-mode`, not `--bare`. `--bare` reads only `ANTHROPIC_API_KEY`, so on a
   subscription login every worker would fail auth. `--safe-mode` also disables MCP servers, so
   `minimal = true` together with `onward_mcp = true` is rejected by validation.
4. Results use native structured output (`--json-schema`), read from the result event's
   `structured_output`. The fenced ```json block remains the fallback.
5. `--permission-mode default` is accepted although `--help` no longer lists it (choices now:
   acceptEdits, auto, bypassPermissions, manual, dontAsk, plan). Config keeps the RFC's four values.
6. `--max-turns` is hidden from `--help` but accepted; verified by a live call.

**Trade-off.** Each item is probed by `smif doctor` and falls back as RFC 10.2 describes when missing.

**Added 2026-11-02 (product review).**
7. Workers run with `--settings '{"disableAllHooks":true}'` unless the profile sets `hooks = true`. RFC 10.2
   called user hooks in workers "acceptable (worker context only)". They are not: a worker in the appnice
   workspace would run its Stop hooks (memory distillation that can start further `claude -p` calls), Entire
   checkpoints and memory raw-capture, all side effects outside the worker. `--safe-mode` is not used for
   this because it also drops CLAUDE.md, which workers need.
8. Workers get `--name "smif <agent>"`, so their sessions are recognisable in `claude --resume`.
9. Workers get `--tools` with the base names of the profile's `allowed_tools` (e.g. `Read,Grep,Glob,Bash`).
   Allow rules alone leave every other built-in tool in place, and some (Agent, WebSearch, scheduling and
   messaging tools) run without a prompt.
10. Every worker, whatever its profile, gets these deny rules: `Bash(*--output*)` (`git log/show/diff
    --output=<file>` writes files through a read-only allowlist) and Read/Grep/Glob/Edit/Write on Smif's
    config dir (tokens) and data dir (other agents' mail and transcripts). This keeps D4 true from the
    worker side too.

Items 7, 9 and 10 are checked live by `TestRealWorkerConfinement` (`make smoke-real`).
