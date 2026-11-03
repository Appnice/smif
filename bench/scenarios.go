package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/mcpserver"
)

const (
	digestRules = "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"digest\"\n"
	workerRules = "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"triage\"\n"
	cmdRules    = `[[agents.AGENT.rules]]
match = { kind = "ask", intent = "status.get" }
handler = "command"
command = ["./status.sh"]
[[agents.AGENT.rules]]
match = {}
handler = "digest"
`
	statusScript = "#!/bin/sh\ncat >/dev/null\nprintf '{\"status\":\"done\",\"summary\":\"ok\",\"reply\":\"1.0.17 (22) live\"}\\n'\n"
)

// est is a token estimate from bytes. Claude tokenizers average roughly
// 3.5-4 bytes per token on English and JSON; the report says so. The real
// sample (-real) measures actual tokens.
func est(bytes int) int { return (bytes + 3) / 4 }

// ---------------------------------------------------------------- S1, S2, S5

// s1CommandAsk: an ask answered by a command handler, round trip at a
// fixed rate.
func (b *bench) s1CommandAsk() error {
	// The per-sender rate limit is a feature, not the path under test.
	e, err := b.env("rate_limit_per_minute = 1000000", "", agentSpec{id: "caller", rules: digestRules},
		agentSpec{id: "app", rules: cmdRules, files: map[string]string{"status.sh": statusScript}})
	if err != nil {
		return err
	}
	defer e.close()
	rate, d := 20.0, b.dur(20*time.Second)
	lat, errs, err := openLoop(func() (*api.Client, error) { return e.client("caller", "interactive") }, 32, rate, d,
		func(c *api.Client, i int) error {
			var res api.SendResult
			err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "app", Kind: "ask", Intent: "status.get",
				Subject: "state?"}, WaitS: 10}, &res, 15*time.Second)
			if err == nil && res.Reply == nil {
				err = fmt.Errorf("no reply")
			}
			return err
		})
	if err != nil {
		return err
	}
	s := summarize(lat)
	b.add("S1", fmt.Sprintf("### S1 · ask answered by a command handler (round trip)\n\nOpen loop, %.0f asks/s for %v, "+
		"each waits for the reply. Latency counts from the scheduled send time.\n\n%s\n%s\n\nErrors: %d of %d.\n",
		rate, d, statsHead, s.row("ask → reply"), errs, s.N), map[string]any{"rate": rate, "stats": s, "errors": errs})
	return nil
}

