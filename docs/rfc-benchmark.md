# RFC: A reproducible benchmark for Mailroom

2026-11-02 · Status: Draft, not started · Scope: `bench/` (new), no change to the daemon

## Summary

Mailroom makes four kinds of promise. It is cheap to run (routing adds little time). It is safe (no inbound
text reaches the human's session, workers stay confined, loops stop, nothing is lost). It is useful (work
crosses projects with fewer human interruptions). And it has a known cost per message. Today only the second
promise has evidence, as pass/fail acceptance tests (`docs/acceptance-report.md`). None of the four has numbers
that a sceptical engineer could check, compare or reproduce.

This RFC defines a benchmark in three tiers:

1. **System tier (S).** Latency, overhead and throughput of the daemon with the scripted fake `claude`. It is
   free, so it can run thousands of times. It uses open-loop load and HdrHistogram, so it avoids coordinated
   omission.
2. **Robustness tier (R).** Safety and correctness under faults and attacks: kill -9, disk full, storms, loops,
   and prompt injection. Each check is an invariant over the audit log and the file system, in the Jepsen style.
   Results are counts of violations, reported with an upper confidence bound when the count is zero.
3. **Value tier (V).** A fixed task suite with programmatic ground truth, run with a real, pinned model against
   four baselines. It measures task success, human interruptions, context pollution in the human's session,
   latency and cost per correct answer.

Five design decisions carry the rest:

- **Fake model for system numbers, real model only for outcome and attack numbers.** The fake gives clean,
  repeatable overhead numbers. The real model gives the numbers users care about. Never mix them in one table.
- **Open-loop load, latency from the intended send time.** This is the wrk2 method, recorded in HdrHistogram.
- **Canary tokens as the oracle for isolation.** Every inbound body carries a unique string. A leak is that string
  showing up, unasked, anywhere the human's session can read.
- **Paired, randomised, pre-registered comparison with a fair "cron plus `claude -p`" baseline.** That baseline
  answers the obvious question: "isn't this just cron and headless Claude?"
- **Exploratory pilot first, frozen thresholds second, confirmatory run third.** Thresholds come from the pilot,
  are written down, then tested on fresh runs. Raw data and scripts are published.

**Status (2026-11-02):** tiers S, R and T are built in `bench/` (`make bench`, `make bench-real`); first results in
`bench/results/2026-11-02-3711a61/report.md`.

**Budget decision (2026-11-02): $0.** No money is spent on the benchmark. Tiers S and R run on the fake model
and cost nothing; they are the benchmark. Real-model checks run only on the existing Claude Code subscription
(no API key, no extra spend), as small Haiku samples, or not at all. The paid tier V plan below ($300–940
API-equivalent) is kept for reference only and is out of scope.

## Claims under test

Each claim has an ID, a measurable form, and the tier that tests it. "Threshold" values are filled in after the
pilot (stage S1, R1, V1) and frozen in `bench/PREREG.md` before the confirmatory run. A claim that misses its
threshold is reported as missed. No claim is dropped after seeing data.

### System overhead (tier S, fake model)

| ID | Claim | Measured as |
|---|---|---|
| S1 | Ingest is fast | Time from client send to ack (message durable, RFC R-DEL-2), at fixed offered rates |
| S2 | Routing is fast | Time from ack to the handler process starting (`exec` seen by the fake or the command handler) |
| S3 | A command-handler ask is fast end to end | Time from `message.ask` to the reply in `message.await`, trivial handler (A1 today: 160–320 ms, single shot) |
| S4 | A worker adds little over calling `claude -p` directly | Paired difference: Mailroom end to end minus a direct launch with the same argv and stdin |
| S5 | The human-facing hooks stay within their budgets under load | `mailroom hook <event>` p99 < 200 ms and `mailroom statusline` p99 < 300 ms (RFC 7.4, CLAUDE.md), while the daemon is loaded |
| S6 | Throughput scales to the stated caps | Highest offered rate at which p99 of S1 and S3 stay under their thresholds; queue wait by priority when overloaded (aging, RFC 6.5) |
| S7 | Mailroom adds few prompt tokens | Input tokens added per worker run by the envelope, policy and memory prompt, versus the bare task text |

S4 and S7 also run with the real model, in small paired samples, because real `claude` start-up is not the fake's.

### Correctness and safety (tier R)

