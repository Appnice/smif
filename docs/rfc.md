# RFC: Smif — event-driven, hard-isolated messaging between Claude Code sessions
2026-11-02 · Status: Implemented (v0.1.0). Source: the Claude Doc "RFC: Smif". Deviations are recorded in `docs/adr/` and marked **(ADR-NNNN)** below.
## 0. Instructions for the implementing agent
You are building Smif end to end, from an empty repository to an installed, tested tool. This RFC is the contract. Requirements marked MUST are not optional, and nothing here is a stretch goal unless section 10 says so.
1. Read the whole RFC before writing code. Sections 3 (requirements) and 5 to 8 (contracts) are normative; section 2 explains why each requirement exists.
2. Work milestone by milestone (section 9). A milestone is done only when its Definition of Done passes, including its tests, and you have committed it.
3. Never call the real `claude` binary in automated tests. Use the fake runner (section 9.4). Real-Claude smoke tests run only when `SMIF_E2E_REAL=1` is set.
4. Keep a decision log in `docs/adr/`. If you must deviate from this RFC, write an ADR stating what, why and the trade-off, then update the RFC text in `docs/rfc.md` in the same commit.
5. Before relying on any Claude Code CLI flag, verify it with `claude --help` on the installed version and record the version in `docs/compat.md`. Section 10.2 lists the flags that need verification and the fallback for each.
6. Do not depend on experimental Claude Code features (channels, `--dangerously-*` flags, native `SendMessage` for delivery). This is a hard requirement (R-ISO-4), not a preference.
7. Do not touch the user's existing Claude Code settings, hooks or MCP config except through `smif install`, which must back up every file it edits and be reversible with `smif uninstall`.
8. Finish by running every acceptance scenario in section 9.5 and writing `docs/acceptance-report.md` with the result of each.
## 1. Summary, problem and scenarios
Smif is a local daemon that lets long-running Claude Code sessions, each owning a different project, message each other, while guaranteeing that an inbound message is never injected into the interactive session a human is driving. Every inbound message is handled by a separate headless Claude Code process, in its own context and its own git worktree, chosen by deterministic routing rules enforced in code.
### 1.1 Problem
The user runs about five terminals, one Claude Code session per app, plus two cross-cutting agents. Today the human relays questions between terminals by hand. Existing tools either inject the message into the live session (context pollution: a crash report lands in the middle of "change the font colour"), require polling, or rely on a prompt instruction such as "delegate this to a subagent". Instructions are not enforcement; the model can ignore them.
### 1.2 Agents in the first deployment
| Agent id | Owns | Typical inbound | Typical outbound |
|---|---|---|---|
| `lab` | Lab app repo | crash events, version queries | asset requests |
| `puzzle` | Puzzle game repo | crash events, version queries, asset requests | asset requests |
| `cafe` | Cafe app repo | crash events, store-status and version queries | — |
| `atlas` | appnice.dev website repo | release notifications | store status and version queries to every app |
| `observability` | observability workspace, daily cross-app report | ack of handled incidents | crash and anomaly events to app agents |

