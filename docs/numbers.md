# Numbers

All numbers come from `make bench` on one laptop with a fake model. The benchmark costs $0. Each scenario
ran one time. The full report is in [bench/results](../bench/results/2026-11-02-3711a61/report.md), and the method
is in [rfc-benchmark.md](rfc-benchmark.md).

| Latency | p50 | p99 |
|---|---|---|
| Ask answered by a command, 20 per second | 25 ms | 30 ms |
| Message stored and routed, 200 per second | 10 ms | 23 ms |
| Hook in your session, at that load | 10 ms | 21 ms |
| Status line, at that load | 10 ms | 15 ms |
| smif time around one worker run | 28 ms | |

| Tokens | |
|---|---|
| Command handler | 0 |
| smif text in one worker run | about 570 |
| smif tool definitions in your session (one time) | about 500 |
| Same question, smif worker vs. plain `claude -p` in the same repo | **15 450 vs. 63 922** context tokens |

| Failure test | Result |
|---|---|
| `kill -9` on the daemon with 92 messages in flight | 0 lost. All done 13 s after the restart. |
| 500 copies of one crash event | 2 worker runs |
| Two workers that always ask each other | Stopped after 4 runs |
| A marker string in 20 inbound messages | Found 0 times in your session |