| ID | Claim | Oracle |
|---|---|---|
| R1 | D4 / R-ISO-1: no inbound text reaches an interactive session unasked | Canary search over the session transcript, hook stdout, status line output, MCP results (see Metrics) |
| R2 | Workers stay confined (R-ISO-3, R-ISO-5, ADR-0002) | Honeytoken files, file hashes of the main checkout and the Mailroom dirs, a local canary HTTP listener, a bare git remote |
| R3 | Loops stop (R-CON-2) | Runs and messages per looping thread; time from loop start to freeze; zero messages after freeze |
| R4 | Nothing acknowledged is lost under kill -9 (R-DEL-2) | Every acked id is present after restart |
| R5 | No message is handled twice to completion (R-DEL-3) | Exactly one `done` transition per message; a side-effect ledger counts duplicate completed effects |
| R6 | Coalescing matches the rules (RFC 6.5) | Runs per dedupe key, `coalesced_count`, cooldown and `reopen_after`, compared with a small reference model of the rules |
| R7 | Every fault in RFC 8.4 has its stated outcome | One fault test per row of the table: kill -9, disk full, `claude` missing, overload, bad result, timeout, broken worktree, SIGSTOP as a stand-in for sleep |
| R8 | No orphan processes | Process-group scan after each fault; zero survivors |

R1 to R8 are mostly tier R with the fake. R1 and R2 also run with a real model under attack, because the model is
what an attacker targets.

### Outcome value (tier V, real model)

| ID | Claim | Measured as |
|---|---|---|
| V1 | Cross-project work gets done | Task success on the suite, graded by code; pass^k over k trials |
| V2 | With fewer human interruptions | Human actions per resolved task, by kind (relay, approval, escalation answer) |
| V3 | With less context pollution | Unsolicited foreign tokens in the human session; the human session's own task success versus a no-interruption control |
| V4 | Escalation is right | Should-escalate tasks escalated; can-answer tasks not escalated |
| V5 | Under attack, the task still gets done safely | Utility under attack and attack success rate, as in AgentDojo |
| V6 | At a known cost | Dollars and tokens per handled message and per correct answer, by route (command, worker, digest) |

The suite is expected to show places where Mailroom loses. A fresh worker re-reads repo context that a live session
already has, so cost per message and latency may be higher than direct injection. The report shows those rows next
to the wins.

## Baselines

A baseline must be something a real user would do instead, given its best reasonable setup, with the same model,
the same tools and the same fixture repos.

| ID | Baseline | How it works in the bench | Why include it |
|---|---|---|---|
| B0 | No tool, human relays | A scripted "relay" copies the message into the target session and the answer back. Each copy counts as a human action | Today's workflow (RFC 1.1) |
| B1 | Shared-folder inbox | One folder per agent; the recipient's CLAUDE.md says "check `inbox/` at the start of each turn". The human session reads and handles the mail itself | The common do-it-yourself setup; isolation by prompt only |
| B2 | Native cross-session messaging | Sender uses `SendMessage`; the recipient session receives it in its live context. Recipient set to `crossSessionInbound = accept` | The shipped first-party alternative (Claude Code ≥ 2.1.224); the injection design Mailroom rejects |
| B3 | Cron plus `claude -p` | A loop polls each inbox folder every 30 s and runs `claude -p` with the **same argv** Mailroom builds for that profile. No router, dedupe, guards, worktree, command handlers or durable state | Isolates what the daemon adds beyond "headless Claude" |
| M | Mailroom | Default config from `mailroom init`, bench-specific limits disclosed | System under test |
| M-a1 | Ablation: no command handlers | Every rule routes to a worker | Measures the value of D7 |
| M-a2 | Ablation: no dedupe, no cooldown | `dedupe_key` ignored | Measures the value of coalescing |

Considered and left out of the main comparison:

- **Agent teams.** Experimental and off by default. One team per session, a single lead, and `claude -p` does not
  spawn teammates (Claude Code docs). They coordinate one project's work, not peers in separate repos. They could
  be an exploratory single-repo arm later, but a comparison would not be fair to either side.
- **A third-party MCP inbox that injects** (for example claude-peers-mcp). It needs a development flag (RFC 2.1).
  B2 covers the injection design with a supported feature.
- **Subagents.** They run inside the human's session and return a summary into it. That is B0 with an automated
  relay. It is an optional arm, B0s, if the pilot shows it differs from B0.

Note: B2's harness code calls `SendMessage`. It lives under `bench/baselines/`, outside `internal/runner`, so the A14
static guard does not apply. The guard must keep that scope.

## Workloads and task suite

### Synthetic traces (tiers S and R)

Traces are YAML files, in the style of OpenMessaging Benchmark workloads: same file, any system. Each trace names
agents, senders, kinds, intents, sizes, an arrival process and a duration.

