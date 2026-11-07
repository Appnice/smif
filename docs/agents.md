# Other coding agents

**A real agent checked only the Claude Code provider.** No other agent ran on the machine that built this, and the
owner has no access to them. For every other agent, the file formats, process names and state rules come from
reading its source and docs. The tests use fake processes and fixtures built from that reading, so a green test
proves only smif's parser, not the real file format. Expect faults.

This covers the session view. Forks stay on Claude Code. Mail works on Claude Code and Codex, see
[messages.md](messages.md#codex).

| Level | Needs | You get |
|---|---|---|
| **Full** | A provider that a real session checked | State, steps, final answer, notifications, typing, a fork |
| **Full, read only** | A provider from a source reading, shown as `unverified` | State, steps, final answer, notifications. No typing, no fork |
| **herdr** | The agent runs in a [herdr](https://herdr.dev) pane | Name, folder, herdr's state, focus. Typing only for an agent that a real session checked. No steps, no answer, no notifications |
| **Listed** | smif knows its process name, and it sits on a terminal | Name and folder, state `unknown`, no typing |

Claude Code (versions 2.1.316 to 2.1.318), Codex (0.166.0) and OpenCode (2.2.6) were checked against a real agent, and
smif types into them. Every other agent is read only and shows as `unverified`.

Codex also joins the mail, see [messages.md](messages.md#codex). Codex has one limit: its files do not tell a running command from an approval prompt. A Codex that waits for your
approval reads `busy` or `unknown`, so it does not get `needs you` or a notification. smif refuses to type into it
then, so a prompt can never answer that approval.

OpenCode has the same limit: an approval lives in its server's memory, so the session reads `busy`. After you interrupt a
run, its database keeps the run open, so the session reads `busy` until its next prompt, which you type in its own window.
A new Codex or OpenCode session has no state until its first message, and smif does not type into it before that.

| Agent | In a herdr pane | Elsewhere | When herdr reads idle |
|---|---|---|---|
| Claude Code | Full | Full | Not used |
| Codex | Full, read only until checked in herdr | Full, types when idle | Not used |
| OpenCode V2 | Full, read only until checked in herdr | Full, types when idle | Not used |
| Qwen Code, GitHub Copilot CLI, Factory Droid | Full, read only | Full, read only | Not used |
| Devin, Hermes, Letta, Grok, Amp, Cline, Maki, Kiro, Muse | herdr | Listed | Idle |
| Kilo, Pi, Oh My Pi, Mastra Code, Kimi | herdr | Listed | Idle if integrated, else `unknown` |
| Gemini CLI, Cursor, Qoder, Antigravity | herdr | Listed | `unknown` |
| Crush, Command Code, Prime Agent | herdr | Not shown | `unknown` |

The second row needs the agent's session file. With none, it drops to herdr in a herdr pane and to Listed elsewhere.
Integrated means that `herdr integration status` reads `current` for the agent.

smif types only when the agent's state source can tell a pending approval from idle. When the state is `unknown`,
smif does not type. smif also never types into an agent that no real session checked, not even in a herdr pane
that reads as idle.

## Add an agent

An agent that smif does not know needs no code. Add it to the config:

```toml
[harnesses.omnis]
process = ["omnis"]   # process names, one word each
command = "omnis"     # optional: what `n` starts
```

- A process with one of these names, on a terminal, shows as a Listed row: name and folder, state `unknown`, no typing.
  `smif reload` picks up a change with no restart of the daemon.
- A wrapper counts: `node /x/bin/omnis` matches too.
- The name is a-z, 0-9, `-` and `_`, at most 32. A custom name needs `process`.
- `command` is one word, a program name or a path, with no spaces or arguments. Use an absolute path: a relative one
  such as `bin/x` passes the check, but the daemon looks for it from its own folder, and the new tab from the session's.
  Without `command`, `n` does not offer the agent.
- A name in the table above may set `process` to add a process name, and `command` to change what `n` starts.
- Claude Code, Codex, Qwen Code, OpenCode, Copilot and Droid have a provider: they may set `command` only.
  `[harnesses.claude] command` changes only what `n` starts. Workers keep `claude_command`.
- A process name that a built-in agent or another harness owns is refused.

## New sessions

`n` offers Claude Code, plus each known or configured agent with a command on the daemon's PATH. Under launchd or
systemd, that PATH is short: `smif install` fills it from where it finds `claude`, `git` and `gh`, plus the usual bin folders. For an agent in
another folder, set `command` to the absolute path. Built-in agents use their usual program, for example
`cursor-agent` for Cursor, `kiro-cli` for Kiro and `agy` for Antigravity.

`session.new` takes a harness name. The daemon builds the command from the config or its built-in table, never from
the call, and refuses an unknown name.

## Checks

`smif doctor` prints one line for each provider. An unverified format reads `unchecked`, never a failure. The file
paths and fields per agent are in [compat.md](compat.md). The decisions are
[ADR-0019](adr/0019-harness-providers.md) and [ADR-0021](adr/0021-session-work.md).