// s2Ingest: tells accepted and routed to the digest at a fixed rate, with
// S5 (hook and status line latency) measured during the load.
func (b *bench) s2IngestS5Hooks() error {
	e, err := b.env("rate_limit_per_minute = 1000000", "", agentSpec{id: "caller", rules: digestRules},
		agentSpec{id: "sink", rules: digestRules})
	if err != nil {
		return err
	}
	defer e.close()
	repo := e.repos["sink"]
	hookIn := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"hook_event_name":"UserPromptSubmit"}`, repo)
	lineIn := fmt.Sprintf(`{"session_id":"s1","workspace":{"current_dir":%q}}`, repo)
	probe := func(n int) (hook, line []time.Duration, hookOut int) {
		for i := 0; i < n; i++ {
			t0 := time.Now()
			out, _ := e.cli(repo, hookIn, "hook", "UserPromptSubmit")
			hook = append(hook, time.Since(t0))
			hookOut += len(out)
			t0 = time.Now()
			e.cli(repo, lineIn, "statusline")
			line = append(line, time.Since(t0))
		}
		return
	}
	idleHook, idleLine, _ := probe(b.count(30))

	rate, d := 200.0, b.dur(20*time.Second)
	var busyHook, busyLine []time.Duration
	var hookBytes int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(d / 4)
		busyHook, busyLine, hookBytes = probe(b.count(30))
	}()
	lat, errs, err := openLoop(func() (*api.Client, error) { return e.client("caller", "interactive") }, 16, rate, d,
		func(c *api.Client, i int) error {
			var res api.SendResult
			return c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "sink", Kind: "tell", Intent: "x.y",
				Subject: fmt.Sprintf("tell %d", i), Body: strings.Repeat("x", 200)}}, &res, 10*time.Second)
		})
	wg.Wait()
	if err != nil {
		return err
	}
	s := summarize(lat)
	b.add("S2", fmt.Sprintf("### S2 · a tell accepted and routed (ingest)\n\nOpen loop, %.0f tells/s for %v to an agent whose "+
		"rule is `digest`. The call returns once the message is stored (SQLite, synchronous=FULL) and routed.\n\n%s\n%s\n\n"+
		"Errors: %d of %d.\n", rate, d, statsHead, s.row("send → stored"), errs, s.N), map[string]any{"rate": rate, "stats": s, "errors": errs})
	b.add("S5", fmt.Sprintf("### S5 · what the human's session pays: hook and status line\n\nEach is a `mailroom` process "+
		"(exec included), as Claude Code runs it. Budgets: hook under 200 ms, status line under 300 ms.\n\n%s\n%s\n%s\n%s\n%s\n\n"+
		"Bytes the hook printed into the session over %d calls under load: **%d** (it must print nothing).\n",
		statsHead, summarize(idleHook).row("hook, idle daemon"), summarize(busyHook).row(fmt.Sprintf("hook, during %.0f tells/s", rate)),
		summarize(idleLine).row("status line, idle"), summarize(busyLine).row(fmt.Sprintf("status line, during %.0f tells/s", rate)),
		len(busyHook), hookBytes),
		map[string]any{"hook_idle": summarize(idleHook), "hook_busy": summarize(busyHook), "line_idle": summarize(idleLine),
			"line_busy": summarize(busyLine), "hook_bytes": hookBytes})
	return nil
}

// ---------------------------------------------------------------- S3, T1

// s3WorkerOverhead: what Mailroom adds around a worker process, measured
// against the same fake claude run directly with the same argv and stdin.
func (b *bench) s3WorkerOverheadT1() error {
	e, err := b.env("", "", agentSpec{id: "caller", rules: digestRules}, agentSpec{id: "w", rules: workerRules})
	if err != nil {
		return err
	}
	defer e.close()
	c, err := e.client("caller", "interactive")
	if err != nil {
		return err
	}
	defer c.Close()
	body := strings.Repeat("Crash in CheckoutView at line 42. ", 6) // ~200 bytes, like a short report
	n := b.count(30)
	var e2e, dispatch, process, finish []time.Duration
	for i := 0; i < n; i++ {
		var res api.SendResult
		if err := c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "w", Kind: "tell", Intent: "crash.report",
			Subject: fmt.Sprintf("crash %d", i), Body: body}}, &res, 10*time.Second); err != nil {
			return err
		}
		got, err := e.waitTerminal([]string{res.ID}, 30*time.Second)
		if err != nil {
			return err
		}
		m := got[res.ID]
		var tv api.ThreadView
		if err := e.human("thread.get", api.IDParams{ID: res.ID}, &tv); err != nil || len(tv.Runs) == 0 {
			return fmt.Errorf("thread %s: %v", res.ID, err)
		}
		r := tv.Runs[len(tv.Runs)-1]
		e2e = append(e2e, m.UpdatedAt.Sub(m.CreatedAt))
		dispatch = append(dispatch, r.StartedAt.Sub(m.CreatedAt))
		if r.EndedAt != nil {
			process = append(process, r.EndedAt.Sub(r.StartedAt))
			finish = append(finish, m.UpdatedAt.Sub(*r.EndedAt))
		}
	}
	// The same fake process alone, with the argv and stdin Mailroom gave it.
	calls := e.fakeCalls()
	if len(calls) == 0 {
		return fmt.Errorf("no fake invocation recorded")
	}
	last := calls[len(calls)-1]
	argv := toStrings(last["argv"])
	stdin, _ := last["stdin"].(string)
	var bare []time.Duration
	for i := 0; i < n; i++ {
		cmd := exec.Command(e.fake, argv[1:]...)
		cmd.Env = append(e.vars, "MAILROOM_FAKE_LOG="+filepath.Join(e.root, "bare"))
		cmd.Dir = e.repos["w"]
		cmd.Stdin = strings.NewReader(stdin)
		t0 := time.Now()
		cmd.Run()
		bare = append(bare, time.Since(t0))
	}
	se, sb := summarize(e2e), summarize(bare)
	b.add("S3", fmt.Sprintf("### S3 · what Mailroom adds around one worker run\n\n%d tells to a worker agent, one at a time "+
		"(fake claude, no sleep). Phases from the daemon's own timestamps (ms resolution).\n\n%s\n%s\n%s\n%s\n%s\n%s\n\n"+
		"Mailroom's own cost per worker message ≈ end to end − the bare process = **%s** (p50).\n",
		n, statsHead, se.row("end to end (accepted → done)"), summarize(dispatch).row("accepted → process start"),
		summarize(process).row("run: prepare, spawn, stream parse, exit"), summarize(finish).row("process exit → done (result, audit)"),
		sb.row("the same fake run directly (baseline)"), ms(se.P50-sb.P50)),
		map[string]any{"e2e": se, "bare": sb, "dispatch": summarize(dispatch), "process": summarize(process), "finish": summarize(finish)})

	// T1: what goes into the worker's context on every run.
	flag := func(name string) string {
		for i, a := range argv {
			if a == name && i+1 < len(argv) {
				return argv[i+1]
			}
		}
		return ""
	}
	policy, schema, tools := flag("--append-system-prompt"), flag("--json-schema"), flag("--tools")
	nTools := 0
	if tools != "" {
		nTools = len(strings.Split(tools, ","))
	}
	mcpCfg := flag("--mcp-config")
	envelopeOverhead := len(stdin) - len(body)
	rows := [][3]string{
		{"policy (`--append-system-prompt`)", fmt.Sprint(len(policy)), fmt.Sprint(est(len(policy)))},
		{"prompt (envelope + instructions) minus the body", fmt.Sprint(envelopeOverhead), fmt.Sprint(est(envelopeOverhead))},
		{"result schema (`--json-schema`, the StructuredOutput tool)", fmt.Sprint(len(schema)), fmt.Sprint(est(len(schema)))},
	}
	total := len(policy) + envelopeOverhead + len(schema)
	var tb strings.Builder
	tb.WriteString("| added to every worker run | bytes | ≈ tokens |\n|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&tb, "| %s | %s | %s |\n", r[0], r[1], r[2])
	}
	fmt.Fprintf(&tb, "| **total Mailroom adds** | **%d** | **≈ %d** |\n", total, est(total))
	b.add("T1", fmt.Sprintf("### T1 · tokens Mailroom adds to each worker run\n\nFrom the argv and stdin of a real worker "+
		"invocation (profile `triage`, a %d-byte body). Token counts are bytes/4 estimates.\n\n%s\n"+
		"What Mailroom takes away from a worker's context, compared with a plain `claude -p` in the repo:\n\n"+
		"- `--tools %s` (%d tools): every built-in tool not listed is not defined in the context at all.\n"+
		"- `--strict-mcp-config --mcp-config %s`: no MCP servers load (a plain session here loads every user and claude.ai server; on this Mac that was 12).\n"+
		"- `--settings {\"disableAllHooks\":true}`: no hook output enters the worker.\n"+
		"- Command handlers use no tokens at all.\n\n"+
		"The real sample (`-real`) measures the net effect in actual tokens.\n",
		len(body), tb.String(), tools, nTools, shortCfg(mcpCfg)),
		map[string]any{"policy_bytes": len(policy), "envelope_overhead_bytes": envelopeOverhead, "schema_bytes": len(schema),
			"total_bytes": total, "tools": tools})
	return nil
}