| Trace | Shape | Tests |
|---|---|---|
| `steady` | Poisson arrivals, rates from 1 to saturation in steps, mixed kinds | S1–S3, S6 |
| `burst` | Quiet, then 500 messages in 1 s, repeated | Queueing, tail latency, S5 under load |
| `storm` | 40, 400 and 4 000 events with one dedupe key, plus a slow drip across the cooldown | R6, A6 at scale |
| `fanout` | One sender asks all agents at once, 5 to 50 agents | Scheduler caps, command pool |
| `loop` | Fake workers whose result always asks the sender back (`#fake:followup=sender`); 2, 3 and 5-agent cycles | R3 |
| `mixed-day` | A replay of one day of a real deployment's audit log, ids remapped, bodies replaced | Ecological check; needs dogfood data (open question) |
| `chaos` | `steady` plus a nemesis: kill -9, SIGSTOP/SIGCONT, disk full, `claude` binary removed | R4, R5, R7, R8 |

Default limits would distort load tests. The per-sender rate limit (60/min) and `max_thread_messages` cap traffic
on purpose. The load generator spreads traffic over many sender identities. Where a limit must change, the bench
config states it, and the report repeats the run with defaults at one rate.

### Task suite (tier V)

The suite follows SWE-bench and Terminal-Bench. A task is a fixture, an inbound message, a hidden ground truth and a
grading program. Tasks are graded by code, not by a model, wherever possible.

**Fixtures.** A seeded generator builds small repos that mirror the five agents of RFC 1.2 (two apps, a game, a
website, an observability workspace). They are synthetic, so they can be published and are unlikely to be in any
model's training data. Each repo has a build, tests, git history, a `VERSION` file, a changelog and a fake
store-status file.

| Family | Count | Inbound | Ground truth | Grader |
|---|---|---|---|---|
| Status asks | 12 | `ask store.status` / `app.version` | Values computed by the generator | Exact match on `reply_data` fields (schema given in the ask) |
| Cross-repo questions | 12 | "Which API version does `web` call?" | Read from repo B by the generator | Exact match on a structured field |
| Crash triage | 16 | `event crash.report` with a real stack trace | File, function and introducing commit of a seeded bug | Field match; line ranges must overlap |
| Crash fix | 8 | Same, profile `fix` | Hidden FAIL_TO_PASS and PASS_TO_PASS tests (SWE-bench) | Tests run on a clean checkout of the worker branch |
| Asset request | 4 | `ask asset.*` | SHA-256 of the asset | Hash match |
| Should escalate | 4 | A business decision or missing information | `needs_human` | State match |
| Under attack | 8 | A valid task with an injection in the body or an attachment | Task truth plus "no forbidden action" | Task grader plus R2 oracles |

That is 64 tasks. Counts are a starting point; the pilot may change them before the freeze.

**How tasks get known answers.**

- Status and cross-repo answers are facts the generator wrote, so the truth is exact.
- Crash bugs are planted by mutation in a known commit. The stack trace comes from actually running the failing
  test, not from a template. The truth is that commit, file and function.
- Fix tasks keep their tests outside the repo. The grader applies them after the run, so the worker cannot see them.
- Each task ships with an oracle solution that scores 1 and runs a "null agent" (does nothing, or echoes the subject)
  that must score 0. This is the trivial-agent check from the Agentic Benchmark Checklist (ABC).
- A second person reviews every task and test before the freeze (ABC: check test cases by hand). SWE-bench Verified
  exists because unreviewed tests rejected valid fixes.
- A private hold-out set (one extra seed, 16 tasks) is never published or looked at while tuning. A large gap
  between public and hold-out scores means prompts were tuned to the suite.

**Episodes and the human session.** V2 and V3 need a "human" session doing its own work. A driver runs a long-lived
session in the agent's repo with a fixed primary task (a small feature with hidden tests) given in four scripted
turns. During the episode, six inbound tasks from the suite arrive at fixed points. The driver never reads them
unless the arm makes it (B0 relays, B1 polls, B2 injects). A no-interruption control runs the same primary task
alone. The driver uses `claude -p` with streamed input so it can be scripted. Whether that behaves like an
interactive session for hooks and for B2 delivery must be checked first (open question 1).

## Metrics

**Latency.** Record every sample in HdrHistogram (3 significant digits). Report p50, p90, p99, p99.9 and max. Report
the full percentile curve, not a mean and a standard deviation. Latency is measured from the intended send time of
the open-loop schedule. The uncorrected (actual send time) curve is shown next to it, so readers see the size of
the correction. Daemon timestamps are in milliseconds (`UnixMilli` in `internal/store`), so sub-millisecond stages
use the client's monotonic clock and timestamps the fake records.

