# Other coding agents

**A real agent checked only the Claude Code provider.** No other agent ran on the machine that built this, and the
owner has no access to them. For every other agent, the file formats, process names and state rules come from
reading its source and docs. The tests use fake processes and fixtures built from that reading, so a green test
proves only smif's parser, not the real file format. Expect faults.

This covers the session view. Messages, workers, forks and `smif_ask` stay on Claude Code.

| Level | Needs | You get |
|---|---|---|
| **Full** | A provider that a real session checked | State, steps, final answer, notifications, typing, a fork |
| **Full, read only** | A provider from a source reading, shown as `unverified` | State, steps, final answer, notifications. No typing, no fork |
| **herdr** | The agent runs in a [herdr](https://herdr.dev) pane | Name, folder, herdr's state, focus. Typing only for an agent that a real session checked. No steps, no answer, no notifications |
| **Listed** | smif knows its process name, and it sits on a terminal | Name and folder, state `unknown`, no typing |

Only Claude Code types and was checked against a real agent, on versions 2.1.316 to 2.1.318. Every other agent is
read only and shows as `unverified`.

| Agent | In a herdr pane | Elsewhere | When herdr reads idle |
|---|---|---|---|
| Claude Code | Full | Full | Not used |
| Codex, Qwen Code, OpenCode V2, GitHub Copilot CLI, Factory Droid | Full, read only | Full, read only | Not used |
| Devin, Hermes, Letta, Grok, Amp, Cline, Maki, Kiro, Muse | herdr | Listed | Idle |
| Kilo, Pi, Oh My Pi, Mastra Code, Kimi | herdr | Listed | Idle if integrated, else `unknown` |
| Gemini CLI, Cursor, Qoder, Antigravity | herdr | Listed | `unknown` |
| Crush, Command Code, Prime Agent | herdr | Not shown | `unknown` |

The second row needs the agent's session file. With none, it drops to herdr in a herdr pane and to Listed elsewhere.
Integrated means that `herdr integration status` reads `current` for the agent.

smif types only when the agent's state source can tell a pending approval from idle. When the state is `unknown`,
smif does not type. smif also never types into an agent that no real session checked, not even in a herdr pane
that reads as idle.

`smif doctor` prints one line for each provider. An unverified format reads `unchecked`, never a failure. The file
paths and fields per agent are in [compat.md](compat.md), and the decision is
[ADR-0019](adr/0019-harness-providers.md).