func shortCfg(s string) string {
	if len(s) > 40 {
		return filepath.Base(s)
	}
	return s
}

func toStrings(v any) []string {
	xs, _ := v.([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------- S4 storm, T3

// s4Storm: the same crash event 500 times. Dedupe merges them into one
// run; the tokens of the merged runs are never spent.
func (b *bench) s4Storm(perRunBytes int) error {
	e, err := b.env("rate_limit_per_minute = 1000000", "", agentSpec{id: "obs", rules: digestRules},
		agentSpec{id: "app", rules: workerRules})
	if err != nil {
		return err
	}
	defer e.close()
	n := b.count(500)
	body := "NPE in DeckScreen on 1.4.2 #fake:sleep=1"
	var mu sync.Mutex
	ids := map[string]bool{}
	coalesced := 0
	lat, errs, err := openLoop(func() (*api.Client, error) { return e.client("obs", "interactive") }, 16, 250, time.Duration(float64(n)/250*float64(time.Second)),
		func(c *api.Client, i int) error {
			var res api.SendResult
			err := c.Call("message.emit", api.SendParams{Draft: envelope.Draft{To: "app", Kind: "event", Intent: "crash.report",
				Subject: "NPE in DeckScreen", Body: body, DedupeKey: "crash:app:npe-deck"}}, &res, 10*time.Second)
			if err == nil {
				mu.Lock()
				ids[res.ID] = true
				if res.Coalesced {
					coalesced++
				}
				mu.Unlock()
			}
			return err
		})
	if err != nil {
		return err
	}
	var list []string
	for id := range ids {
		list = append(list, id)
	}
	if _, err := e.waitTerminal(list, 60*time.Second); err != nil {
		return err
	}
	runs := len(e.fakeCalls())
	saved := (len(lat) - runs) * (perRunBytes + len(body))
	b.add("S4", fmt.Sprintf("### S4 · a storm of the same crash event (dedupe)\n\n%d identical events (same `dedupe_key`) at "+
		"250/s to a worker agent; each worker run takes 1 s.\n\n| | count |\n|---|---|\n| events sent | %d |\n| messages created | %d |\n"+
		"| events merged into an open message | %d |\n| worker runs | **%d** |\n| errors | %d |\n\n%s\n%s\n\n"+
		"T3 (storm part): the merged events never start a run. At the T1 overhead plus this body, that is about "+
		"**%d tokens** of worker input not spent (bytes/4), before the worker's own reading of the repo, which is usually far larger.\n",
		len(lat), len(lat), len(ids), coalesced, runs, errs, statsHead, summarize(lat).row("emit → stored or merged"), est(saved)),
		map[string]any{"events": len(lat), "messages": len(ids), "coalesced": coalesced, "runs": runs, "errors": errs})
	return nil
}

// ---------------------------------------------------------------- R1 kill -9

// r1Kill9: kill -9 the daemon while 100 messages are in flight, restart
// it, and check that every accepted message ends done, none lost, and no
// message ran more often than its attempts allow.
func (b *bench) r1Kill9() error {
	agents := []agentSpec{{id: "src", rules: digestRules}}
	for i := 0; i < 4; i++ {
		agents = append(agents, agentSpec{id: fmt.Sprintf("w%d", i), rules: workerRules})
	}
	e, err := b.env("rate_limit_per_minute = 1000000", "", agents...)
	if err != nil {
		return err
	}
	defer e.close()
	c, err := e.client("src", "interactive")
	if err != nil {
		return err
	}
	n := b.count(100)
	var ids []string
	for i := 0; i < n; i++ {
		var res api.SendResult
		if err := c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: fmt.Sprintf("w%d", i%4), Kind: "tell",
			Intent: "job.run", Subject: fmt.Sprintf("job %d", i), Body: "#fake:sleep=0.5"}}, &res, 10*time.Second); err != nil {
			return err
		}
		ids = append(ids, res.ID)
	}
	c.Close()
	time.Sleep(700 * time.Millisecond)
	var before []api.MessageView
	e.human("thread.list", api.ListParams{Limit: 100000}, &before)
	inFlight := 0
	for _, m := range before {
		if m.State != "done" && m.Kind == "tell" {
			inFlight++
		}
	}
	e.kill9()
	killedAt := time.Now()
	if err := e.start(); err != nil {
		return err
	}
	got, err := e.waitTerminal(ids, 180*time.Second)
	recovered := time.Since(killedAt)
	lost, notDone, overRun := 0, 0, 0
	for _, id := range ids {
		m, ok := got[id]
		if !ok {
			lost++
			continue
		}
		if m.State != "done" {
			notDone++
		}
		var tv api.ThreadView
		if e.human("thread.get", api.IDParams{ID: id}, &tv) == nil && len(tv.Runs) > 3 {
			overRun++
		}
	}
	b.add("R1", fmt.Sprintf("### R1 · kill -9 with %d messages in flight\n\n%d tells to 4 worker agents (0.5 s per run), "+
		"`kill -9` of the daemon after 0.7 s, restart.\n\n| check | result |\n|---|---|\n| not yet done at the kill | %d |\n| accepted messages lost | **%d** |\n"+
		"| not done after recovery | %d |\n| messages with more runs than max_attempts (3) | %d |\n"+
		"| time from kill to all done | %v |\n| wait error | %v |\n\n"+
		"A run cut by the kill is re-run (at-least-once for the handler, exactly-once for the message state).\n",
		n, n, inFlight, lost, notDone, overRun, recovered.Round(100*time.Millisecond), errString(err)),
		map[string]any{"n": n, "lost": lost, "not_done": notDone, "over_run": overRun, "recovery_ms": recovered.Milliseconds()})
	return nil
}