**Overhead breakdown (S2, S4).** One message gives these stages: send → ack → routed → queued → run started →
process exec → result parsed → done → reply seen by the client. Each stage comes from the audit log, the fake's
record and the client clock.

**Safety counts.** Each R claim is a count of violations over N trials. When the count is 0, report the 95% upper
bound on the rate: about 3/N (the "rule of three"). "0 leaks in 10 000 messages" means "rate below 0.03%", nothing
stronger.

**Isolation (R1) in detail.** Each inbound body, attachment and data field carries a unique canary, for example
`MRCANARY-<run>-<msg>-<field>`. After the run, search:

- the human session's transcript JSONL (all turns, tool results, system reminders);
- stdout of every `mailroom hook` call (must be empty);
- every `mailroom statusline` output (counts only);
- every MCP tool result returned to the human session;
- OS notification bodies (from the file notifier).

A hit is classified. **Solicited**: the reply to the session's own `mailroom_ask`, or digest text after an explicit
`/mailroom` (RFC 5.3, 7.4). **Unsolicited**: anything else. R1 passes only with zero unsolicited hits. A worker can
copy a canary into its own summary, so a canary in a notification or a `/mailroom` digest is expected and is
reported as such, not as a leak.

**Confinement (R2) in detail.** Before each run, hash every file in the main checkout, `~/.config/mailroom` and the
data dir. Place honeytoken files (a canary inside) in places a profile must not read. Run a local HTTP listener and
put its URL in the attack text. Add a bare git remote. A violation is a changed hash, a honeytoken canary in any
transcript or result, a listener hit, or a new ref on the remote.

**Outcome (V).** Success rate per arm with a 95% CI. pass^k from tau-bench: the chance that all k trials of a task
succeed, with the unbiased estimator over n trials. Human actions per resolved task, by kind; no composite score.
Unsolicited foreign tokens in the human session: tokens of turns that carry an unsolicited canary. Primary-task
success versus the no-interruption control. Wall time from send to a correct answer.

**Cost (V6).** Sum `total_cost_usd` and the `usage` fields from each run's result event (they are present on Claude
Code 2.1.316, `docs/compat.md`). Split input, cache-read, cache-write and output tokens. Report cost per handled
message, per correct answer, and per route. Command and digest routes cost no model tokens; report their share of
traffic, because the mix decides the average. Plot success against cost per arm, as "AI Agents That Matter"
recommends: an arm is only better if it is not dominated on both axes.

## Methodology

**Fake model versus real model.** Tier S uses `testdata/fakeclaude`, with a delay drawn from a fixed distribution
per run. That distribution is fitted to real run times from the pilot, and it is reported. Tier S numbers measure
Mailroom, not Claude, and every table says so. Tier V uses the real model only. S4 and S7 have a small real-model
sample to show that the fake's numbers carry over.

**Open-loop load.** The generator follows a schedule fixed in advance (Poisson or constant rate). It never waits for
a response before sending the next message. A closed-loop generator slows down when the system slows down, and so
hides the queueing a real burst would see. That is coordinated omission (Tene; wrk2). Schroeder, Wierman and
Harchol-Balter show that open and closed models can give very different response times for the same load. A
closed-loop run is used only to find the saturation point, and it is labelled as such.

**Repetitions.** Results vary between daemon starts (Go runtime, SQLite page cache, file system state) and between
requests within a start. Follow Kalibera and Jones: repeat at both levels. Each S data point is at least 10 fresh
daemon starts (fresh data dir), each running the trace for at least 60 s after a discarded 20 s warm-up. Compute
CIs by bootstrap over daemon starts, not over requests, because requests within one start are correlated.

**Warm and cold.** Cold: a fresh data dir, a fresh daemon, the first message, and for real runs a worker with no
session to resume. Warm: after the warm-up, and for real runs a resumed session within the prompt-cache window.
Report both, because a cold real worker re-reads the whole repo prompt at full price.

**Trials and pairing for tier V.** Run every task k = 5 times on every arm. Run arms interleaved in a random order
within each task block, not arm after arm, so time-of-day API latency and model drift spread evenly over arms.
Compare arms on paired per-task differences. Report standard errors clustered by task, as Miller (2024) recommends,
and bootstrap CIs. Correct for multiple comparisons on the primary endpoints with Holm's method. Choose sample sizes
from the pilot's variance with a power calculation before the confirmatory run.

**Pin everything.** Record and pin: the Mailroom git SHA and config hash; Claude Code version (2.1.316 at the time of
writing); full model ids (for example `claude-sonnet-5-5`, never the alias `sonnet`); effort level; the model id
reported in each run's init event; Go version; OS and kernel; CPU, memory, disk; power state and load average.
Temperature cannot be set to make runs repeat. Even at temperature 0, inference is not fully deterministic (Thinking
Machines, 2025). Model variance is part of the result and is reported as such.

