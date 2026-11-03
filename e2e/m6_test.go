package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/config"
	"github.com/appnice/mailroom/internal/envelope"
)

// M6: doctor, gc, status metrics and the read commands work against a live daemon.
func TestM6OperatorCommands(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe", Extra: "budget_usd = 3"})
	e.Start()
	r := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "hello", Body: "#fake:cost=0.25"})
	e.WaitState(r.ID, 15*time.Second, "done")
	slow := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "cancel me", Body: "#fake:sleep=20"})
	e.WaitState(slow.ID, 10*time.Second, "running")
	if out, err := e.CLI("", "cancel", slow.ID); err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	e.WaitState(slow.ID, 10*time.Second, "rejected")

	out, _ := e.CLI("", "doctor")
	for _, want := range []string{"[ok  ] claude CLI 2.1.316", "[ok  ] daemon", "[ok  ] socket", "[ok  ] agent cafe repo", "[FAIL] installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	out, err := e.CLI("", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var s api.StatusView
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s.Metrics.CostToday["cafe"] != 0.25 || s.Metrics.BudgetLeft["cafe"] != 2.75 || s.Metrics.RunsLast24h != 2 || !s.Daemon.ClaudeOK {
		t.Fatalf("metrics: %+v", s.Metrics)
	}
	if out, err := e.CLI("", "status"); err != nil || !strings.Contains(out, "cafe") || !strings.Contains(out, "$0.25") {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if out, err := e.CLI("", "inbox", "cafe"); err != nil || !strings.Contains(out, "fake handled x.y") || !strings.Contains(out, "cancelled") {
		t.Fatalf("inbox: %v\n%s", err, out)
	}
	if out, err := e.CLI("", "thread", r.ID); err != nil || !strings.Contains(out, "#fake:cost") {
		t.Fatalf("thread shows the body to the human: %v\n%s", err, out)
	}
	runID := e.Runs(r.ID)[0].ID
	if out, err := e.CLI("", "run", runID, "--transcript"); err != nil || !strings.Contains(out, `"type":"result"`) {
		t.Fatalf("transcript: %v\n%s", err, out)
	}
	if out, err := e.CLI("", "tail", "--follow=false"); err != nil || !strings.Contains(out, "message.transition") {
		t.Fatalf("tail: %v", err)
	}
	if out, err := e.CLI("", "agents"); err != nil || !strings.Contains(out, "observability") {
		t.Fatalf("agents: %v\n%s", err, out)
	}
	var gc api.GCResult
	e.Call("admin.gc", nil, &gc)
	if out, err := e.CLI("", "reload"); err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("reload: %v %s", err, out)
	}
	// An invalid config is refused and the old one kept.
	good, _ := os.ReadFile(e.Config())
	os.WriteFile(e.Config(), []byte("[daemon]\nbogus = 1\n"), 0o600)
	if _, err := e.CLI("", "reload"); err == nil {
		t.Fatal("invalid reload accepted")
	}
	os.WriteFile(e.Config(), good, 0o600)
	r2 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "still works"})
	e.WaitState(r2.ID, 10*time.Second, "done")
	// Agents cannot read mail through the socket (D4).
	c := e.AgentClient("cafe")
	defer c.Close()
	for _, m := range []string{"thread.get", "thread.list", "digest.get", "run.list", "message.reply"} {
		if err := c.Call(m, api.IDParams{ID: r.ID}, nil, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s by an agent: %v", m, err)
		}
	}
	// A reply to an ask nobody awaited counts as ready until the digest is read.
	tk := e.Send("observability", "message.ask", envelope.Draft{To: "cafe", Kind: "ask", Intent: "x.y", Subject: "unawaited"})
	e.WaitFor("reply ready", 10*time.Second, func() bool {
		var c api.Counts
		e.Call("inbox.counts", api.CountsParams{Agent: "observability"}, &c)
		return c.RepliesReady == 1
	})
	_ = tk
	if _, err := e.CLI("", "digest", "--agent", "observability"); err != nil {
		t.Fatal(err)
	}
	var after api.Counts
	e.Call("inbox.counts", api.CountsParams{Agent: "observability"}, &after)
	if after.RepliesReady != 0 {
		t.Fatalf("digest did not clear replies ready: %+v", after)
	}
	// A sender cannot await someone else's ticket.
	ask := e.Send("observability", "message.ask", envelope.Draft{To: "cafe", Kind: "ask", Intent: "x.y", Subject: "q"})
	if err := c.Call("message.await", api.AwaitParams{Ticket: ask.ID}, nil, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("await of a foreign ticket: %v", err)
	}
}

