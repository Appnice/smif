# ADR-0011: `stats.get` and `mailroom stats`

**Status:** accepted, 2026-11-02.

**Context.** The owner uses Mailroom daily for two weeks and then decides whether it earns its place. The decision
needs four numbers: how much went through, how much was handled without him, how much needed him, and what it
cost in tokens.

**Decision.** A human-only daemon method `stats.get {since}` (default the last 14 days) and `mailroom stats
[--days N | --since YYYY-MM-DD] [--json]`, per agent and per day. Definitions:

- **inbound**: asks, tells and events created in the window (by `created_at`); replies are counted apart.
- Each inbound message has exactly one outcome: **without you** (done and never in needs_human, split by
  handler), **needed you** (a transition to needs_human in the audit trail, then answered, waiting or
  cancelled), **cancelled by you**, **dead**, **stopped by a guard** (rejected by an allowlist, loop or rule),
  **expired**, **in progress**. The outcomes add up to inbound (tested).
- **merged**: `message.coalesced` audit rows in the window (dedupe repeats that started no run).
- **runs, tokens, cost**: runs by `started_at`; tokens are input including cache reads and writes, plus output;
  the cost is what claude reports (API-equivalent, not billed on a subscription).
- The work share printed by the CLI is without you / (without you + needed you + dead).

Data older than `message_retention` (default 90 days) is gone and not counted. No agent can call it (D4):
`e2e/stats_test.go` checks the counts on known traffic, against `run.list`, and the forbidden agent call.