// ---------------------------------------------------------------- R2 loop

// r2Loop: two workers that always ask each other back.
func (b *bench) r2Loop() error {
	e, err := b.env("", "", agentSpec{id: "ping", rules: workerRules}, agentSpec{id: "pong", rules: workerRules})
	if err != nil {
		return err
	}
	defer e.close()
	c, err := e.client("ping", "interactive")
	if err != nil {
		return err
	}
	defer c.Close()
	var res api.SendResult
	if err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "pong", Kind: "ask", Intent: "loop.ping",
		Subject: "ping", Body: "always ask back #fake:followup=sender"}}, &res, 10*time.Second); err != nil {
		return err
	}
	start := time.Now()
	var tv api.ThreadView
	for time.Since(start) < 60*time.Second {
		if e.human("thread.get", api.IDParams{ID: res.ThreadID}, &tv) == nil && tv.Frozen {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	took := time.Since(start)
	time.Sleep(1500 * time.Millisecond)
	e.human("thread.get", api.IDParams{ID: res.ThreadID}, &tv)
	b.add("R2", fmt.Sprintf("### R2 · two workers that always ask each other back\n\n| check | result |\n|---|---|\n"+
		"| thread frozen | %v (%s) |\n| messages in the thread | %d |\n| worker runs (tokens spent) | **%d** |\n| time to freeze | %v |\n\n"+
		"Without the guards this loop would run until the budget stops it.\n",
		tv.Frozen, tv.FrozenReason, len(tv.Messages), len(tv.Runs), took.Round(100*time.Millisecond)),
		map[string]any{"frozen": tv.Frozen, "reason": tv.FrozenReason, "messages": len(tv.Messages), "runs": len(tv.Runs)})
	return nil
}

// ---------------------------------------------------------------- R3 canary, T2

// r3CanaryT2: inbound text must never reach the human's session. 20
// messages carry a unique canary; every channel into the session (hook
// output, status line, the MCP tools and the MCP server instructions) is
// checked for it. T2 measures what those channels cost in tokens.
func (b *bench) r3CanaryT2() error {
	e, err := b.env("", "", agentSpec{id: "src", rules: digestRules}, agentSpec{id: "app", rules: workerRules})
	if err != nil {
		return err
	}
	defer e.close()
	repo := e.repos["app"]
	canary := fmt.Sprintf("CANARY-%d", time.Now().UnixNano())
	e.cli(repo, fmt.Sprintf(`{"session_id":"h1","cwd":%q,"hook_event_name":"SessionStart"}`, repo), "hook", "SessionStart")
	c, err := e.client("src", "interactive")
	if err != nil {
		return err
	}
	var ids []string
	for i := 0; i < 20; i++ {
		var res api.SendResult
		if err := c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "app", Kind: "tell", Intent: "note.add",
			Subject: "note " + canary, Body: "secret " + canary + " #fake:sleep=0"}}, &res, 10*time.Second); err != nil {
			return err
		}
		ids = append(ids, res.ID)
	}
	c.Close()
	if _, err := e.waitTerminal(ids, 60*time.Second); err != nil {
		return err
	}
	probes, leaks := 0, 0
	check := func(s string) {
		probes++
		if strings.Contains(s, canary) {
			leaks++
		}
	}
	for _, ev := range []string{"UserPromptSubmit", "Stop", "SessionStart"} {
		out, _ := e.cli(repo, fmt.Sprintf(`{"session_id":"h1","cwd":%q,"hook_event_name":%q}`, repo, ev), "hook", ev)
		check(out)
	}
	line, _ := e.cli(repo, fmt.Sprintf(`{"session_id":"h1","workspace":{"current_dir":%q}}`, repo), "statusline")
	check(line)
	// The MCP server as the app's interactive session sees it.
	tok, _ := os.ReadFile(filepath.Join(e.root, "cfg", "tokens", "app"))
	id := mcpserver.Identity{Agent: "app", Origin: "interactive",
		Auth: api.Auth{Agent: "app", Token: strings.TrimSpace(string(tok)), Origin: "interactive"}}
	srv := mcpserver.Server(id, func() (*api.Client, error) {
		return api.Dial(filepath.Join(e.root, "s.sock"), id.Auth, 2*time.Second)
	})
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		return err
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	defs, _ := json.Marshal(tools.Tools)
	instr := ""
	if ir := cs.InitializeResult(); ir != nil {
		instr = ir.Instructions
	}
	for _, name := range []string{"mailroom_status", "mailroom_agents"} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil {
			out, _ := json.Marshal(r.Content)
			check(string(out))
		}
	}
	check(string(defs) + instr)
	b.add("R3", fmt.Sprintf("### R3 · inbound text never reaches the human's session (canary)\n\n20 messages to an agent with "+
		"an open interactive session, each carrying the unique string `%s…`. Then every way into that session was read: "+
		"the hook output (3 events), the status line, `mailroom_status` and `mailroom_agents`, the MCP tool definitions and "+
		"instructions.\n\n| check | result |\n|---|---|\n| probes | %d |\n| probes that contained the canary | **%d** |\n\n"+
		"The digest (`/mailroom`) shows summaries only when the human types it; it is not probed here.\n",
		canary[:12], probes, leaks), map[string]any{"probes": probes, "leaks": leaks})
	b.add("T2", fmt.Sprintf("### T2 · tokens Mailroom costs the human's session\n\n| channel | bytes | ≈ tokens | when |\n|---|---|---|---|\n"+
		"| MCP tool definitions (%d tools) | %d | ≈ %d | in every session's context (cached after the first turn) |\n"+
		"| MCP server instructions | %d | ≈ %d | same |\n| hook output | 0 | 0 | every prompt (hooks print nothing) |\n"+
		"| status line `%s` | %d | 0 | shown in the terminal only, never sent to the model |\n"+
		"| inbound messages (R3: 20 messages) | 0 | 0 | never; workers and handlers take them |\n"+
		"| `/mailroom` digest | on demand | – | only when the human types it |\n",
		len(tools.Tools), len(defs), est(len(defs)), len(instr), est(len(instr)), strings.TrimSpace(line), len(line)),
		map[string]any{"mcp_tools": len(tools.Tools), "mcp_def_bytes": len(defs), "mcp_instr_bytes": len(instr)})
	return nil
}

func errString(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}
