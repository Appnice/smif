# ADR-0022: Mail between harnesses

Status: accepted, 2026-11-06.

## Decision

smif stays the post office, with no new protocol. The envelope, rules, audit and limits stay as they are, and a Codex
agent joins through two small doors.

**Receive.** A profile can set `harness = "codex"`. A rule that names it hands the message to `codex exec`, and the
reply comes back through the thread like any worker reply.

- The sandbox is `read-only`. A Codex profile must have `write_scope = "none"` and no `onward_mcp`, or the config fails.
- The message goes in on stdin, never in argv (R-ISO-3). It holds the same `<smif_message untrusted="true">` block as a
  Claude worker gets.
- `--ignore-user-config` and `--ignore-rules` keep the user's MCP servers and rules out of the run, and `--ephemeral`
  keeps no session. Each message starts fresh, with no memory or resume.
- `--output-schema` holds the result to `status`, `summary`, `reply` and `human_question`. Codex wants every field in a
  strict schema, so a Codex worker sends no followups.
- The program comes from `[harnesses.codex] command`, default `codex`.

**Send.** `smif mcp` is a normal MCP server, so `codex mcp add smif -- smif mcp` gives Codex `smif_ask` and the others.
The agent comes from the folder, as for Claude Code. Codex asks before each MCP call unless
`default_tools_approval_mode = "approve"` is set for the server.

## Why not more

- **No delivery into a live session.** Inbound mail never reaches a session (D4), and that holds for every harness.
  `codex queue` could do it, and it stays unused.
- **No write access.** A review needs none. A writing Codex worker would need worktree, commit and merge rules that no
  real use asks for yet.
- **No A2A or ACP.** ACP links an agent to an editor. The envelope already has what two agents need here.

## Checked

Live on 2026-11-06, Codex CLI 0.166.0 and Claude Code 2.1.318, with an isolated daemon:

- Claude Code (haiku) called `smif_ask`, a rule sent it to `codex exec`, and Codex found a real bug in an uncommitted
  diff in 21 s. The tree was left as it was.
- Codex called `smif_ask` over MCP, a Claude worker read a file and answered, in 14 s.

`TestCodexWorkerAnswersAsk` covers argv and stdin with a stand-in.