Repo paths are placeholders in the example config (section 7.6); `smif init` discovers them interactively.
### 1.3 Scenarios the system must handle
1. **Release sync.** `atlas` asks `cafe`: is the current build live in the store, and which version? The answer must come back without the human touching the Cafe terminal, ideally from a deterministic command, with no LLM call.
2. **Crash routing.** `observability` detects a crash in Puzzle and sends an event. A background worker in a `puzzle` worktree triages it, writes a report and optionally a fix branch. The human working in the Puzzle terminal is not interrupted; the status line shows that something was handled.
3. **Asset request.** One app agent asks another for an asset (icon, screenshot, sprite). The answer carries files, not just text.
4. **Escalation.** A worker decides a human decision is needed. The human gets an OS notification and a status-line badge, and pulls the details when ready.
5. **Storm.** The same crash fires 40 times in an hour. The recipient handles it once, with a counter.
6. **Loop.** Two agents keep replying to each other. The system stops the loop deterministically.
## 2. Prior art and research findings
No shipping tool enforces, at the tooling level, that an inbound message to a Claude Code session is handled by a hard-isolated, context-separate worker. One project has the right architecture in preview form; everything else injects, polls, or isolates by prompt. Research run of 2026-11-02 ([report](https://agent.tinyfish.ai/playground/research/share/01a19ba4-b67d-71ae-93a0-ffdfd36d7b17)).
### 2.1 Landscape
| Tool | Push wake-up | Process-isolated handling | Persistence | Status | Why it does not fit |
|---|---|---|---|---|---|
| Claude Code native `ListAgents` / `SendMessage` (v2.1.224+) | Claimed, unreliable | No, injected into session | Socket only | Shipped, buggy | Bugs below; always lands in the live context |
| [dataforxyz/agent-intercom-claude](https://github.com/dataforxyz/agent-intercom-claude) | Yes, daemon spawns `claude -p --resume` | Yes | Durable outbox | Preview, 3 stars, stale, AGPL-3.0 | Coupled to the Pi broker (`~/.pi`, `PI_*`); no worktree isolation, no human notification, asks capped at 120 s |
| [louislva/claude-peers-mcp](https://github.com/louislva/claude-peers-mcp) | Yes, via experimental channels | No | SQLite | ~2.2k stars, inactive, no license | Needs `--dangerously-load-development-channels` |
| [yilunzhang/claude-code-inter-session](https://github.com/yilunzhang/claude-code-inter-session) | Monitor, active sessions only | No, prompt injection by design | Log file | ~29 stars | Inherits Monitor wake bug |
| [dicklesworthstone/mcp_agent_mail](https://github.com/dicklesworthstone/mcp_agent_mail) and [Rust rewrite](https://mcpagentmail.com/) | No, poll only | No | SQLite + Git | Python deprecated (~1.7k stars), Rust active | Never solved wake-up |
| [AgentMBX](https://agentmbx.com/) | Yes, inbox pointer batched in 30 s | Partial: pointer, not body | Signed, on disk | Alpha | Same session wakes; plaintext at rest |
| [shariqh/agent-inbox](https://github.com/shariqh/agent-inbox) | Optional hooks | No | SQLite | 4 stars | Good human UI, no worker spawning |
| [avivsinai/agent-message-queue](https://github.com/avivsinai/agent-message-queue/blob/main/README.md) | Doorbell types into terminal | No | Atomic files | Early | Terminal typing is brittle |
| [krasserm/agent-exchange](https://github.com/krasserm/agent-exchange) | Fire-and-forget tmux send | No | Unclear | 4 stars | tmux send-keys problems |
| Claude Code Agent Teams | Lead to teammate only | No, one parent session | Runtime-owned | Experimental | Single project, single parent; no cross-project peers |

Not assessed in depth: Switchboard, agentbus, openswarm, DeanSingh/claude-peers (broker with triage), agent-wire, omp-mail, 5dive-a2a. `jpslav/agent-mailbox` has no verifiable public repo.
### 2.2 Platform defects that shape the design
| Issue | Defect | Consequence for Smif |
|---|---|---|
| [#86671](https://github.com/anthropics/claude-code/issues/86671) | Cross-session messages shown in UI but not delivered to the model; one report measured 77% non-delivery | Native delivery is not a transport |
| [#77300](https://github.com/anthropics/claude-code/issues/77300) | Monitor events do not wake idle named teammates | Monitor is not a wake mechanism |
| [#43706](https://github.com/anthropics/claude-code/issues/43706) | Teammate-to-lead `SendMessage` silently dropped | No reliance on teams messaging |
| [#23415](https://github.com/anthropics/claude-code/issues/23415) | Teammates never read inbox after restart (tmux backend) | Daemon owns delivery, not the session |
| [#44080](https://github.com/anthropics/claude-code/issues/44080) | `SendMessage` intermittently missing from the tool set | Own MCP tools for sending |
| [#77495](https://github.com/anthropics/claude-code/issues/77495) | No sender provenance; caused refusal spirals; closed as not planned | Daemon stamps authenticated sender |
| [#23463](https://github.com/anthropics/claude-code/issues/23463) | Large subagent results overflow parent context | Bounded worker output, session rotation |
| [#62631](https://github.com/anthropics/claude-code/issues/62631) | No child-to-parent wake; closed as not planned | Continuations via daemon |
| [#27441](https://github.com/anthropics/claude-code/issues/27441), [#37213](https://github.com/anthropics/claude-code/issues/37213), [#33788](https://github.com/anthropics/claude-code/issues/33788) | Repeated requests for external injection, inter-session comms and a cross-project lobby; closed as duplicates | Demand is real and unmet; #27441 sketches exactly this daemon but never built it |

Channels (`notifications/claude/channel`) need a development flag, are best-effort, and drop events for idle sessions. The VS Code request [#336056](https://github.com/microsoft/vscode/issues/336056) for a one-shot wake contract confirms no stable cross-host wake API exists.
### 2.3 Why previous projects failed
1. **Built on unstable APIs.** claude-peers-mcp depended on an experimental flag and stalled.
2. **Polling mistaken for messaging.** Agent Mail, agent-inbox and agent-message-queue built good storage but never solved wake-up.
3. **Inherited platform bugs.** Tools on Monitor or native `SendMessage` inherit #77300, #86671 and #43706.
4. **tmux as a bus.** Shell-init interference, lost Enter keys, no way to tell agent output from the prompt.
5. **Isolation as prompt policy.** Every tool but agent-intercom-claude isolates through SKILL.md text or caution notices, which a model can override.
### 2.4 Research foundations
- **Actor model** (Hewitt 1973; [AutoGen v0.4](https://www.microsoft.com/en-us/research/articles/autogen-v0-4-reimagining-the-foundation-of-agentic-ai-for-scale-extensibility-and-robustness/)): actors share no state, process one message at a time from a mailbox, and delivery is decoupled from handling. Smif applies this per agent: one mailbox, one serial handler.
- **Blackboard** ([arXiv 2510.01285](https://arxiv.org/abs/2510.01285)): agents pick up work from a shared store instead of a controller pushing into their reasoning context; reported 13–57% relative gains over master-slave baselines. Smif's thread store is the blackboard; workers, not the live session, pick up.
- **Concurrency control for MAS** ([arXiv 2608.18092](https://arxiv.org/abs/2608.18092)): MAS failure rates of 41–86.7% trace to concurrency faults; prompt-based mitigations give brittle guarantees versus system-level mechanisms.
- **CoAgent** ([arXiv 2606.15376](https://arxiv.org/abs/2606.15376)): tool-call middleware enforcing serialization with one-way notifications; within 5% of serial correctness at 1.4× speed. Smif enforces ordering and write isolation in the daemon for the same reason.
- **Interop protocols** ([arXiv 2505.02279](https://arxiv.org/abs/2505.02279), MCP, ACP, A2A, ANP): network-level coordination; none addresses local idle-session wake with isolated handling.
### 2.5 Lessons turned into requirements
| Lesson | Requirement |
|---|---|
| Injection pollutes the live context | R-ISO-1, R-ISO-2 |
| Prompt-level isolation is brittle | R-ISO-3 |
| Experimental APIs die | R-ISO-4 |
| Polling never wakes anyone | R-DEL-1 |
| Native delivery drops messages | R-DEL-2, R-DEL-3 |
| No provenance causes refusal spirals | R-SEC-1 |
| Concurrent writers corrupt state | R-ISO-5, R-CON-1 |
| Context overflow is unrecoverable | R-RUN-3 |
| Humans lose track of background work | R-HUM-1, R-HUM-2 |

## 3. Goals, non-goals and requirements
### 3.1 Goals
- Any registered agent can send a message, ask a question or emit an event to any other registered agent on the same machine.
- Every inbound message is handled automatically and autonomously, without a human relaying it, and without entering the context of the recipient's interactive session.
- The human always knows what was handled, what is running and what needs them, at a glance and without reading transcripts.
- Deterministic, cheap handling where an LLM is not needed (version lookups, file fetches).
- Survives daemon restarts, machine sleep and Claude Code upgrades without losing messages.
### 3.2 Non-goals (v1)
- Cross-machine or multi-user delivery. The transport is a local Unix socket; the protocol must not preclude a relay later.
- Windows. macOS and Linux only.
- Mail, workers and `smif_ask` on harnesses other than Claude Code. The runner is an interface so Codex or OpenCode can be added later. The session view reads other agents through harness providers, section 3.4 **(ADR-0019)**.
- A GUI. CLI, status line and OS notifications only, plus an optional one-way phone ping **(ADR-0021)**.
- Replacing the user's own orchestration harnesses. Smif is a mail system, not a planner.
### 3.3 Requirements
MUST unless stated otherwise. Each requirement has an acceptance test (section 9.5).
| ID | Requirement |
|---|---|
| R-ISO-1 | Inbound message content is never written into an interactive Claude Code session's context by Smif, automatically, under any setting. |
| R-ISO-2 | Each inbound message that needs an LLM is handled by a separate OS process (`claude -p`) whose context contains only the worker's own history, the envelope and the policy prompt. |
| R-ISO-3 | Routing, handler choice, permissions, write scope and budgets are enforced by the daemon in code. No safety property depends on the model obeying an instruction. |
| R-ISO-4 | No dependency on experimental Claude Code features: channels, `--dangerously-*` flags, Agent Teams, native `SendMessage` or Monitor for delivery. |
| R-ISO-5 | Workers that may write do so in a dedicated git worktree on a Smif branch. Writing to the main checkout is allowed only for agents explicitly configured `write_scope = "direct"`, and only while no interactive session for that agent is active (section 6.4). |
| R-DEL-1 | Delivery is push: the daemon starts the handler as soon as the recipient's queue allows, with no polling by any agent. |
| R-DEL-2 | Messages are durable (SQLite, WAL, fsync on commit) from acceptance to terminal state. A daemon crash loses nothing that was acknowledged to the sender. |
| R-DEL-3 | At-least-once handling with idempotency: a message is never handled twice to completion; a crash mid-run causes a retry with the same message id, and handlers see an attempt counter. |
| R-DEL-4 | Failed messages go to a dead-letter state after the configured retries, with the reason, and the human is notified. |
| R-CON-1 | Per recipient, runs are serialized (one worker process per agent at a time, since `--resume` on one session cannot run concurrently). A global cap limits total concurrent workers. |
| R-CON-2 | Loop, hop and deadlock protection (section 6.6). |
| R-RUN-1 | Each run has a wall-clock timeout, a max-turns cap and a cost budget; exceeding any terminates the process group. |
| R-RUN-2 | Workers return a structured result validated against a schema; invalid output is recorded and treated as a failed attempt. |
| R-RUN-3 | Worker sessions rotate before context overflow, carrying forward a compact memory file. |
| R-SEC-1 | The daemon stamps the authenticated sender on every message. A sender cannot claim another agent's identity. |
| R-SEC-2 | Per-recipient allowlists of senders and message intents. |
| R-SEC-3 | Inbound payloads are presented to workers as untrusted data inside a delimited envelope. |
| R-HUM-1 | The interactive session's status line shows per-agent counts: running, handled since last look, needs human. |
| R-HUM-2 | `needs_human` and dead-letter states raise an OS notification. |
| R-HUM-3 | The human can read any thread, run, transcript and diff from the CLI, and pull a digest into their interactive session only by explicit action (`/smif` command). |
| R-OPS-1 | Single static binary, user-level service (launchd or systemd), one-command install and uninstall. |
| R-OPS-2 | Structured logs, an append-only audit log and a health check. |

### 3.4 Agents in the session view (ADR-0019)
This section covers the session view only: `session.list`, `session.tail`, `session.prompt`, `session.send`, `session.queue`, `session.focus` and `session.attach`. Mail, workers and `smif_ask` stay on Claude Code.

**A real agent checked only the Claude Code provider.** Every file path, field, process name and state rule for another agent comes from reading its source and docs. The fixtures come from the same reading, and the live checks ran fake processes in herdr panes. These prove smif's parser and gates, not the real formats. An unverified provider MUST stay read only: no typing, no fork, no hooks. Only a row in `docs/compat.md` with the version a real session checked lifts this, in the provider's code.

| Level | Needs | The human gets |
|---|---|---|
| Full | A provider a real session checked | State, steps, final answer, notifications, typing, a fork |
| Full, read only | A provider from a source reading, marked `unverified` | State, steps, final answer, notifications. No typing, no fork |
| herdr | The agent runs in a herdr pane | Name, folder, herdr's state, focus. Typing only for an agent that a real session checked. No steps, no answer, no notifications |
| Listed | A known process name on a terminal, or one from `[harnesses.<name>]` in the config **(ADR-0021)** | Name and folder, state `unknown`, no typing |

| Agent | Level | Typing | Checked against a real agent |
|---|---|---|---|
| Claude Code | Full | Yes, when idle | **Yes** (Claude Code 2.1.316 to 2.1.318) |
| Codex, Qwen Code, OpenCode V2, GitHub Copilot CLI, Factory Droid | Full, read only (`unverified`). In a herdr pane with no session file: herdr, read only. Else Listed | No | No |
| Devin, Hermes, Letta, Grok, Amp, Cline, Maki, Kiro, Muse | herdr, read only (`unverified`). Outside herdr: Listed | No | No |
| Kilo, Pi, Oh My Pi, Mastra Code, Kimi | herdr, read only (`unverified`). State `unknown` when herdr reads idle, unless `herdr integration status` reads `current` for the agent. Outside herdr: Listed | No | No |
| Gemini CLI, Cursor, Qoder, Antigravity | herdr, read only (`unverified`), state `unknown` when herdr reads idle. Outside herdr: Listed | No | No |
| Crush, Command Code, Prime Agent | herdr, read only (`unverified`), state `unknown` when herdr reads idle | No | No |

Smif MUST NOT type when the agent's state source cannot tell a pending approval from idle: the state is `unknown`, and unknown means no typing. At the herdr level, an agent with no provider stays read only until a real session checks it (`herdrChecked`, `internal/daemon/herdr.go`): herdr's screen rules are not proof for an agent nobody ran. The state gates behind it (`internal/harness/herdr.go`) were checked only with fake processes.

## 4. Architecture
One per-user daemon owns delivery, state and process supervision; interactive sessions only send and observe counts; every inbound message is handled by a worker process or a command handler the daemon starts.
_(Diagram in the source doc: senders → daemon (ingress, store, router, scheduler, supervisor) → isolated handlers (command, `claude -p` worker in a worktree); a dashed counts-only line from the daemon to the status line.)_
The dashed line is the only path from the daemon into an interactive session, and it carries counts, never message content. Replies return to the sender through the same daemon.
### 4.1 Components
| Component | Binary / location | Responsibility |
|---|---|---|
| `smifd` | `smif daemon` | Socket server, store, router, scheduler, supervisor, notifier. The only long-lived process. |
| `smif` CLI | `smif <cmd>` | Admin, inspection, sending from shell scripts, install and uninstall. |
| MCP server | `smif mcp` (stdio) | Tools for interactive sessions and workers (section 7.2). Thin client of the daemon socket. |
| Hooks | `smif hook <event>` | Presence (`SessionStart`, `SessionEnd`, `UserPromptSubmit`, `Stop`) and the status line command. |
| Runner | `internal/runner/claude` | Builds and runs `claude -p` invocations; parses stream JSON; enforces limits. Behind a `Runner` interface. |
| Command handlers | user scripts | Deterministic handlers declared in config, run by the supervisor. |
| Workspace manager | `internal/workspace` | Creates, resets and garbage-collects git worktrees per agent. |

### 4.2 Process model
- The daemon runs as a launchd agent (macOS) or systemd user service (Linux), restarted on failure.
- Each handler run is a child process in its own process group, so a timeout kills the whole tree (`kill(-pgid)`).
- On start the daemon reconciles: runs in state `running` with no live process become `retry_wait` with attempt+1.
- The daemon never blocks on a handler: dispatch, output parsing and timeouts are asynchronous.
### 4.3 Isolation model
Isolation is three independent layers, each enforced by the daemon, so a failure in one does not compromise the others.
| Layer | Mechanism | Enforced by |
|---|---|---|
| Context | Separate `claude -p` process; the interactive session never receives content | Process boundary; no injection code path exists |
| Filesystem | Dedicated git worktree per agent; `direct` scope only with no active interactive session | Workspace manager + presence table |
| Authority | `--permission-mode`, `--allowedTools`, `--disallowedTools`, budgets, allowlists | Runner builds argv from validated config; no model-controlled flags |

### 4.4 Message flow
1. A sender calls `send`, `ask` or `emit` through MCP or the CLI.
2. Ingress authenticates the connection, stamps `from`, validates size and schema, and commits the message to the store (state `accepted`) before acknowledging.
3. The router evaluates the recipient's policy: allowlist, dedupe, hop and loop guards, then picks a handler (section 6.1).
4. The scheduler enqueues the run in the recipient's serial queue, ordered by priority then age.
5. The supervisor prepares the workspace and starts the handler.
6. The handler's structured result is validated and stored; a reply is routed to the sender if the message was an `ask`.
7. Counters update; the status line reflects them on its next refresh; `needs_human` and dead letters fire a notification.
8. If the worker's result requests follow-up asks to other agents, those become new messages with the same `thread_id` and `hop+1`.
### 4.5 Identity
An agent is a named project role (`puzzle`), not a session. The interactive session and the background worker share the agent id but have different origins (`interactive`, `worker`), recorded on every message. Presence hooks tell the daemon which cwd has a live interactive session, which feeds the `direct` write rule. A session started outside its agent's repo (a workspace root, a monorepo) sets `SMIF_AGENT=<id>`; hooks, status line, digest and MCP then use that agent instead of the cwd.
## 5. Message model and protocol
Every message is an immutable envelope with a daemon-stamped sender, a thread, a hop count and a kind; its lifecycle is a fixed state machine owned by the daemon.
### 5.1 Envelope
```json
{
  "id": "01JC8Z3V7Q5H2R9T4W6X8Y0A1B",
  "thread_id": "01JC8Z3V7Q5H2R9T4W6X8Y0A1B",
  "parent_id": null,
  "from": {"agent": "observability", "origin": "worker", "run_id": "01JC8Z..."},
  "to": "puzzle",
  "kind": "event",
  "intent": "crash.report",
  "priority": "high",
  "subject": "NullPointerException in DeckScreen on 1.4.2",
  "body": "Free text for the handler.",
  "data": {"version": "1.4.2", "count_24h": 37, "stack_hash": "a91f"},
  "attachments": [{"name": "stack.txt", "sha256": "…", "size": 4211, "mime": "text/plain"}],
  "dedupe_key": "crash:puzzle:a91f",
  "hop": 0,
  "deadline": "2026-11-03T09:00:00Z",
  "created_at": "2026-11-02T10:42:11Z"
}
```
| Field | Rule |
|---|---|
| `id`, `thread_id` | ULIDs minted by the daemon. A new conversation's `thread_id` equals its first `id`. |
| `from` | Set by the daemon from the authenticated connection. Any client-supplied value is rejected. |
| `kind` | `tell` (no reply expected), `ask` (exactly one reply expected), `event` (machine signal, dedupable), `reply` (answers an `ask`, carries `parent_id`). |
| `intent` | Dotted label, matched by routing rules. Free-form, validated against `^[a-z0-9]+(\.[a-z0-9_-]+)*$`. |
| `priority` | `low`, `normal`, `high`, `urgent`. Orders the recipient queue. |
| `body` | Max 32 KiB. Larger content goes into attachments. |
| `data` | Optional JSON object, max 64 KiB, passed to handlers verbatim. |
| `attachments` | Content-addressed blobs (section 8.1), max 25 MiB each, 100 MiB per message. |
| `dedupe_key` | For `event` only. A second event with the same key while the first is not terminal is coalesced (section 6.5). |
| `hop` | `0` for human-originated or interactive sends; parent hop + 1 for messages sent by a worker run. |
| `deadline` | Optional; past it, a queued message becomes `expired`. Default TTL from config. |

### 5.2 Lifecycle
_(Diagram in the source doc. Implemented edges, `internal/store/store.go`: accepted → queued | rejected | needs_human | done | expired; queued → running | rejected | needs_human | expired; running → done | needs_human | retry_wait | dead_letter | rejected; retry_wait → queued | dead_letter | rejected | expired; needs_human → done | queued | rejected; dead_letter → queued.)_
Terminal states are `done`, `rejected`, `expired`, `dead_letter`; `needs_human` is terminal for the run but reopens if the human answers through `smif reply`. A coalesced event never enters this machine: it increments `coalesced_count` on the open message and is recorded in the audit log. Every transition is a single SQLite transaction that also appends an audit row.
### 5.3 Ask and reply semantics
- `ask` returns a ticket immediately. The caller may then `await` it with `wait_s` (default 30, max 600). No ask blocks the daemon.
- An `ask` is resolved by exactly one `reply`. The handler's structured result (`reply` field) becomes that reply automatically; handlers never call a reply tool for the message they are handling.
- If the asker was a worker run that ended before the reply arrived, the daemon starts a **continuation**: a new run for the asking agent with the reply as its inbound message, same `thread_id`. This replaces blocking waits and is how chained work proceeds.
- If the asker was an interactive session, the reply is stored and surfaced as a count (`1 reply ready`); the session retrieves it with `await` or `get_thread`. Retrieving the answer to a question the session itself asked does not violate R-ISO-1.
- An ask with no reply by its deadline resolves with a synthetic `reply` of status `timeout`.
### 5.4 Wire protocol
Newline-delimited JSON-RPC 2.0 over a Unix domain socket at `$XDG_RUNTIME_DIR/smif/smifd.sock` (Linux) or `~/Library/Application Support/smif/smifd.sock` (macOS), mode `0600`. Every request carries `auth: {agent, token}`; the daemon additionally checks the peer uid (`SO_PEERCRED` / `getpeereid`) equals its own. Worker runs receive a run-scoped token bound to `{agent, run_id, hop}`, valid only while the run is `running`, so a worker cannot forge hop counts or outlive its run. The protocol carries `protocol_version`; the daemon rejects unknown major versions.
## 6. Routing, handlers and worker runtime
The daemon decides how every message is handled from config rules, never from the model. A cheap deterministic handler is preferred whenever one is configured.
### 6.1 Routing rules
Each agent has an ordered list of rules. The first rule whose `match` fits the message wins. Every agent must end with a catch-all rule; config validation fails otherwise.
| Match field | Semantics |
|---|---|
| `from` | Agent id or list; `*` for any allowed sender |
| `kind` | `tell`, `ask`, `event`, `reply` or list |
| `intent` | Glob on the dotted intent, e.g. `crash.*`, `store.status` |
| `priority_at_least` | Minimum priority |

| Handler | What happens | Typical use |
|---|---|---|
| `command` | Runs a user script with the envelope on stdin; its JSON stdout is the result. No LLM. | Version and store-status lookups, file fetches |
| `worker` | Runs a Claude Code worker with the named profile (6.2) | Crash triage, asset preparation, release tasks |
| `escalate` | Goes straight to `needs_human` with a notification | Anything the user wants to decide personally |
| `digest` | Stored and counted, not handled | FYI notices |
| `drop` | `rejected` with the rule id as reason; an `ask` gets a `rejected` reply | Disallowed traffic |

Allowlists (R-SEC-2) are checked before rules: `accept_from` per agent, default all configured agents. Traffic from an unknown or disallowed sender is `rejected` and audited.
### 6.2 Worker profiles
A profile is a named, validated set of runtime settings. Agents reference profiles in rules and may override fields.
| Field | Default | Notes |
|---|---|---|
| `model` | `sonnet` | Passed as `--model` |
| `effort` | unset | Passed only if the installed CLI supports it (10.2) |
| `permission_mode` | `default` | One of `default`, `acceptEdits`, `plan`, `bypassPermissions`; the last requires `allow_bypass = true` on the agent |
| `allowed_tools` / `disallowed_tools` | per profile | Passed as `--allowedTools` / `--disallowedTools`; rejected if they appear inside `extra_args` |
| `write_scope` | `none` | `none`, `worktree` or `direct` (6.4) |
| `max_turns` | 30 | `--max-turns` |
| `max_cost_usd` | unset | `--max-budget-usd`, the per-run cost cap of R-RUN-1 **(ADR-0002)** |
| `timeout_s` | 900 | Wall clock; kills the process group |
| `policy` | built-in per profile | File appended with `--append-system-prompt` |
| `onward_mcp` | `false` | If true, the worker gets the Smif MCP tools with a run-scoped token |
| `hooks` | `false` | If false, the user's and the repo's Claude Code hooks do not run in the worker **(ADR-0002)** |
| `minimal` | `false` | Run without CLAUDE.md, skills, plugins and hooks if the CLI offers such a mode (10.2) |

`harness = "codex"` runs the worker on a read-only `codex exec`: `write_scope` must be `none`, and no `onward_mcp` (ADR-0022).

Built-in profiles: `triage` (read-only tools, `write_scope = none`), `fix` (`acceptEdits`, `write_scope = worktree`), `answer` (read-only, short `max_turns = 8`), `release` (`acceptEdits`, `write_scope = direct`, for agents that must act on the main checkout, such as appnice.dev publishing).
### 6.3 Worker invocation
The runner builds argv only from validated config. Nothing from the message can reach argv.
```bash
claude -p \
  --output-format stream-json --verbose \
  --resume <worker_session_id> \
  --model <model> --permission-mode <mode> \
  --allowedTools <list> --disallowedTools <list> \
  --max-turns <n> --permission-prompts none \
  [--max-budget-usd <max_cost_usd>] [--effort <effort>] [--json-schema <result schema>] [--safe-mode] \
  --append-system-prompt "$(cat <policy_file>)" \
  --mcp-config <generated.json> --strict-mcp-config
# cwd = workspace from 6.4, stdin = rendered inbound prompt
# env: SMIF_AGENT, SMIF_RUN_ID, SMIF_ATTEMPT, SMIF_MESSAGE_ID, SMIF_BLOB_DIR;
# SMIF_RUN_TOKEN only when the profile sets onward_mcp (ADR-0003)
```
The first run of a worker session omits `--resume`, reads `session_id` from the stream's init event, and persists it. The inbound prompt wraps the envelope as untrusted data:
```text
You are the background mail handler for agent "puzzle". No human is watching this run.
<smif_message untrusted="true">
{ ...envelope JSON, attachments listed with local read-only paths... }
</smif_message>
Handle it under your policy. End with exactly one ```json result block.
```
Result schema (R-RUN-2). If the installed CLI supports native structured output (10.2), use it; otherwise parse the last fenced `json` block of the final message.
```json
{
  "status": "done | needs_human | failed",
  "summary": "≤ 280 chars, shown in status line and notifications",
  "reply": "text answer for an ask, ≤ 32 KiB",
  "reply_data": {},
  "artifacts": [{"path": "relative/path", "description": "..."}],
  "human_question": "required when status is needs_human",
  "followups": [{"to": "cafe", "kind": "ask", "intent": "store.status", "subject": "...", "body": "..."}]
}
```
`followups` are sent by the daemon after the run, with `hop + 1` and the same thread, so workers need no tools to talk to peers. Artifacts are copied into the blob store and attached to the reply. Full transcripts stay on disk (8.1); nothing large is ever forwarded into another agent's context.
### 6.4 Workspace isolation
| `write_scope` | Worker cwd | Rule |
|---|---|---|
| `none` | Main checkout | Write tools are denied by `--disallowedTools Edit Write NotebookEdit`; Bash is limited to the profile's allowlisted patterns |
| `worktree` | `~/.local/share/smif/worktrees/<agent>` | Branch `smif/<agent>/<thread-ulid-short>` created from the agent's `base_ref`; reused by continuations in the same thread |
| `direct` | Main checkout | Allowed only if presence shows no active interactive session for that repo; the daemon holds a repo lock for the run. Otherwise the message waits (`hold`) or downgrades to `worktree`, per agent config |

- Before a `worktree` run: checkout the thread branch, `git clean -fd` (ignored files such as `node_modules` and build caches are kept), verify a clean tree.
- After the run: if the tree is dirty, the daemon commits with `smif: <summary> [run <id>]` so no work is lost, and records branch, head SHA and diffstat on the run.
- Merging is always a human action: `smif diff <run>`, `smif apply <run>` (merge into the main checkout's current branch, refused on a dirty tree), or `pr = true` per agent to open a pull request with `gh` after the run.
- Presence: `SessionStart` marks a repo active, `SessionEnd` clears it, `UserPromptSubmit`, `Stop` and the status line refresh it as heartbeats; presence older than `presence_ttl` (default 30 min) expires. While a `direct` run holds the lock, the status line in any session for that repo shows it.
### 6.5 Scheduling, concurrency and dedupe
- One worker process per agent at a time (actor semantics, R-CON-1). Global cap `max_workers` (default 3). Command handlers use a separate pool (default 4) and may run in parallel.
- Queue order: priority, then age; a queued message gains one priority level per 30 min waiting, so nothing starves.
- Dedupe: an `event` whose `dedupe_key` matches a non-terminal message is coalesced (counter + audit row). Rules may set `cooldown` (e.g. `6h`): within it, a repeat of a `done` event is also coalesced, unless the counter crosses `reopen_after` (e.g. 10), which creates a new message flagged `recurrence`.
### 6.6 Loops, hops and deadlocks (R-CON-2)
- `max_hops` (default 4): a message above it is `rejected` and escalated.
- Ping-pong guard: more than 6 messages between the same two agents in one thread within 10 min freezes the thread and escalates.
- `max_thread_messages` (default 20) per thread.
- Per-sender rate limit (token bucket, default 60 per minute).
- No run ever blocks waiting for another agent: asks resolve through continuations (5.3), so wait-for cycles cannot hang processes; cycles are bounded by the hop limit.
### 6.7 Limits and budgets (R-RUN-1)
- Per run: `timeout_s`, `max_turns`, and `max_output_bytes` for the stream (default 8 MiB). Breach kills the process group; state `retry_wait` or `dead_letter`.
- Per agent per day: `budget_usd` and `budget_tokens`, summed from the run's final usage report. When exhausted, new worker runs follow `on_budget`: `hold` (wait until midnight local) or `escalate`. Command handlers are not budgeted.
- Retries: exponential backoff 30 s, 2 min, 10 min; `max_attempts` default 3; attempt number is passed to the handler.
### 6.8 Worker session rotation (R-RUN-3)
- Rotate when the last run's input tokens exceed `rotate_at_tokens` (default 120 000), after `rotate_after_runs` (default 25), or after a context-length failure.
- Rotation runs one compaction turn on the old session that rewrites `~/.local/share/smif/agents/<agent>/memory.md` (max 4 000 chars, human-editable), then starts a fresh session whose policy prompt includes that file.
- The old session id is archived on the run record so `claude --resume` still opens it for inspection.
## 7. Interfaces
Four surfaces: the daemon socket API, an MCP server for sessions, a CLI for humans and scripts, and Claude Code hooks that report presence and draw the status line. None of them can push message content into an interactive session. The only text Smif types into a session is the human's own prompt (`session.prompt`, ADR-0012), at once or from the queue that only the human fills (`session.queue`, ADR-0021). `session.send` to a busy session, or an agent's ask to a session's name, is answered by a read-only fork of that session (`--fork-session`), never typed in (ADR-0013).
### 7.1 Daemon API (JSON-RPC methods)
| Method | Purpose |
|---|---|
| `message.send` / `message.ask` / `message.emit` | Create `tell`, `ask`, `event` |
| `message.await` | Wait up to `wait_s` for the reply to the caller's own ask |
| `message.reply` | Human answer to a `needs_human` item, or an ask routed to `escalate` |
| `message.retry` / `message.cancel` | Requeue a dead letter; cancel a queued or running item (returns the state after the call: `rejected` at once, or `cancelling` while a running worker stops) **(ADR-0009)** |
| `thread.get` / `thread.list` | Threads with messages, runs and results |
| `run.get` / `run.list` / `run.transcript` | Run records and transcript paths |
| `run.diff` / `run.apply` | Inspect or merge a worker branch; the daemon runs the merge so it is audited **(ADR-0006)** |
| `agent.list` / `agent.status` | Agents, capabilities (`intents` from their rules), queue depth, budget left |
| `presence.update` | From hooks: session start, heartbeat, end, and the turn (`busy` on `UserPromptSubmit`, `idle` on `Stop`) with the claude pid, tty and terminal, and the session's name and last prompt from its transcript (human token only); `session.list` also asks the harness providers, for Claude Code its session files **(ADR-0012, ADR-0019)**; `agent` (from `SMIF_AGENT`) wins over `cwd` |
| `session.list` / `session.tail` / `session.focus` / `session.prompt` / `session.send` | Human only (`session.send`: typed in when idle, else a read-only fork of the session answers, **ADR-0013**): live interactive sessions with their harness and support level (`full`, `herdr` or `listed`), the `unverified` mark of a provider no real session checked, which leaves the session read only **(ADR-0019)**, turn, last answer, what the newest exchange does now (prompt, tool calls, the pending one; **ADR-0014**) and its facts (files, commits, tests after the last edit, uncommitted paths, context tokens; **ADR-0021**); the last turns of one session from its transcript (final messages only); bring its tab or pane to the front; type one line of the human's own text into an idle session (Terminal.app, iTerm2, tmux, herdr) **(ADR-0012, ADR-0018)** The UI attaches long pastes, images and files as `@path` in that line (**ADR-0020**) |
| `stats.get` | Human only: counts over a window, per agent and day (ADR-0011) |
| `inbox.counts` | Per-agent counters for the status line; for the human, also the other sessions that wait or answered (**ADR-0014**) |
| `session.queue` / `session.queue.cancel` | Human only (**ADR-0021**, no MCP tool, inbound mail never reaches it): one line of the human's text waits until its session is idle. A daemon watcher types the oldest item 2 s after the session reads idle, through the gates of `session.prompt`, one item per idle. A refused item stays with its reason, retried at most every 30 s. It expires after 24 h, and a session that ended turns its items `gone` with a notification. `session.list` gives each session its queued items. A read-only or listed session takes no item |
| `session.attach` | Human only (**ADR-0021**, no MCP tool): opens `claude attach <id>` for a background session in a new tab. `session.list` marks these sessions `background`, read from `claude agents --json --all`. A background session takes no typing, no queue item and no fork. The id comes from the list and the program from the config, never from the call |
| `session.history` / `session.search` / `session.resume` / `session.closed` / `session.reopen` | Human only (**ADR-0021**, no MCP tool): past Claude Code sessions with no `sdk-` entrypoint and no subagent files, read from the head and tail of each transcript; a text search of user and assistant text that stops at 50 hits or 10 s; `claude --resume <id>` in the session's folder in a new tab, for an id in the index, a folder that exists and a session that does not run; the restore list; a reopen of that list, at most 20, 500 ms apart. The id and folder come from the index and the program from the config, never from the call. `open-sessions.json` in the data dir keeps the live sessions and the list across a restart |
| `prompts.list` / `session.new` / `session.harnesses` | Human only (**ADR-0015**): the saved prompts (`[prompts]`; `@name` in `session.send` and `session.prompt` stands for one); open a coding agent in a folder in a new Terminal.app window, iTerm2 tab, tmux window or herdr workspace; the agents `session.new` can start now. `session.new` takes a harness *name*, default `claude`. The daemon builds argv from the config or its built-in table, and only `claude` gets `--name <name>`. An unknown name is refused **(ADR-0021)** |
| `digest.get` | Summaries of handled items since a cursor (human pull only); optional `until` bounds the advance **(ADR-0008)** |
| `audit.since` | Audit rows after a cursor, for the live feed of `smif ui` and `smif watch` (human only) **(ADR-0008)** |
| `blob.put` / `blob.get` | Attachment storage |
| `admin.health` / `admin.reload` / `admin.gc` | Health, config hot-reload, cleanup |

### 7.2 MCP server
`smif mcp` is registered at user scope. It resolves its agent from `SMIF_AGENT`, or by matching the session's cwd to an agent's `repo`. In an unregistered directory it exposes only `smif_agents`.
| Tool | Available to | Behaviour |
|---|---|---|
| `smif_agents` | all | Lists agents, the intents they handle and their status summary |
| `smif_send` | interactive, worker with `onward_mcp` | Sends a `tell` or `event` |
| `smif_ask` | interactive, worker with `onward_mcp` | Sends an `ask`; optional `wait_s` (max 120 inside a tool call); returns the reply or a ticket |
| `smif_await` | interactive, worker with `onward_mcp` | Waits for the caller's own ticket |
| `smif_status` | interactive | Counts only: running, handled, needs human, replies ready |

There is deliberately no MCP tool that reads another agent's inbound mail or the agent's own handled items. Reading those is a human action through the CLI or the `/smif` command (7.4), so the model cannot pull inbound content on its own initiative. This is how R-ISO-1 is enforced rather than requested.
### 7.3 CLI
| Command | Purpose |
|---|---|
| `smif init` | Interactive setup: discover repos, write config, generate tokens |
| `smif install` / `uninstall` | Service unit, hooks, status line, slash command, MCP registration; backs up and restores every edited file |
| `smif daemon` | Run the daemon in the foreground (used by the service unit) |
| `smif status` | Global overview: agents, queues, running runs, budgets, DLQ size |
| `smif inbox [agent]` | Recent items with state and summary |
| `smif thread <id>` / `run <id> [--transcript]` | Full detail |
| `smif digest [--agent a] [--since last]` | Summaries of handled items; advances the per-agent read cursor |
| `smif diff <run>` / `apply <run>` | Inspect or merge a worker branch |
| `smif reply <msg> "text"` | Answer a `needs_human` item; resumes the thread |
| `smif send` / `ask` / `emit` | Same as the MCP tools, for scripts and cron (the observability agent's daily job can emit from a shell) |
| `smif retry` / `cancel` / `dlq` | Dead-letter management |
| `smif tail` | Follow the audit log |
| `smif ui` | Full-screen live view for the human (agents, running runs, needs you, dead letters, activity, live audit events) with reply, cancel and retry; inbox with filters, thread, run (record, transcript tail, diff) and apply with a y/N question; digest with mark read; human token only **(ADR-0007, ADR-0008)** |
| `smif watch [--events]` | Plain-text live view for a pipe, tmux or ssh: a summary every few seconds, or one line per audit event; retries while the daemon is down **(ADR-0008)** |
| `smif doctor` | Checks CLI version and flags (10.2), socket, permissions, hooks, git, notifier, and each harness provider's file format (an unverified provider's format is `unchecked`, never a failure, **ADR-0019**) |
| `smif config validate` | Schema and rule validation, including catch-all rule presence |
| `smif notify test` | Sends one ping to `daemon.notify_url` from the CLI process **(ADR-0021)** |

All read commands support `--json`.
### 7.4 Hooks, status line and slash command
- `smif install` adds hooks for `SessionStart`, `SessionEnd`, `UserPromptSubmit` and `Stop`, each running `smif hook <event>`. Hook commands read the hook payload from stdin, call `presence.update`, print nothing, and always exit 0 within 200 ms (fail open, log errors). Printing nothing is mandatory: hook stdout can enter the session context.
- Status line: `smif statusline` prints one segment, e.g. `! shop waits · ● docs answered · mail: puzzle 1 running, 2 handled, 1 needs you`: first the other live sessions that wait in their tab or have an unseen answer **(ADR-0014)**, then the mail counts. If the user already has a `statusLine` command, install wraps it (`smif statusline --wrap '<existing command>'`) and appends the segment. The status line call also counts as a presence heartbeat.
- Slash command `~/.claude/commands/smif.md` runs `smif digest --cwd "$PWD" --since last` through the command file's shell-execution syntax, so digest content enters the session only when the human types `/smif`.
- Notifications **(ADR-0017)**: on macOS a small `Smif.app` in the data dir (an AppleScript applet with smif's icon, built and signed ad hoc on first use, fed through `notify-queue/`), else `terminal-notifier` or `osascript`; `notify-send` on Linux, for `needs_human`, dead letters and budget exhaustion. Title = agent, body = summary (≤ 280 chars), never the raw inbound body. Also for a session that starts to wait in its tab, or answers after a turn of 30 s or more (`daemon.session_notify`, **ADR-0014**); title = the session's name, body = what it asks for or the answer's first line.
- Phone ping **(ADR-0021)**: `daemon.notify_url` (empty = off) gets one POST for each notification above and each watchdog notification, with the headers `Title: smif` and `Tags: bell`. The body is the name and what happened (`shop waits for you`), built from the kind and the agent or folder name, never from the summary or a session title, so it holds no prompt, answer or command text. `https`, or `http` to localhost; no redirect; 5 s timeout, no retry, at most 16 at once; the watcher never waits for it. No log, error or doctor line names the URL. `smif notify test` sends one. This is the one thing that sends data off the machine, and nothing comes back.
### 7.5 Command handler contract
Stdin: the envelope JSON. Env: `SMIF_AGENT`, `SMIF_MESSAGE_ID`, `SMIF_ATTEMPT`, `SMIF_BLOB_DIR`. Cwd: the agent's repo. Stdout: one JSON object `{status, summary, reply, reply_data, artifacts}`. Exit non-zero, timeout (default 30 s) or invalid JSON is a failed attempt; stderr is stored on the run.
### 7.6 Configuration
`~/.config/smif/config.toml`, validated on load and on `admin.reload`; an invalid reload keeps the old config. Paths below are placeholders that `smif init` fills in.
```toml
[daemon]
max_workers = 3
command_pool = 4
default_ttl = "24h"
presence_ttl = "30m"
notify = "os"
session_notify = "waiting,answered"  # or "waiting", "answered", "none" (ADR-0014)
notify_url = ""            # phone ping: https, or http to localhost; empty is off (ADR-0021)
remind_waiting = "10m"     # watchdogs (ADR-0015): a duration or "off"
alert_silent = "10m"
alert_long_turn = "off"
context_warn = "200k"      # a session's row shows its context over this; a count or "off" (ADR-0021)
test_commands = []         # regexes added to the built-in test commands of the session facts (ADR-0021)
terminal = "auto"          # session prompts: "auto" (Terminal.app, iTerm2, tmux, herdr), "none", "log:<path>" (ADR-0012)

[defaults]
claude_command = "claude"
max_hops = 4
max_attempts = 3

[harnesses.omnis]          # a coding agent with no code (ADR-0021): Listed level, `n` can start it
process = ["omnis"]
command = "omnis"          # one word; a built-in provider's name may set only this

[profiles.triage]
model = "sonnet"
permission_mode = "default"
allowed_tools = ["Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git show:*)"]
disallowed_tools = ["Edit", "Write", "NotebookEdit"]
write_scope = "none"
max_turns = 25
timeout_s = 900

[profiles.fix]
model = "sonnet"
permission_mode = "acceptEdits"
allowed_tools = ["Read", "Grep", "Glob", "Edit", "Write", "Bash(git:*)", "Bash(./gradlew test:*)"]
write_scope = "worktree"
max_turns = 40
timeout_s = 1800

[agents.puzzle]
repo = "~/dev/puzzle"
base_ref = "main"
accept_from = ["observability", "atlas", "lab", "cafe"]
budget_usd = 10
on_budget = "escalate"

[[agents.puzzle.rules]]
match = { kind = "ask", intent = "app.version" }
handler = "command"
command = ["./scripts/smif/version.sh"]

[[agents.puzzle.rules]]
match = { from = "observability", kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "6h"
reopen_after = 10

[[agents.puzzle.rules]]
match = { kind = "ask", intent = "asset.*" }
handler = "worker"
profile = "answer"

[[agents.puzzle.rules]]
match = {}
handler = "escalate"

[agents.cafe]
repo = "~/dev/cafe"

[[agents.cafe.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/smif/store-status.sh"]

[[agents.cafe.rules]]
match = { from = "observability", kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "6h"

[[agents.cafe.rules]]
match = {}
handler = "escalate"

[agents.atlas]
repo = "~/dev/appnice.dev"
direct_conflict = "hold"

[[agents.atlas.rules]]
match = { kind = "tell", intent = "release.*" }
handler = "worker"
profile = "release"

[[agents.atlas.rules]]
match = {}
handler = "digest"

[agents.observability]
repo = "~/dev/observability"

[[agents.observability.rules]]
match = { kind = "reply" }
handler = "digest"

[[agents.observability.rules]]
match = {}
handler = "escalate"

# agents.lab follows the puzzle pattern
```
The store-status and version scripts belong to each app repo; `smif init` scaffolds them as stubs that print a valid result and exit, so the user fills in the store API call.
## 8. Storage, security, observability, failures
All state lives in one SQLite database plus content-addressed files under one data directory; nothing listens on the network; every failure has a defined, tested outcome.
### 8.1 Storage
Data directory: `~/.local/share/smif/` on Linux, `~/Library/Application Support/smif/` on macOS. Everything is created `0700` / `0600`.
| Path | Content | Retention |
|---|---|---|
| `smif.db` | SQLite, WAL, `synchronous=FULL`, `busy_timeout=5000` | Messages and runs 90 days |
| `blobs/sha256/ab/cd…` | Attachments and artifacts, refcounted | Until unreferenced, then 7 days |
| `transcripts/<agent>/<run>.jsonl` | Raw stream JSON and stderr per run | 30 days |
| `worktrees/<agent>/` | Git worktrees | Branches removed 30 days after merge or abandonment |
| `agents/<agent>/` | `memory.md`, `session.json` (current and archived session ids, one session per agent and workspace directory, ADR-0005) | Permanent |
| `audit/YYYY-MM-DD.jsonl` | Append-only audit log, gzipped after rotation; mirrors the SQLite `audit` table written in each transition's transaction **(ADR-0004)** | 90 days |
| `logs/smifd.jsonl` | Structured daemon log | 14 days |

Core tables (migrations versioned in `internal/store/migrations`):
| Table | Key columns |
|---|---|
| `messages` | `id`, `thread_id`, `parent_id`, `from_agent`, `from_origin`, `from_run`, `to_agent`, `kind`, `intent`, `priority`, `state`, `hop`, `dedupe_key`, `coalesced_count`, `deadline`, `rule_id`, `envelope_json`, timestamps |
| `runs` | `id`, `message_id`, `agent`, `handler`, `profile`, `attempt`, `state`, `pid`, `pgid`, `session_id`, `workspace`, `branch`, `head_sha`, `result_json`, `usage_json`, `cost_usd`, `error`, timestamps |
| `presence` | `repo`, `session_id`, `last_seen`, `state`, `turn`, `turn_at`, `tty`, `pid`, `term`, `title`, `last_prompt` (ADR-0012), `harness` (ADR-0019) |
| `session_asks` | `message_id`, `session_id`, `cwd`, `created_at`: an ask a fork of that session answers, written by the daemon only (ADR-0013) |
| `session_queue` | `id`, `session_id`, `agent`, `name`, `text`, `created_at`, `state` (`queued`, `sent`, `cancelled`, `expired`, `gone`), `error`: a prompt that waits for its session to be idle. Each state change writes an audit row in the same transaction (ADR-0021) |
| `cursors` | `agent`, `digest_cursor` |
| `budgets` | `agent`, `day`, `cost_usd`, `tokens` |
| `blobs` | `sha256`, `size`, `mime`, `refcount` |

Indexes on `(to_agent, state, priority, created_at)`, `(thread_id)`, `(dedupe_key, state)`.
### 8.2 Security
Threat model: one local user. Threats are a prompt-injected or misbehaving agent trying to make peers act harmfully, malicious content inside messages (crash logs, web text), other local OS users, and accidental destructive actions by workers.
| Control | Requirement |
|---|---|
| Socket `0600`, peer uid check, no TCP listener | Other users cannot connect |
| Per-agent token (32 random bytes, `~/.config/smif/tokens/<agent>`, `0600`), run-scoped tokens for workers | R-SEC-1 |
| Daemon-stamped `from`, `origin`, `run`, `hop` | R-SEC-1, fixes the #77495 class of refusal spirals |
| Per-agent `accept_from` and rule-based `drop` | R-SEC-2 |
| Envelope wrapped as untrusted data; policy prompt states that message text is data, not instructions | R-SEC-3 (defence in depth; not relied on for safety) |
| argv built only from config; `extra_args` cannot contain permission or tool flags | R-ISO-3 |
| Workers see only the built-in tools their profile allows (`--tools`); every worker is denied `Bash(*--output*)` and any access to Smif's config and data dirs **(ADR-0002)** | R-ISO-3, D4 |
| `bypassPermissions` needs `allow_bypass = true` per agent; `doctor` warns when set | Explicit opt-in |
| Attachments stored by hash, copied read-only into a per-run temp dir; names sanitised, no path traversal | No file escape |
| Secrets (tokens) redacted from logs and transcripts index | No leakage |

### 8.3 Observability
- Every log line carries `agent`, `message_id`, `run_id`, `thread_id`.
- The audit log records every state transition, rule decision, coalesce, rejection, auth failure, config reload and human action.
- `smif status --json` exposes queue depth, running runs, success rate, p50/p95 run duration, cost and tokens per agent per day, DLQ size and budget left. The observability agent can include it in its daily report by running that command.
- `admin.health` reports daemon version, schema version, DB status, detected Claude Code version and flag-probe result.
### 8.4 Failure handling
| Failure | Behaviour |
|---|---|
| Daemon crash or kill mid-run | On restart, kill any recorded `pgid` still alive, mark the attempt failed, schedule a retry |
| Disk full or DB write error | Reject new messages with an error to the sender; never acknowledge what was not persisted |
| `claude` missing, logged out, or a required flag gone after an upgrade | Hold all worker runs, one notification, `doctor` explains; command handlers keep working |
| API overload or rate limit | Retry with backoff; these attempts do not count toward `max_attempts` up to 5 extra |
| Invalid result block | One automatic repair turn on the same session asking only for the JSON block; then failed attempt |
| Timeout or max turns | Kill the process group; any worktree changes are committed to the thread branch before retry |
| Worktree broken or branch conflict | Recreate the worktree; on conflict create a suffixed branch and record both |
| Machine sleep | Timeouts use elapsed process time; after wake, a run past its deadline is killed only if it produced no output in the last 60 s |
| Recipient removed from config | Queued messages become `rejected` with reason `agent_removed` |
| Sender gone when the reply arrives | Reply stored; delivered by `await`, a continuation, or the digest |

## 9. Implementation plan
Build in Go as one static binary, in seven milestones, each closed by its tests; the release is accepted only when all fifteen acceptance scenarios pass.
### 9.1 Stack
| Concern | Choice | Why |
|---|---|---|
| Language | Go, current stable release | Single static binary, process groups and signals, strong concurrency, fast startup for hooks (200 ms budget) |
| SQLite | `modernc.org/sqlite` | Pure Go, no cgo, so the binary stays static |
| MCP | Official Go SDK `github.com/modelcontextprotocol/go-sdk` | Stdio server; verify current version |
| CLI | `spf13/cobra` | Standard subcommand tree |
| Terminal UI | `charmbracelet/bubbletea` v1 + `lipgloss` | Pure Go; testable model (`Update`/`View`) **(ADR-0007)** |
| Config | `pelletier/go-toml/v2` + explicit validation | Strict decoding, unknown keys rejected |
| IDs | `oklog/ulid/v2` | Sortable message ids |
| Logging | `log/slog`, JSON handler | Structured logs |
| Git | `git` CLI via exec | Worktree support is complete only in the CLI |
| Release | GoReleaser, darwin and linux, amd64 and arm64 | Reproducible binaries, optional Homebrew tap |

### 9.2 Repository layout
```text
cmd/smif/            entrypoint, cobra commands
internal/config/         schema, defaults, validation, reload
internal/auth/           tokens, run tokens, peer credentials
internal/api/            JSON-RPC server and client
internal/store/          SQLite access, migrations, state machine transitions
internal/router/         allowlists, rules, dedupe, hop and loop guards
internal/scheduler/      per-agent queues, priority, aging, global caps
internal/supervisor/     process groups, timeouts, orphan recovery
internal/runner/         Runner interface
internal/runner/claude/  argv builder, flag probe, stream parser, result validation, repair turn
internal/runner/command/ command handler execution
internal/workspace/      worktrees, branches, commit-on-exit, diff, apply
internal/presence/       hook events, heartbeats, repo locks
internal/harness/        Provider interface and registry: what the session view reads per coding agent (ADR-0019)
internal/harness/claude/ Claude Code provider: session files, transcripts, file status, fork folder, process name
internal/harness/qwen/   Qwen Code provider, read only: per-pid registry, transcript tree (unverified, from its source)
internal/harness/codex/  Codex provider, read only: ps and folder matched to rollouts (unverified, from its source)
internal/harness/opencode/ OpenCode V2 provider, read only: SQLite with mode=ro, ps and folder (unverified, from its source)
internal/harness/copilot/ Copilot CLI provider, read only: lock files, events.jsonl (unverified, from its schema)
internal/harness/droid/  Droid provider, read only: ps and folder matched to transcripts (unverified, from its bundle)
internal/mcpserver/      MCP tools
internal/statusline/     segment rendering, wrapping
internal/notify/         macOS and Linux notifiers
internal/blob/           content-addressed storage
internal/audit/          append-only audit log
internal/install/        hooks, settings backups, service units, slash command
testdata/fakeclaude/     scripted fake of `claude -p`
e2e/                     acceptance scenarios
docs/                    rfc.md, adr/, compat.md, acceptance-report.md
packaging/               launchd plist, systemd unit templates
```
### 9.3 Milestones
| # | Scope | Definition of done |
|---|---|---|
| M0 | Repo, local checks on macOS and Linux via `make check` / `make check-linux` (vet, staticcheck, race tests) **(ADR-0001)**, config schema and validation, fake claude | `smif config validate` passes on the 7.6 example and fails on a config without a catch-all rule; CI green |
| M1 | Store, migrations, daemon, socket API, auth, send/ask/emit, state machine, audit | State machine property test (no illegal transition reachable); `kill -9` during writes loses no acknowledged message |
| M2 | Router, scheduler, command handlers, allowlists, dedupe and cooldown, hop and loop guards, priorities, retries, dead letters | A1, A6, A7, A9 pass |
| M3 | Claude runner, flag probe, stream parsing, result schema and repair, limits, workspaces, commit-on-exit, continuations, session rotation | A2, A3, A4, A10, A13 pass with fake claude |
| M4 | MCP server, full CLI, digest cursor, diff and apply, human reply | A5 passes; MCP tool list matches 7.2 exactly (test asserts no inbound-reading tool) |
| M5 | Hooks, status line with wrapping, slash command, notifications, presence and direct-scope locking, install and uninstall, service units | A11, A12 pass |
| M6 | Budgets, GC, doctor, real-Claude smoke test, docs, GoReleaser, acceptance report | A8, A14, A15 pass; all scenarios pass; `docs/acceptance-report.md` written |

### 9.4 Testing strategy
- **Fake claude.** A Go program in `testdata/fakeclaude` built at test time and pointed to by `claude_command`. It accepts the real flag set, records its argv, cwd and env to a JSON file, reads stdin, and emits stream JSON (init with `session_id`, assistant messages, final result with usage and cost). Behaviour is scripted by directives in the message body: `#fake:result=needs_human`, `#fake:sleep=30`, `#fake:bad_output`, `#fake:edit=path`, `#fake:spawn_child` (for process-group kill tests), `#fake:exit=1`, `#fake:followup=<agent>`.
- **Integration.** Real daemon with a temp `HOME`, temp git repos per agent, fake notifier recording calls.
- **Chaos.** 200 messages across 5 agents while the daemon is `kill -9`'d at random points 10 times; assert every message reaches exactly one terminal state and no run completes twice.
- **Static guard.** A test fails if forbidden strings (`dangerously-load-development-channels`, `--dangerously-skip-permissions` outside the explicit opt-in path, `SendMessage`, `Monitor`) appear in runner code or recorded argv.
- **Real smoke.** With `SMIF_E2E_REAL=1`: two tiny repos, one ask answered by a real worker on `haiku`, run budget $0.50.
- Race detector on in CI; coverage target 80% for `router`, `scheduler`, `store`, `runner/claude`.
### 9.5 Acceptance scenarios
| ID | Scenario | Pass criteria | Covers |
|---|---|---|---|
| A1 | Release sync: `atlas` asks `cafe` `store.status` | Reply within 5 s from the command handler; zero claude processes started | R-DEL-1, 6.1 |
| A2 | Crash routing while a `puzzle` interactive session is present | Worker cwd is the worktree or repo per profile; main checkout `git status` and file hashes unchanged; hooks printed nothing; status line shows `1 handled` | R-ISO-1, R-ISO-2, R-ISO-5, R-HUM-1 |
| A3 | Fix profile edits files | Commit on `smif/puzzle/<thread>`; `diff` shows it; `apply` merges on a clean tree and refuses on a dirty one | R-ISO-5 |
| A4 | Asset request with attachment | Artifact returned to asker; SHA-256 matches; read-only copy given to worker | 6.3, 8.2 |
| A5 | Worker returns `needs_human` | Notifier called once; `smif reply` starts a continuation in the same thread | R-HUM-2, 5.3 |
| A6 | 40 identical crash events | One run; `coalesced_count = 39` | 6.5 |
| A7 | Two agents whose rules always ask each other | Thread frozen by guard; escalated; at most 6 runs | R-CON-2 |
| A8 | `kill -9` daemon during a running run | After restart: orphan killed, retried, exactly one `done` | R-DEL-2, R-DEL-3 |
| A9 | Client sets `from`; expired run token used | Both rejected and audited | R-SEC-1 |
| A10 | Fake sleeps past timeout and spawns a child | Whole process group killed; retries then `dead_letter`; notification | R-RUN-1, R-DEL-4 |
| A11 | `release` direct run while interactive presence active | Held; runs in main checkout after `SessionEnd`; status line shows the lock while running | R-ISO-5 |
| A12 | Install then uninstall with an existing `statusLine` and hooks | Settings files byte-identical after uninstall; existing status line output preserved while installed | R-OPS-1 |
| A13 | Rotation threshold reached | New session id; `memory.md` included in next policy prompt; old id archived | R-RUN-3 |
| A14 | Static guard and argv audit | No forbidden flag or feature in code or recorded argv | R-ISO-4 |
| A15 | Daily budget exceeded | New worker runs follow `on_budget`; command handlers unaffected | R-RUN-1 |

## 10. Decisions, verification and future work
The design choices below are final for v1; the CLI details in 10.2 must be verified against the installed Claude Code before code depends on them, each with a defined fallback.
### 10.1 Decisions
| ID | Decision | Rationale |
|---|---|---|
| D1 | Push through daemon-spawned headless workers, never session injection | Only architecture that meets R-ISO-1; precedent in agent-intercom-claude and the design sketched in #27441 |
| D2 | No native `SendMessage`, channels, Monitor or Agent Teams | Bugs #86671, #77300, #43706, #23415, #44080; experimental flags (2.3, pattern 1) |
| D3 | Workers talk to peers through `followups` in their result; `onward_mcp` off by default | Daemon controls hops and threads; smaller tool surface |
| D4 | No MCP tool reads inbound mail; the human pulls via `/smif` or CLI | Enforces R-ISO-1 in code instead of in a prompt |
| D5 | Worktree per agent, branch per thread, merge only by the human | Concurrent writers are the main MAS failure source (arXiv 2608.18092) |
| D6 | One run per agent at a time; continuations instead of blocking asks | Actor semantics; no deadlocks; `--resume` cannot run concurrently |
| D7 | Deterministic command handlers are first class | Most cross-app questions (versions, store status) need no LLM |
| D8 | Own protocol, MIT license, clean-room implementation | agent-intercom-claude is AGPL-3.0 and tied to the Pi broker. **Do not copy code from it**; reading its docs for ideas is fine |
| D9 | Go, pure-Go SQLite, single binary | Fast hooks, simple install, no runtime dependencies |
| D10 | Local, single user, macOS and Linux | Scope of the problem; protocol leaves room for a relay |

### 10.2 Verify during implementation
Run each check in `smif doctor` and record results in `docs/compat.md` with the Claude Code version.
| Item | Expected | Fallback if absent |
|---|---|---|
| `-p --output-format stream-json` (may require `--verbose`) | Streamed events with init `session_id` and final result | `--output-format json`; timeouts still enforced by the supervisor |
| `--resume <id>` in `-p` mode | Continues the worker session | Pre-assign with `--session-id <uuid>` if supported; else read `session_id` from the result |
| `--append-system-prompt` in `-p` | Policy appended | Prepend the policy to the stdin prompt |
| `--allowedTools` / `--disallowedTools` with patterns like `Bash(git log:*)` | Enforced in headless mode without hanging | Profile restricted to non-Bash tools; `doctor` warns |
| Headless behaviour on a denied tool | Denied, run continues, no hang | Supervisor timeout; document |
| `--max-turns`, `--permission-mode` values | As in 6.2 | Rely on timeout; reject unknown modes |
| `--mcp-config`, `--strict-mcp-config` | Worker sees only the generated MCP set | Omit strict; document that user MCP servers load in workers |
| Native structured output (schema flag) | Validated JSON result | Parse the last fenced `json` block + repair turn |
| Minimal mode (`--safe-mode`; `--bare` rejected because it disables subscription auth, ADR-0002) | No CLAUDE.md, skills, plugins, hooks | `minimal = true` rejected by validation with a clear message |
| `--effort` | Effort per run | Ignore with a warning |
| Result fields `total_cost_usd`, `usage`, `num_turns`, `is_error`, `subtype` | Present | Budget by tokens only; cost shown as unknown |
| Hook events and payload (`cwd`, `session_id`) for `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `Stop` | As listed | Presence from status line heartbeats only |
| Status line stdin JSON includes cwd | Yes | Use the process cwd |
| Slash command shell execution syntax and required frontmatter | Output inserted only when invoked | Slash command tells the human to run `smif digest` |
| Worker hooks | User hooks also fire inside worker runs; they have side effects outside the worker (memory capture, checkpoints, nested calls) | Workers run with `--settings {"disableAllHooks":true}` unless the profile sets `hooks = true` **(ADR-0002)** |
| launchd and systemd PATH | `claude` resolvable | `init` records the absolute `claude` path |

### 10.3 Future work (not in v1)
- Runner adapters for Codex and OpenCode behind the existing `Runner` interface.
- Cross-machine relay with signed messages (ed25519, as AgentMBX does).
- A TUI or local web inbox in the spirit of agent-inbox's viewer.
- An optional counts-only notifier into live sessions if a stable MCP wake contract lands (VS Code #336056, MCP SEP-2495), or native cross-session delivery once #86671 is fixed.
- CoAgent-style concurrency control for resources shared across agents, such as a common library repo.
### Sources
- [Deep research report, 2026-11-02](https://agent.tinyfish.ai/playground/research/share/01a19ba4-b67d-71ae-93a0-ffdfd36d7b17)
- [Claude Code cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging) · [headless mode](https://code.claude.com/docs/en/headless) · [agent teams](https://code.claude.com/docs/en/agent-teams) · [what's new 2026 w32](https://code.claude.com/docs/en/whats-new/2026-w32)
- Claude Code issues: [#27441](https://github.com/anthropics/claude-code/issues/27441), [#37213](https://github.com/anthropics/claude-code/issues/37213), [#33788](https://github.com/anthropics/claude-code/issues/33788), [#86671](https://github.com/anthropics/claude-code/issues/86671), [#77300](https://github.com/anthropics/claude-code/issues/77300), [#43706](https://github.com/anthropics/claude-code/issues/43706), [#23415](https://github.com/anthropics/claude-code/issues/23415), [#44080](https://github.com/anthropics/claude-code/issues/44080), [#77495](https://github.com/anthropics/claude-code/issues/77495), [#23463](https://github.com/anthropics/claude-code/issues/23463), [#62631](https://github.com/anthropics/claude-code/issues/62631); VS Code [#336056](https://github.com/microsoft/vscode/issues/336056)
- Tools: [agent-intercom-claude](https://glama.ai/mcp/servers/dataforxyz/agent-intercom-claude), [claude-peers-mcp](https://github.com/louislva/claude-peers-mcp), [claude-code-inter-session](https://github.com/yilunzhang/claude-code-inter-session), [mcp_agent_mail](https://github.com/dicklesworthstone/mcp_agent_mail), [MCP Agent Mail (Rust)](https://mcpagentmail.com/), [AgentMBX](https://agentmbx.com/), [agent-inbox](https://github.com/shariqh/agent-inbox), [agent-message-queue](https://github.com/avivsinai/agent-message-queue/blob/main/README.md), [agent-exchange](https://github.com/krasserm/agent-exchange), [dot-agent-deck](https://github.com/vfarcic/dot-agent-deck)
- Papers: [arXiv 2510.01285](https://arxiv.org/abs/2510.01285) (blackboard), [arXiv 2608.18092](https://arxiv.org/abs/2608.18092) (concurrency control), [arXiv 2606.15376](https://arxiv.org/abs/2606.15376) (CoAgent), [arXiv 2505.02279](https://arxiv.org/abs/2505.02279) (interop protocols), [AutoGen v0.4](https://www.microsoft.com/en-us/research/articles/autogen-v0-4-reimagining-the-foundation-of-agentic-ai-for-scale-extensibility-and-robustness/) (actor model)