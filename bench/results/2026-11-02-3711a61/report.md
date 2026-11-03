# Mailroom benchmark 2026-11-02 18:35

- commit `3711a61` (uncommitted changes), darwin/arm64, Go 1.27.2
- machine: an Apple silicon laptop, 15 CPUs, macOS 26
- mode: full; took 1m27s; money spent: $0 (T-real used the existing Claude login on haiku; no API key)

Every scenario starts a fresh daemon in /tmp with the fake claude (`testdata/fakeclaude`), so the numbers are Mailroom's own cost, not the model's. Plan and method: `docs/rfc-benchmark.md`. Token counts marked ≈ are bytes/4 estimates; T-real (with `-real`) has actual counts.

## S · system overhead

### S1 · ask answered by a command handler (round trip)

Open loop, 20 asks/s for 20s, each waits for the reply. Latency counts from the scheduled send time.

| | n | p50 | p90 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|
| ask → reply | 400 | 24.61 ms | 26.60 ms | 30.35 ms | 161.46 ms | 161.46 ms |

Errors: 0 of 400.

### S2 · a tell accepted and routed (ingest)

Open loop, 200 tells/s for 20s to an agent whose rule is `digest`. The call returns once the message is stored (SQLite, synchronous=FULL) and routed.

| | n | p50 | p90 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|
| send → stored | 4000 | 10.14 ms | 19.95 ms | 23.12 ms | 25.21 ms | 27.04 ms |

Errors: 0 of 4000.

### S5 · what the human's session pays: hook and status line

Each is a `mailroom` process (exec included), as Claude Code runs it. Budgets: hook under 200 ms, status line under 300 ms.

| | n | p50 | p90 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|
| hook, idle daemon | 30 | 5.44 ms | 5.90 ms | 6.40 ms | 6.40 ms | 6.40 ms |
| hook, during 200 tells/s | 30 | 9.71 ms | 10.10 ms | 21.16 ms | 21.16 ms | 21.16 ms |
| status line, idle | 30 | 5.65 ms | 6.04 ms | 6.38 ms | 6.38 ms | 6.38 ms |
| status line, during 200 tells/s | 30 | 9.95 ms | 10.37 ms | 15.17 ms | 15.17 ms | 15.17 ms |

Bytes the hook printed into the session over 30 calls under load: **0** (it must print nothing).

### S3 · what Mailroom adds around one worker run

30 tells to a worker agent, one at a time (fake claude, no sleep). Phases from the daemon's own timestamps (ms resolution).

| | n | p50 | p90 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|
| end to end (accepted → done) | 30 | 31.00 ms | 33.00 ms | 41.00 ms | 41.00 ms | 41.00 ms |
| accepted → process start | 30 | 5.00 ms | 6.00 ms | 7.00 ms | 7.00 ms | 7.00 ms |
| run: prepare, spawn, stream parse, exit | 30 | 26.00 ms | 28.00 ms | 36.00 ms | 36.00 ms | 36.00 ms |
| process exit → done (result, audit) | 30 | 0.00 ms | 0.00 ms | 0.00 ms | 0.00 ms | 0.00 ms |
| the same fake run directly (baseline) | 30 | 2.63 ms | 3.77 ms | 5.88 ms | 5.88 ms | 5.88 ms |

Mailroom's own cost per worker message ≈ end to end − the bare process = **28.37 ms** (p50).

### S4 · a storm of the same crash event (dedupe)

500 identical events (same `dedupe_key`) at 250/s to a worker agent; each worker run takes 1 s.

| | count |
|---|---|
| events sent | 500 |
| messages created | 2 |
| events merged into an open message | 498 |
| worker runs | **2** |
| errors | 0 |

| | n | p50 | p90 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|
| emit → stored or merged | 500 | 44.96 ms | 64.09 ms | 65.10 ms | 65.36 ms | 65.36 ms |

T3 (storm part): the merged events never start a run. At the T1 overhead plus this body, that is about **287471 tokens** of worker input not spent (bytes/4), before the worker's own reading of the repo, which is usually far larger.

## R · robustness and safety

### R1 · kill -9 with 100 messages in flight

100 tells to 4 worker agents (0.5 s per run), `kill -9` of the daemon after 0.7 s, restart.

| check | result |
|---|---|
| not yet done at the kill | 92 |
| accepted messages lost | **0** |
| not done after recovery | 0 |
| messages with more runs than max_attempts (3) | 0 |
| time from kill to all done | 12.9s |
| wait error | none |

A run cut by the kill is re-run (at-least-once for the handler, exactly-once for the message state).

### R2 · two workers that always ask each other back

| check | result |
|---|---|
| thread frozen | true (ping_pong: 7 messages between pong and ping within 10m0s) |
| messages in the thread | 8 |
| worker runs (tokens spent) | **4** |
| time to freeze | 100ms |

Without the guards this loop would run until the budget stops it.

### R3 · inbound text never reaches the human's session (canary)

20 messages to an agent with an open interactive session, each carrying the unique string `CANARY-17936…`. Then every way into that session was read: the hook output (3 events), the status line, `mailroom_status` and `mailroom_agents`, the MCP tool definitions and instructions.

| check | result |
|---|---|
| probes | 7 |
| probes that contained the canary | **0** |

The digest (`/mailroom`) shows summaries only when the human types it; it is not probed here.

## T · tokens

### T1 · tokens Mailroom adds to each worker run

From the argv and stdin of a real worker invocation (profile `triage`, a 204-byte body). Token counts are bytes/4 estimates.

| added to every worker run | bytes | ≈ tokens |
|---|---|---|
| policy (`--append-system-prompt`) | 834 | 209 |
| prompt (envelope + instructions) minus the body | 634 | 159 |
| result schema (`--json-schema`, the StructuredOutput tool) | 801 | 201 |
| **total Mailroom adds** | **2269** | **≈ 568** |

What Mailroom takes away from a worker's context, compared with a plain `claude -p` in the repo:

- `--tools Read,Grep,Glob,Bash` (4 tools): every built-in tool not listed is not defined in the context at all.
- `--strict-mcp-config --mcp-config mcp.json`: no MCP servers load (a plain session here loads every user and claude.ai server; on this Mac that was 12).
- `--settings {"disableAllHooks":true}`: no hook output enters the worker.
- Command handlers use no tokens at all.

The real sample (`-real`) measures the net effect in actual tokens.

### T2 · tokens Mailroom costs the human's session

| channel | bytes | ≈ tokens | when |
|---|---|---|---|
| MCP tool definitions (5 tools) | 2627 | ≈ 657 | in every session's context (cached after the first turn) |
| MCP server instructions | 166 | ≈ 42 | same |
| hook output | 0 | 0 | every prompt (hooks print nothing) |
| status line `mail: app 20 handled` | 21 | 0 | shown in the terminal only, never sent to the model |
| inbound messages (R3: 20 messages) | 0 | 0 | never; workers and handlers take them |
| `/mailroom` digest | on demand | – | only when the human types it |

### T-real · actual tokens of one handled question (haiku, subscription)

The same question, 2 trials each: a Mailroom worker (profile `ro`: Read/Grep/Glob, no MCP, no hooks) versus a plain `claude -p` in the same repo with the user's normal setup. Means per run; the cost column is what claude reports (API-equivalent; on a subscription it is not billed).

| | input | cache write | cache read | output | **context tokens** | reported cost |
|---|---|---|---|---|---|---|
| Mailroom worker | 4 | 4087 | 11359 | 231 | **15450** | $0.0019 |
| plain claude -p | 4 | 20826 | 43092 | 111 | **63922** | $0.0047 |

