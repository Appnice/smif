package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/envelope"
	"github.com/appnice/smif/internal/ui"
)

// UI-HOME: the start screen against a live daemon. Every group fills from
// real traffic, the human answers a needs-you item in the box under the
// preview, a running worker shows its live card, and an ask typed at the
// `:` prompt is answered by a command handler.
func TestUIHomeLive(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe", Before: withScript("store-status.sh", storeStatusScript), Rules: `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/smif/store-status.sh"]
[[agents.AGENT.rules]]
match = {}
handler = "worker"
profile = "triage"
`},
		AgentSpec{ID: "web", Extra: "max_attempts = 1"})
	e.Start()

	need := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "release.decide",
		Subject: "Ship 1.0.2 now?", Body: "#fake:result=needs_human"})
	e.WaitState(need.ID, 15*time.Second, "needs_human")
	dead := e.Send("observability", "message.send", envelope.Draft{To: "web", Kind: "tell", Intent: "crash.report",
		Subject: "boom", Body: "#fake:exit=3"})
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	long := e.Send("observability", "message.send", envelope.Draft{To: "web", Kind: "tell", Intent: "report.build",
		Subject: "Build the long report", Body: "#fake:sleep=30"})
	e.WaitState(long.ID, 15*time.Second, "running")

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{}))
	v := u.until("the home screen", 10*time.Second, has("NEEDS YOU · 1", "RUNNING · 1", "DEAD · 1", "Ship 1.0.2 now?"))
	t.Logf("home:\n%s", v)
	if u.m.HomeSelected() != need.ID {
		t.Fatalf("first selection %q, want the needs-you item %s", u.m.HomeSelected(), need.ID)
	}

	// The running worker has a live card.
	u.press("down")
	if u.m.HomeSelected() != long.ID {
		t.Fatalf("down selected %q, want the running %s", u.m.HomeSelected(), long.ID)
	}
	u.until("the live card", 5*time.Second, has("WORKER · LIVE", "worker/triage"))

	// Answer the needs-you item inline; the thread continues.
	u.press("up", "a", "Ship it", "enter")
	u.until("the reply result", 10*time.Second, func(string) bool { return strings.HasPrefix(u.m.Status(), "Reply sent to") })
	if m := e.Message(need.ID); m.State != "done" {
		t.Fatalf("needs-you item after the answer: %s", m.State)
	}

	// An ask from the prompt, answered by the command handler.
	u.press(":", "ask cafe store.status Live?", "enter")
	u.until("the ask sent", 10*time.Second, func(string) bool { return strings.HasPrefix(u.m.Status(), "Sent ask") })
	v = u.until("the answer in the preview", 10*time.Second, has("1.0.1 (4)"))
	t.Logf("after :ask:\n%s", v)

	u.press("q")
}