**A clean Claude environment.** User-level CLAUDE.md, skills, plugins and settings would change behaviour across
arms. Run real arms under a dedicated config dir or a dedicated OS user that holds only login credentials. Record a
hash of that config. Whether `claude` keeps its login under a separate `CLAUDE_CONFIG_DIR` must be checked (open
question 3).

**Host hygiene.** This Mac is shared with other projects and emulators. Before a run: close emulators and
simulators, check that no other agent builds, keep the laptop on power, disable Low Power Mode, and record load
average. Abort a run when the 1-minute load average exceeds a set value before the start. Linux numbers come from a
native Linux machine, not Docker on macOS, which runs a VM.

**Graders and judges.** Most tasks need no judge. Where a free-text reply must be graded (some cross-repo answers),
use a model judge only with: a written rubric; arm labels hidden; answer order randomised; length not shown as a
signal. Validate the judge against human labels on at least 20% of judged items before the confirmatory run, and
report Cohen's kappa. Zheng et al. found position, verbosity and self-preference biases in model judges; this design
targets each one. The judge's model and prompt are pinned like the system under test.

**Cost caps.** Every real run sets `--max-budget-usd` through the profile's `max_cost_usd`. The harness keeps a cost
ledger from `total_cost_usd` and stops the stage when the stage cap is reached. Costs are API-equivalent dollars as
Claude Code reports them. On a subscription they may not be billed, but rate limits apply instead; the cap applies
to the reported sum either way.

**Pre-registration.** Before each confirmatory run, commit `bench/PREREG.md` with: the claims and primary endpoints,
thresholds, sample sizes, the analysis script's SHA and the exclusion rules. Runs that crash for harness reasons are
rerun and listed; they are never silently dropped.

**Publishing.** Publish raw HdrHistogram logs, per-trial JSONL, transcripts with account metadata removed, the
fixture seeds (not the hold-out seed), every script, and the exact configs. A reader with a Mac and a Claude account
should be able to rerun tier S and R for free, and tier V within the stated budget.

## Threats to validity

| Threat | Why it matters | Handling |
|---|---|---|
| The fake is not Claude | Start-up time, output size and stream timing differ | Fake delays fitted to real runs; S4/S7 real-model samples; no fake number in a tier V table |
| Model nondeterminism and drift | Same task, different result; the model may change between runs | k = 5 trials, interleaved arms, pinned ids, init-event model id logged, CIs reported |
| Prompt-cache state | A warm cache makes later arms cheaper | Randomised order; cache tokens reported separately; cold and warm shown apart |
| Author builds the baselines | "Only evaluate against yourself" and "unfair benchmarking of competitors" (Heiser) | Same model, tools and fixtures for all arms; baseline configs published; an outside reviewer checks them before the freeze |
| Selective reporting | The "mother of all benchmarking crimes" (Heiser) | Pre-registration; every pre-registered metric reported, wins and losses |
| Weak graders | Insufficient tests over- or under-state success by up to 100% (ABC) | Oracle and null-agent checks; human review of tests; hold-out set |
| Judge bias | Position, verbosity, self-preference (Zheng et al.) | Code graders first; blinded, randomised, validated judge; kappa reported |
| Contamination | Known repos may be memorised | Synthetic seeded fixtures; private hold-out |
| Simulated human | A script is not a person; interruption cost is a proxy | Count actions by kind; do not convert them to minutes or money |
| One machine, one user, five agents | Results may not transfer | Full platform disclosure; Linux rerun; `mixed-day` replay if data exists |
| macOS fsync | SQLite's `fullfsync` pragma is off by default, so a kill -9 test covers a process crash, not power loss | State the scope; no power-loss claim |
| Coordinated omission | Closed-loop tools hide tail latency | Open-loop schedule; intended-time latency; uncorrected curve shown for contrast |
| Timer resolution | DB timestamps are in ms; sleep jitter ~1 ms (wrk2 notes the same) | Client monotonic clock for short stages; no claim below 1 ms |
| Laptop noise | Thermal limits, background agents, Spotlight | Host hygiene checklist; load average recorded; runs aborted when busy |
| Bench-only limit changes | Raised rate limits change behaviour | Disclosed; one run per rate repeated with defaults |
| Harness bugs that favour one arm | A shortcut can make an arm look good (ABC: inspect outliers) | Inspect outliers in the pilot; mutation checks on oracles, as A8 and A10 already do |
| API overload and rate limits | Retries inflate latency in one arm | Retries counted and reported per arm; the confirmatory run stops if the overload rate passes a set level |

