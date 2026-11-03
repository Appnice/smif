package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

const storeStatusScript = `#!/bin/sh
env=$(cat)
case "$env" in *store.status*) ;; *) echo "wrong envelope" >&2; exit 2;; esac
printf '{"status":"done","summary":"1.0.1 (4) live on Play","reply":"1.0.1 (4) is live","reply_data":{"version":"1.0.1","build":4,"live":true}}\n'
`

func withScript(name, body string) func(string) {
	return func(repo string) {
		p := filepath.Join(repo, "scripts", "mailroom", name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o755)
	}
}

// A1: release sync answered by a command handler, no claude process.
func TestA1ReleaseSyncCommandHandler(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "atlas", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"digest\"\n"},
		AgentSpec{ID: "cafe", Before: withScript("store-status.sh", storeStatusScript), Rules: `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/mailroom/store-status.sh"]
[[agents.AGENT.rules]]
match = {}
handler = "escalate"
`})
	e.Start()
	start := time.Now()
	c := e.AgentClient("atlas")
	defer c.Close()
	var res api.SendResult
	err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "cafe", Kind: "ask", Intent: "store.status",
		Subject: "Is the current build live, which version?"}, WaitS: 5}, &res, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == nil {
		t.Fatalf("no reply within 5 s: %+v", res)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("reply took %s", took)
	}
	if res.Reply.Text != "1.0.1 (4) is live" || !strings.Contains(string(res.Reply.Data), `"build":4`) {
		t.Fatalf("reply = %+v", res.Reply)
	}
	if n := len(e.WorkerCalls()); n != 0 {
		t.Fatalf("%d claude processes started, want 0", n)
	}
	var counts api.Counts
	e.Call("inbox.counts", api.CountsParams{Agent: "cafe"}, &counts)
	if counts.Handled != 0 {
		t.Fatalf("a routine command answer badged cafe: %+v", counts)
	}
	t.Logf("A1 reply in %s: %s", time.Since(start).Round(time.Millisecond), res.Reply.Text)
}

// A9: a client-supplied from is rejected; an expired run token is rejected.
// Both are audited.
func TestA9ForgedSenderAndExpiredRunToken(t *testing.T) {
	e := NewEnv(t, "", `
[profiles.onward]
onward_mcp = true
max_turns = 3
`,
		AgentSpec{ID: "alpha", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"onward\"\n"},
		AgentSpec{ID: "beta"})
	e.Start()
	c := e.AgentClient("alpha")
	defer c.Close()
	raw := json.RawMessage(`{"to":"beta","kind":"tell","intent":"x.y","subject":"forged","from":{"agent":"beta","origin":"worker"}}`)
	err := c.Call("message.send", raw, nil, 5*time.Second)
	if !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), `"from"`) {
		t.Fatalf("forged from: got %v", err)
	}
	// Run a worker that has a run token, then reuse the token after the run.
	r := e.Send("beta", "message.send", envelope.Draft{To: "alpha", Kind: "tell", Intent: "x.y", Subject: "hello"})
	e.WaitState(r.ID, 15*time.Second, "done")
	var tok string
	for _, inv := range e.WorkerCalls() {
		if inv.Env["MAILROOM_AGENT"] == "alpha" && inv.Env["MAILROOM_RUN_TOKEN"] != "" {
			tok = inv.Env["MAILROOM_RUN_TOKEN"]
		}
	}
	if tok == "" {
		t.Fatal("worker with onward_mcp got no run token")
	}
	rc, err := api.Dial(filepath.Join(e.Root, "s.sock"), api.Auth{Agent: "alpha", Token: tok}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	err = rc.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "beta", Kind: "tell", Intent: "x.y", Subject: "late"}}, nil, 5*time.Second)
	if !api.IsCode(err, api.CodeAuth) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired run token: got %v", err)
	}
	var forged, expired bool
	for _, a := range e.Audit() {
		ev, _ := a["event"].(string)
		det, _ := a["detail"].(map[string]any)
		if ev == "message.rejected_ingress" && strings.Contains(det["reason"].(string), `"from"`) {
			forged = true
		}
		if ev == "auth.failed" && strings.Contains(det["error"].(string), "expired") {
			expired = true
		}
	}
	if !forged || !expired {
		t.Fatalf("audit rows missing: forged=%v expired=%v", forged, expired)
	}
}

// A6: 40 identical crash events produce one run with coalesced_count = 39.
func TestA6StormCoalesced(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"digest\"\n"},
		AgentSpec{ID: "puzzle", Rules: `
[[agents.AGENT.rules]]
match = { from = "observability", kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "6h"
[[agents.AGENT.rules]]
match = {}
handler = "escalate"
`})
	e.Start()
	var first string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report",
				Priority: "high", Subject: "NPE in DeckScreen on 1.4.2", Body: "#fake:sleep=1", DedupeKey: "crash:puzzle:a91f"})
			mu.Lock()
			if !r.Coalesced {
				if first != "" {
					t.Errorf("second non-coalesced message %s", r.ID)
				}
				first = r.ID
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if first == "" {
		t.Fatal("no message created")
	}
	m := e.WaitState(first, 20*time.Second, "done")
	if m.Coalesced != 39 {
		t.Fatalf("coalesced_count = %d, want 39", m.Coalesced)
	}
	if runs := e.Runs(first); len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if n := len(e.WorkerCalls()); n != 1 {
		t.Fatalf("worker calls = %d, want 1", n)
	}
}

// A7: two agents whose rules always ask each other: frozen by the guard,
// escalated, at most 6 runs.
func TestA7LoopStopped(t *testing.T) {
	loop := "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"triage\"\n"
	e := NewEnv(t, "", "", AgentSpec{ID: "ping", Rules: loop}, AgentSpec{ID: "pong", Rules: loop})
	e.Start()
	r := e.Send("ping", "message.ask", envelope.Draft{To: "pong", Kind: "ask", Intent: "loop.ping", Subject: "ping",
		Body: "always ask back #fake:followup=sender"})
	e.WaitFor("thread frozen", 30*time.Second, func() bool { return e.Thread(r.ThreadID).Frozen })
	time.Sleep(1500 * time.Millisecond) // let anything in flight settle
	tv := e.Thread(r.ThreadID)
	if len(tv.Runs) > 6 {
		t.Fatalf("%d runs, want at most 6", len(tv.Runs))
	}
	var escalated bool
	for _, n := range e.Notifications() {
		if strings.Contains(n["summary"].(string), "ping_pong") || strings.Contains(n["summary"].(string), "max_hops") {
			escalated = true
		}
	}
	if !escalated {
		t.Fatalf("no escalation notification: %v", e.Notifications())
	}
	for _, m := range tv.Messages {
		if m.State == "queued" || m.State == "running" || m.State == "retry_wait" {
			t.Fatalf("message %s still %s after freeze", m.ID, m.State)
		}
	}
	t.Logf("A7: frozen (%s) after %d messages, %d runs", tv.FrozenReason, len(tv.Messages), len(tv.Runs))
}