// init writes a valid config, tokens and the status handler, and nothing
// into the repos unless --scaffold.
func TestInitNonInteractive(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "cafe"}, AgentSpec{ID: "web"})
	os.Remove(e.Config())
	out, err := e.CLI("", "init", "--agent", "cafe="+e.Repos["cafe"], "--agent", "web="+e.Repos["web"], "--yes")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	c, err := config.Load(e.Config())
	if err != nil || len(c.Agents) != 2 {
		t.Fatalf("config: %v", err)
	}
	for _, id := range []string{"human", "cafe", "web"} {
		fi, err := os.Stat(filepath.Join(e.Root, "cfg", "tokens", id))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token %s: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.Root, "cfg", "handlers", "status.sh")); err != nil {
		t.Fatalf("status handler not in the config dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Repos["cafe"], "scripts")); err == nil {
		t.Fatal("init wrote into the repo without --scaffold")
	}
	// The handler answers a real ask, with JSON-safe text from git.
	e.Git(e.Repos["cafe"], "commit", "--allow-empty", "-q", "-m", `say "hi" \ there`)
	e.Start()
	cl := e.AgentClient("web")
	defer cl.Close()
	var res api.SendResult
	if err := cl.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "cafe", Kind: "ask", Intent: "status.get", Subject: "state?"}, WaitS: 5}, &res, 10*time.Second); err != nil || res.Reply == nil {
		t.Fatalf("status handler: %v %+v", err, res)
	}
	t.Logf("status.get reply: %s", res.Reply.Text)
	if !strings.Contains(res.Reply.Text, "latest tag") || !strings.Contains(res.Reply.Text, `say "hi" \ there`) {
		t.Fatalf("reply %q", res.Reply.Text)
	}
	// A notice goes to the digest without a worker.
	n := e.Send("web", "message.emit", envelope.Draft{To: "cafe", Kind: "event", Intent: "notice.build", Subject: "build 12 done"})
	if m := e.WaitState(n.ID, 5*time.Second, "done"); m.Handler != "digest" {
		t.Fatalf("notice handled by %q", m.Handler)
	}

	// --scaffold puts the handler into each repo and points the rule there.
	out, err = e.CLI("", "init", "--agent", "cafe="+e.Repos["cafe"], "--yes", "--scaffold")
	if err != nil {
		t.Fatalf("init --scaffold: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.Repos["cafe"], "scripts", "mailroom", "status.sh")); err != nil {
		t.Fatal("--scaffold did not write the repo handler")
	}
	if b, _ := os.ReadFile(e.Config()); !strings.Contains(string(b), `command = ["./scripts/mailroom/status.sh"]`) {
		t.Fatalf("--scaffold config:\n%s", b)
	}
}

// $MAILROOM_AGENT names the agent of a session started outside its repo:
// the hooks, the status line and the digest follow it instead of the cwd.
func TestAgentFromEnv(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "cafe"}, AgentSpec{ID: "web"})
	e.Start()
	outside := t.TempDir()
	in := `{"session_id":"env1","cwd":"` + outside + `","hook_event_name":"SessionStart"}`
	if _, errOut, err := e.CLIDir(outside, in, []string{"MAILROOM_AGENT=web"}, "hook", "SessionStart"); err != nil {
		t.Fatalf("hook: %v %s", err, errOut)
	}
	var st api.StatusView
	e.Call("agent.status", struct{}{}, &st)
	for _, a := range st.Agents {
		if a.Interactive != (a.ID == "web") {
			t.Fatalf("agent %s interactive = %v", a.ID, a.Interactive)
		}
	}
	out, _, err := e.CLIDir(outside, "", []string{"MAILROOM_AGENT=web"}, "digest", "--cwd", outside, "--peek")
	if err != nil || !strings.Contains(out, "web") || strings.Contains(out, "cafe") {
		t.Fatalf("digest with MAILROOM_AGENT=web: %v\n%s", err, out)
	}
}