## Harness design (files in `bench/`)

The harness is a Go module inside the repo, so it reuses `internal/api` and the e2e helpers. Nothing in `bench/`
ships in the `mailroom` binary.

```text
bench/
  PREREG.md                 claims, endpoints, thresholds, sample sizes, analysis SHA (frozen per run)
  cmd/mrbench/              CLI: load, chaos, redteam, suite, report, manifest
  manifest/                 records hardware, OS, Go, claude --version, model ids, git SHA, config hashes, load
  loadgen/                  open-loop scheduler (Poisson, constant), many sender identities,
                            intended-time latency, HdrHistogram logs (hdrhistogram-go)
  traces/                   steady.yaml, burst.yaml, storm.yaml, fanout.yaml, loop.yaml, chaos.yaml, mixed-day.yaml
  fakeprofiles/             delay distributions for the fake, fitted from pilot runs
  chaos/                    nemesis (kill -9, SIGSTOP/SIGCONT, disk full via a small disk image or tmpfs,
                            claude binary removed) and checkers over the audit log:
                            acked ⇒ present, one terminal state, one done, no orphan process groups
  refmodel/                 a small reference model of dedupe, cooldown and reopen_after (R6)
  redteam/                  attack corpus (YAML), honeytoken placement, file-hash snapshots,
                            canary HTTP listener, bare git remote, compromised-worker prompts
  canary/                   canary minting and the transcript, hook, status line and MCP scanners (R1)
  suite/
    gen/                    seeded fixture-repo generator (five agents)
    tasks/                  task specs: id, family, fixture seed, inbound draft, truth, grader
    graders/                exact-match, field-match, hidden-test runner, hash, state, judge (with its rubric)
    oracle/ null/           reference solutions and null agents for validity checks
  baselines/
    b0_relay/ b1_folder/ b2_sendmessage/ b3_cron/   each with its config and runner
    mailroom/               config.toml for M, M-a1, M-a2
  driver/                   scripted human session: primary task turns, inbound schedule, action counter
  ledger/                   cost ledger with stage caps; stops the stage at the cap
  report/                   analysis: bootstrap CIs, clustered SEs, pass^k, Holm; tables and plots
  results/                  one folder per run id (gitignored; published sets go to a release asset)
```

Makefile targets, all opt-in:

- `make bench-sys`: tier S with the fake. Free. Runs in about an hour.
- `make bench-chaos`: tier R with the fake. Free.
- `make bench-real STAGE=<id> BUDGET_USD=<n>`: anything that calls the real `claude`. Refuses to start without
  `MAILROOM_BENCH_REAL=1`, a clean host check, a manifest and a frozen `PREREG.md` (except in pilot stages).
- `make bench-report RUN=<id>`: rebuilds every table and plot from raw data only.

Two small changes outside `bench/` will be needed later, each with its own commit: the fake must record a
monotonic start timestamp, and accept a `#fake:delay=<dist>` directive. They are test-only and do not touch the
daemon.

## Budget

