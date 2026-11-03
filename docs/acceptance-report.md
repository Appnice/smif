# Acceptance report (RFC 9.5)

**Result: all 15 scenarios pass**, on macOS 26 (darwin/arm64, Go 1.27.2) and on Linux (Ubuntu 24.04 in Docker,
`golang:1.27`, `make check-linux`). Run 2026-11-02 at commit after `8462afa`. Every scenario runs against a
real daemon process (built with `-race` when the suite runs under `-race`) with a temp HOME, temp git repos
per agent, the scripted fake `claude` and a file notifier. Command: `make check` (or
`go test -race -run 'TestA|TestM1|TestChaos' ./e2e/`).

| ID | Scenario | Result | Evidence (assertions in `e2e/`) |
|---|---|---|---|
| A1 | `atlas` asks `cafe` `store.status` | pass | Reply from the command handler in 160–320 ms (limit 5 s); 0 `claude -p` processes |
| A2 | Crash routing while a `puzzle` session is present | pass | Triage worker cwd = repo with `--disallowedTools Edit Write NotebookEdit`; `git status` and every file hash unchanged; hooks printed nothing; envelope wrapped as untrusted; status line `mail: puzzle 1 handled` with no message content |
| A3 | Fix profile edits files | pass | Commit `mailroom: … [run <id>]` on `mailroom/puzzle/<thread>`; worker cwd = the agent worktree; `mailroom diff` shows the file; `apply` refused on a dirty tree, merges on a clean one; ignored `node_modules/` survives `git clean -fd` |
| A4 | Asset request with attachment | pass | Artifact returned as a reply attachment, SHA-256 equal to the repo file; the asker's attachment reached the worker as a mode-0400 copy with matching hash; a non-recipient agent cannot fetch the blob |
| A5 | Worker returns `needs_human` | pass | One `needs_human` notification, summary without the raw body; status line `1 needs you`; `mailroom reply` starts a continuation in the same thread that resumes the worker session with the answer; digest shows it once, then "nothing new" |
| A6 | 40 identical crash events | pass | One message, one run, `coalesced_count = 39` |
| A7 | Two agents that always ask each other | pass | Thread frozen by the ping-pong guard (7 messages in 10 min) after 4 runs (limit 6); escalation notification; nothing left queued |
| A8 | `kill -9` daemon during a run | pass | Worker survives the daemon; after restart its process group is killed within 3 s, the attempt is `orphaned`, the retry `succeeded`, exactly one `done` transition |
| A9 | Client sets `from`; expired run token | pass | Both rejected (`-32003` / `-32001 … expired`) and audited (`message.rejected_ingress`, `auth.failed`) |
| A10 | Fake sleeps past the timeout and spawns a child | pass | Worker and child PIDs dead after each timeout (process-group kill); 2 attempts then `dead_letter`; notification; `mailroom dlq` lists it; `retry` requeues |
| A11 | `release` direct run while a session is active | pass | Held (`interactive session active`) through a heartbeat; starts after `SessionEnd`; cwd = main checkout; status line during the run `mail: atlas 1 running · worker holds repo`; lock gone afterwards |
| A12 | Install then uninstall with an existing `statusLine` and hooks | pass | Four hooks added next to the existing one; wrapped status line prints the old output first, then `mail: cafe …`; `/mailroom` command and MCP registration added; `settings.json` **byte-identical** after uninstall; if edited meanwhile, only Mailroom's entries are removed |
| A13 | Rotation threshold reached | pass | Compaction turn on the old session, `memory.md` written, next run omits `--resume`, its `--append-system-prompt` contains `memory.md`, old id on the run's `archived_session` |
| A14 | Static guard and argv audit | pass | No channels, `--dangerously-*`, SendMessage or Monitor in `internal/runner`; every built-in profile's recorded argv has `--permission-prompts none`, `--strict-mcp-config` and no forbidden flag |
| A15 | Daily budget exceeded | pass | `on_budget = "hold"`: next worker run held until midnight; `"escalate"`: `needs_human`; one budget notification per agent; command handlers on both agents still answer |

## Milestone gates

| Gate | Result |
|---|---|
| M0 `config validate` passes on the RFC 7.6 example, fails without a catch-all | pass (`make check`) |
| M1 state machine property test; `kill -9` during writes loses no acknowledged message | pass: 3 000 random transitions, only legal edges; 150 acknowledged messages all present after `kill -9` |
| M4 MCP tool list matches 7.2 exactly, no inbound-reading tool | pass (`internal/mcpserver`) |
| Chaos: 200 messages, 5 agents, 10 random `kill -9` | pass: every message terminal exactly once, no run completed twice |
| Coverage ≥ 80 % for router, scheduler, store, runner/claude | 95 %, 97 %, 87 %, 90 % |
| Real Claude smoke (`MAILROOM_E2E_REAL=1`, haiku, $0.50 cap) | pass: correct answer from a real worker, $0.0053, 4.8 s |
| Linux (`make check-linux`) | pass |

## Fixed while testing

- Fake `--help` lacked `stream-json`; the flag probe correctly held all workers until it was fixed.
- A8 first passed even with orphan killing disabled (the fake exited by itself); the scenario now uses a
  60 s first attempt, and a mutation check confirms it fails when orphans are not killed. A10 likewise fails
  when only the leader (not the process group) is killed.
- A10 was timing-sensitive under `-race` with a 1 s timeout; it now uses 2 s.
- The race detector found an unsynchronised session callback in the runner; it is now synchronous.
