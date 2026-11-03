package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// STATS: known traffic, every outcome once or more, and stats.get must
// count each message in exactly one outcome. Runs and tokens are checked
// against run.list (a separate read path).
func TestStatsCountsKnownTraffic(t *testing.T) {
	appRules := `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/smif/store-status.sh"]
[[agents.AGENT.rules]]
match = { kind = "event", intent = "notice.*" }
handler = "digest"
[[agents.AGENT.rules]]
match = { intent = "release.*" }
handler = "escalate"
[[agents.AGENT.rules]]
match = {}
handler = "worker"
profile = "triage"
`
	e := NewEnv(t, "", "",
		AgentSpec{ID: "obs", Rules: digestRule()},
		AgentSpec{ID: "stranger", Rules: digestRule()},
		AgentSpec{ID: "app", Rules: appRules, Extra: `accept_from = ["obs", "human"]`, Before: withScript("store-status.sh", storeStatusScript)},
		AgentSpec{ID: "web", Extra: "max_attempts = 1"})
	e.Start()
	send := func(to, kind, intent, body, dedupe string) api.SendResult {
		method := map[string]string{"ask": "message.ask", "tell": "message.send", "event": "message.emit"}[kind]
		return e.Send("obs", method, envelope.Draft{To: to, Kind: kind, Intent: intent, Subject: intent, Body: body, DedupeKey: dedupe})
	}

	// Without you: command, digest, worker (with known tokens).
	ask := send("app", "ask", "store.status", "", "")
	e.WaitState(ask.ID, 15*time.Second, "done")
	notice := send("app", "event", "notice.build", "", "notice:1")
	e.WaitState(notice.ID, 15*time.Second, "done")
	crash := send("app", "tell", "crash.report", "#fake:tokens=1234 #fake:cost=0.01", "")
	e.WaitState(crash.ID, 15*time.Second, "done")

	// Needed you: one answered, one still waiting, one escalated by rule.
	x := send("app", "tell", "x.decide", "#fake:result=needs_human", "")
	e.WaitState(x.ID, 15*time.Second, "needs_human")
	var rr api.SendResult
	e.Call("message.reply", api.HumanReplyParams{MessageID: x.ID, Text: "yes"}, &rr)
	e.WaitState(x.ID, 15*time.Second, "done")
	if rr.ID != "" {
		e.WaitState(rr.ID, 15*time.Second, "done")
	}
	y := send("app", "tell", "y.decide", "#fake:result=needs_human", "")
	e.WaitState(y.ID, 15*time.Second, "needs_human")
	rel := send("app", "tell", "release.ship", "", "")
	e.WaitState(rel.ID, 15*time.Second, "needs_human")

	// Cancelled by you: a queued one behind a running one, then the running one.
	long := send("app", "tell", "job.long", "#fake:sleep=30", "")
	e.WaitState(long.ID, 15*time.Second, "running")
	queued := send("app", "tell", "job.next", "", "")
	e.WaitState(queued.ID, 5*time.Second, "queued")
	var cr api.CancelResult
	e.Call("message.cancel", api.IDParams{ID: queued.ID}, &cr)
	e.Call("message.cancel", api.IDParams{ID: long.ID}, &cr)
	e.WaitState(long.ID, 15*time.Second, "rejected")

	// Dead letter, a storm of 5 repeats (1 message + 4 merged), a guard.
	dead := send("web", "tell", "crash.boom", "#fake:exit=3", "")
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	var storm api.SendResult
	for i := 0; i < 5; i++ {
		r := send("web", "event", "crash.storm", "#fake:sleep=1", "storm:1")
		if i == 0 {
			storm = r
		}
	}
	e.WaitState(storm.ID, 15*time.Second, "done")
	c := e.AgentClient("stranger")
	var sr api.SendResult
	err := c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "app", Kind: "tell", Intent: "x.y", Subject: "hi"}}, &sr, 10*time.Second)
	c.Close()
	t.Logf("stranger → app: %+v err=%v", sr, err)

	var st api.StatsResult
	e.Call("stats.get", api.StatsParams{Since: time.Now().Add(-time.Hour)}, &st)
	tot := st.Total
	t.Logf("total: %+v replies=%d", tot, st.Replies)
	guard := 0
	if sr.ID != "" {
		guard = 1 // the allowlist stored and rejected it
	}
	want := map[string][2]int{
		"inbound":          {tot.Inbound, 10 + guard},
		"auto":             {tot.Auto, 4},
		"auto command":     {tot.AutoBy["command"], 1},
		"auto digest":      {tot.AutoBy["digest"], 1},
		"auto worker":      {tot.AutoBy["worker"], 2},
		"needed you":       {tot.NeededYou, 3},
		"answered":         {tot.Answered, 1},
		"waiting":          {tot.Waiting, 2},
		"cancelled":        {tot.Cancelled, 2},
		"dead":             {tot.Dead, 1},
		"rejected (guard)": {tot.Rejected, guard},
		"in progress":      {tot.InProgress, 0},
		"merged":           {tot.Merged, 4},
	}
	for k, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s = %d, want %d", k, v[0], v[1])
		}
	}
	if sum := tot.Auto + tot.NeededYou + tot.Cancelled + tot.Dead + tot.Rejected + tot.Expired + tot.InProgress; sum != tot.Inbound {
		t.Errorf("outcomes add up to %d, inbound is %d", sum, tot.Inbound)
	}
	if st.Replies < 2 {
		t.Errorf("replies = %d, want at least 2 (the command answer and the human's answer)", st.Replies)
	}

	// Runs and tokens against run.list.
	var runs []api.RunView
	e.Call("run.list", api.ListParams{Limit: 1000}, &runs)
	var worker, command, failed int
	var in, out int64
	var cost float64
	var crashTokens int64
	for _, r := range runs {
		switch r.Handler {
		case "worker":
			worker++
		case "command":
			command++
		}
		if r.State == "failed" {
			failed++
		}
		in, out, cost = in+r.InputTokens, out+r.OutputTokens, cost+r.CostUSD
		if r.MessageID == crash.ID {
			crashTokens = r.InputTokens
		}
	}
	if tot.Runs != len(runs) || tot.WorkerRuns != worker || tot.CommandRuns != command || tot.FailedRuns != failed {
		t.Errorf("runs %d/%d/%d/%d, run.list says %d/%d/%d/%d (all/worker/command/failed)",
			tot.Runs, tot.WorkerRuns, tot.CommandRuns, tot.FailedRuns, len(runs), worker, command, failed)
	}
	if tot.InputTokens != in || tot.OutputTokens != out || int(tot.CostUSD*1e6) != int(cost*1e6) {
		t.Errorf("tokens %d/%d $%.6f, run.list says %d/%d $%.6f", tot.InputTokens, tot.OutputTokens, tot.CostUSD, in, out, cost)
	}
	if crashTokens != 1234 {
		t.Errorf("the crash run reported %d input tokens, the fake sent 1234", crashTokens)
	}

	// Per agent and per day add up to the total.
	var agentIn, dayIn, agentRuns int
	for _, a := range st.Agents {
		agentIn += a.Inbound
		agentRuns += a.Runs
	}
	for _, d := range st.Days {
		dayIn += d.Inbound
	}
	if agentIn != tot.Inbound || dayIn != tot.Inbound || agentRuns != tot.Runs {
		t.Errorf("per-agent inbound %d, per-day %d, per-agent runs %d; total %d inbound, %d runs", agentIn, dayIn, agentRuns, tot.Inbound, tot.Runs)
	}

	// The CLI prints the same numbers; agents cannot read stats (D4).
	outText, err := e.CLI("", "stats", "--days", "1")
	if err != nil || !strings.Contains(outText, "without you   4 ") || !strings.Contains(outText, "needed you    3  answered 1 · waiting 2") {
		t.Errorf("smif stats: %v\n%s", err, outText)
	}
	t.Logf("smif stats:\n%s", outText)
	ac := e.AgentClient("obs")
	defer ac.Close()
	if err := ac.Call("stats.get", api.StatsParams{}, &st, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent read stats.get: %v", err)
	}
}