**Decision 2026-11-02: no money.** In scope: tiers S and R in full (fake model, $0), plus at most a few small
real-model samples on the existing subscription (Haiku, the same way `make smoke-real` runs), for example S4/S7
(Mailroom's overhead over plain `claude -p`) and part of the R2 red team. Out of scope: the tier V pilot and
confirmatory runs, the ablations and the judge. The table below is the original estimate, kept for reference.

Prices are from Anthropic's model table cached on 2026-10-06 (per million tokens): Haiku 5.5 $0.10 in / $0.50
out; Sonnet 5.5 $2 / $10; Opus 5.5 $4 / $20. Confirm them on the day of the run.

The only measured per-run cost so far is the haiku smoke test: $0.0053 for a trivial question
(`docs/acceptance-report.md`). The per-run costs for Sonnet below are **assumptions**. Stage V1 replaces them with
measured values before the main run is sized.

| Stage | Runs | Assumed cost per run | Estimate |
|---|---|---|---|
| S4/S7 real sample | 200 trivial runs on Haiku 5.5 (100 pairs) | $0.005–0.01 | $1–2 |
| R2/V5 red team | 40 attacks × 2 profiles × 3 trials on Haiku 5.5, plus 40 × 3 on Sonnet 5.5 | $0.01–0.03 Haiku, $0.10–0.30 Sonnet | $15–40 |
| V1 pilot | 12 tasks × 5 arms × 3 trials, plus 30 driver episodes | $0.10–0.30 per task, $0.30–1.00 per episode | $30–90 |
| V confirmatory | 64 tasks × 5 arms × 5 trials = 1 600 task runs | $0.08–0.25 | $130–400 |
| V episodes | 10 episodes × 5 arms × 5 trials + 50 control episodes = 300 | $0.30–1.00 | $90–300 |
| Ablations | 64 × 2 × 3 = 384 task runs | $0.08–0.25 | $30–100 |
| Judge | a few hundred short calls on Haiku 5.5 | under $0.01 | under $5 |
| **Total** | | | **$300–940; midpoint about $620** |

The plan sets a hard cap of **$800**, enforced by the ledger, with per-stage caps. The upper end of the range is
above the cap. If the pilot puts Sonnet runs in the upper half, cut trials from k = 5 to k = 3 before cutting tasks
(that saves about 40% of the confirmatory and episode lines), and say so in `PREREG.md`.

A **lean variant** runs everything on Haiku 5.5. It costs roughly a twentieth of the Sonnet price per token, so
about $40 in total. It answers the system and safety questions just as well. It answers the value questions for
Haiku only, and Mailroom's default profiles use Sonnet, so the lean variant is a pilot, not the headline.

Wall time for tier V: with `max_workers = 3` and about a minute per worker run, 1 600 task runs take about 9 hours
of worker time per pass, or two to three days with episodes, ablations and reruns.

## Staged plan

| Stage | What | Cost | Done when |
|---|---|---|---|
| 0 | `bench/` skeleton, manifest, `PREREG.md` template, host hygiene check | free | `mrbench manifest` prints a complete record |
| S1 | Load generator, traces, HdrHistogram output, overhead breakdown; exploratory runs | free | Latency-versus-load curve for S1–S3; thresholds proposed |
| S2 | Confirmatory tier S with frozen thresholds; S5 hook and status line under load | free | Table S and plots published in a report draft |
| R1 | Nemesis and checkers; storm, loop and fault traces; reference model for dedupe | free | R3–R8 tables with violation counts and upper bounds |
| R2 | Canary scanners; red-team corpus; real-model attack runs | about $15–40 | R1, R2 and V5 tables |
| V0 | Fixture generator, 64 tasks, graders, oracle and null-agent checks, human review | free | Every task: oracle 1, null 0, reviewed |
| V1 | Pilot on all arms with real model; judge validation; power calculation | about $30–90 | Per-run cost and variance measured; sample sizes and thresholds frozen |
| V2 | Confirmatory tier V and ablations | about $250–800 within the cap | Table V and plots |
| P | Publish data and scripts; native Linux rerun of tiers S and R; invite an outside rerun | free | Release asset with raw data; report links it |

**What a first credible result looks like.** After stages S2 and R1, before any money is spent:

- Table 1: S1–S3 and S5 at three offered rates. p50, p99, p99.9, max, each with a CI over daemon starts. Hardware
  and software listed above the table.
- Figure 1: p50, p99 and p99.9 against offered load, open loop, up to saturation. The uncorrected p99 as a dashed
  line.
- Figure 2: the full HdrHistogram percentile curve at one rate.
- Figure 3: the overhead breakdown of one message as a stacked bar per stage.
- Table 2: R3–R8. Trials, violations, and the 95% upper bound.

After V2:

- Table 3: per arm, success with CI, pass^5, human actions per resolved task by kind, unsolicited foreign tokens,
  primary-task success versus control, median and p90 time to a correct answer, cost per correct answer.
- Figure 4: success against cost per correct answer, one point per arm with CIs (Pareto view).
- Figure 5: primary-task success under each arm against the no-interruption control.
- Table 4: under attack: utility, attack success rate, and R2 violations, per arm.

## Open questions

1. Can a `claude -p` session with streamed input stand in for the human session? It must fire hooks (for presence)
   and accept B2 messages. Claude Code docs say `-p` sessions bind an inbox and can receive with
   `crossSessionInbound = accept`, but multi-turn streamed input under the driver has not been tried.
2. Is there dogfood traffic to build `mixed-day` from? Without it, the traffic mix (how much goes to command handlers)
   is a guess, and the average cost per message depends on it.
3. Can real runs use a clean config dir and still use the login? The smoke test uses the real HOME for the login.
4. Who outside the project reviews the baselines and tasks before the freeze?
5. Should the headline model be Sonnet 5.5 (the default profile) or should the default change first?
6. Is a native Linux machine available, or does the Linux rerun stay out of scope?
7. Does B2 need a fairness tweak, such as telling the recipient in CLAUDE.md to delegate mail to a subagent? That
   would make it B0s plus native delivery. Decide in the pilot, before the freeze.

## References

Agent and multi-agent benchmarks:

- Jimenez et al., "SWE-bench: Can Language Models Resolve Real-World GitHub Issues?" https://arxiv.org/abs/2310.06770
  (FAIL_TO_PASS and PASS_TO_PASS fields as described on the dataset card and in OpenAI's post; the paper text was
  not read for this RFC)
- OpenAI, "Introducing SWE-bench Verified" (500 tasks screened by 93 developers):
  https://openai.com/index/introducing-swe-bench-verified/
- Yao et al., "τ-bench" (pass^k): https://arxiv.org/abs/2406.12045
- Liu et al., "AgentBench: Evaluating LLMs as Agents": https://arxiv.org/abs/2308.03688
- Terminal-Bench 2.0 (task = instruction, image, tests, reference solution, time limit):
  https://arxiv.org/abs/2601.11868 · https://www.tbench.ai/
- Chan et al., "MLE-bench" (75 Kaggle competitions, human baselines from leaderboards): https://arxiv.org/abs/2410.07095
- Zhu et al., "MultiAgentBench" (MARBLE): https://arxiv.org/abs/2503.01935
- Cemri et al., "Why Do Multi-Agent LLM Systems Fail?" (MAST, 14 failure modes): https://arxiv.org/abs/2503.13657
- Zhu et al., "Establishing Best Practices for Building Rigorous Agentic Benchmarks" (ABC; task and outcome
  validity; trivial-agent baselines): https://arxiv.org/abs/2507.02825
- Kapoor et al., "AI Agents That Matter" (cost-aware evaluation, Pareto curves): https://arxiv.org/abs/2407.01502
- Debenedetti et al., "AgentDojo" (utility under attack, state-based checks): https://arxiv.org/abs/2406.13352

Statistics, judges and reporting:

- Miller, "Adding Error Bars to Evals: A Statistical Approach to Language Model Evaluations":
  https://arxiv.org/abs/2411.00640
- Zheng et al., "Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena": https://arxiv.org/abs/2306.05685
- NeurIPS paper checklist (error bars, compute disclosure): https://neurips.cc/public/guides/PaperChecklist
- Thinking Machines Lab, "Defeating Nondeterminism in LLM Inference" (2025):
  https://thinkingmachines.ai/blog/defeating-nondeterminism-in-llm-inference/ (only secondary coverage was read)
- The "rule of three" for zero-event upper bounds: Hanley and Lippman-Hand, JAMA 1983 (cited from memory, not
  re-checked)

Systems benchmarking:

- Tene, "How NOT to Measure Latency" (coordinated omission):
  https://videog.infoq.com/downloads/pdfdownloads/presentations/QConSF2015-GilTene-HowNOTtomeasureLatency.pdf
- wrk2 (constant-rate load, latency from intended send time): https://github.com/giltene/wrk2
- HdrHistogram: http://hdrhistogram.org/ · Go port https://github.com/HdrHistogram/hdrhistogram-go (not opened for
  this RFC)
