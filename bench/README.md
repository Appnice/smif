# bench

Mailroom's benchmark, tiers S (system overhead), R (robustness and safety) and T (tokens) of
`docs/rfc-benchmark.md`. It costs nothing: each scenario starts a fresh daemon in `/tmp` with the fake claude.

```sh
make bench                    # full run, about 1.5 minutes
go run ./bench -quick         # smaller counts, about 20 s
go run ./bench -only S1,R3    # some scenarios
make bench-real               # also T-real: actual tokens on haiku through the existing claude login (no API key)
```

Output: `bench/results/<date>-<commit>/report.md` (tables) and `results.json` (raw numbers).

| id | what |
|---|---|
| S1 | ask answered by a command handler, open loop 20/s, latency from the scheduled send time |
| S2 | tells stored and routed, open loop 200/s |
| S5 | hook and status line process time, idle and under the S2 load; bytes the hook prints |
| S3 | phases of one worker message; Mailroom's cost over the same fake process run directly |
| S4 | 500 identical crash events (dedupe): messages, merges, worker runs |
| R1 | kill -9 with 100 messages in flight: lost, not done, runs over max_attempts |
| R2 | two workers that always ask back: when the loop guard freezes the thread, runs spent |
| R3 | canary string in 20 inbound messages, searched in every channel into the human's session |
| T1 | bytes and estimated tokens Mailroom adds to each worker run (policy, envelope, schema) |
| T2 | what Mailroom costs the human's session (MCP tool definitions, hooks, status line) |
| T-real | the same question via a Mailroom worker and a plain `claude -p`: actual tokens (`-real`) |

Latencies include process start where the real path has one (hooks, status line). Token counts marked ≈ are
bytes/4 estimates; only T-real counts tokens.