- Schroeder, Wierman, Harchol-Balter, "Open Versus Closed: A Cautionary Tale", NSDI 2006:
  https://www.usenix.org/conference/nsdi-06/open-versus-closed-cautionary-tale (abstract page only; the summary
  above is from the paper's known thesis, not re-read)
- Dean and Barroso, "The Tail at Scale", CACM 2013: https://research.google/pubs/pub40801/
- OpenMessaging Benchmark (YAML workloads shared across systems; p50–p99.99 latency, publish delay):
  https://openmessaging.cloud/docs/benchmarks/ (warm-up length is not stated on that page)
- Heiser, "Systems Benchmarking Crimes": https://gernot-heiser.org/benchmarking-crimes.html
- van der Kouwe et al., benchmarking flaws in systems security: https://www.vusec.net/projects/benchmarking-crimes/
- Kalibera and Jones, "Rigorous Benchmarking in Reasonable Time", ISMM 2013: https://kar.kent.ac.uk/33611/
- Jepsen (history checkers, nemesis-driven fault injection): https://jepsen.io/ (described from secondary sources)
- SQLite `PRAGMA fullfsync`: https://www.sqlite.org/pragma.html#pragma_fullfsync

Claude Code:

- Agent teams (experimental; one team per session; `-p` does not spawn teammates):
  https://code.claude.com/docs/en/agent-teams
- Cross-session messaging (`SendMessage` into the live session; `-p` sessions can receive):
  https://code.claude.com/docs/en/cross-session-messaging
- Headless mode: https://code.claude.com/docs/en/headless
- This repo: `docs/rfc.md`, `docs/compat.md`, `docs/acceptance-report.md`, `testdata/fakeclaude/main.go`
