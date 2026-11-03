package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// message.cancel returns the state the item has after the call (ADR-0009):
// "rejected" for an item it rejects at once, "cancelling" only for a running
// item whose worker must stop. The CLI prints the same result.
func TestCancelReturnsState(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability"}, AgentSpec{ID: "cafe"})
	e.Start()

	long := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "report.build",
		Subject: "Build the long report", Body: "#fake:sleep=60"})
	e.WaitState(long.ID, 15*time.Second, "running")
	// The transcript path is known while the worker runs, so it can be tailed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs := e.Runs(long.ID)
		if len(rs) == 1 && rs[0].Transcript != "" {
			if b, _ := os.ReadFile(rs[0].Transcript); strings.Contains(string(b), `"subtype":"init"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live transcript for the running run: %+v", rs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// One worker per agent: the next two items wait in the queue.
	q1 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "queued one", Body: "one"})
	q2 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "queued two", Body: "two"})
	e.WaitState(q1.ID, 5*time.Second, "queued")
	e.WaitState(q2.ID, 5*time.Second, "queued")

	// A queued item: rejected at once, and the store agrees without a wait.
	var r api.CancelResult
	e.Call("message.cancel", api.IDParams{ID: q1.ID}, &r)
	t.Logf("cancel of the queued item: %+v", r)
	if r.ID != q1.ID || r.State != api.CancelRejected {
		t.Fatalf("queued cancel = %+v, want state %q", r, api.CancelRejected)
	}
	if m := e.Message(q1.ID); m.State != "rejected" || m.Reason != "cancelled" {
		t.Fatalf("queued item after the cancel: state %s reason %q", m.State, m.Reason)
	}

	// The CLI tells the same result.
	out, err := e.CLI("", "cancel", q2.ID)
	t.Logf("smif cancel <queued>: %s", strings.TrimSpace(out))
	if err != nil || strings.TrimSpace(out) != "cancelled "+q2.ID+": rejected" {
		t.Fatalf("cli cancel of a queued item: %v %q", err, out)
	}
	if s := e.Message(q2.ID).State; s != "rejected" {
		t.Fatalf("q2 state %s, want rejected", s)
	}

	// A running item: the worker must stop first.
	r = api.CancelResult{}
	e.Call("message.cancel", api.IDParams{ID: long.ID}, &r)
	t.Logf("cancel of the running item: %+v", r)
	if r.ID != long.ID || r.State != api.CancelCancelling {
		t.Fatalf("running cancel = %+v, want state %q", r, api.CancelCancelling)
	}
	m := e.WaitState(long.ID, 15*time.Second, "rejected")
	if m.Reason != "cancelled" {
		t.Fatalf("running item reason = %q, want cancelled", m.Reason)
	}
	if rs := e.Runs(long.ID); len(rs) != 1 || rs[0].State != "cancelled" {
		t.Fatalf("runs after the cancel: %+v", rs)
	}
	human := false
	for _, a := range e.Audit() {
		if b, _ := json.Marshal(a); a["message_id"] == long.ID && strings.Contains(string(b), `"human":true`) {
			human = true
		}
	}
	if !human {
		t.Fatal("the cancel of the running item has no human=true audit row")
	}

	// A rejected item cannot be cancelled again: an error, not a state.
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r = api.CancelResult{}
	if err := c.Call("message.cancel", api.IDParams{ID: q1.ID}, &r, 10*time.Second); !api.IsCode(err, api.CodeRejected) || r.State != "" {
		t.Fatalf("second cancel: err %v result %+v, want a rejected error", err, r)
	}
}

// The CLI names a running cancel as "cancelling", not as done.
func TestCancelCLIRunning(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability"}, AgentSpec{ID: "cafe"})
	e.Start()
	long := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "report.build",
		Subject: "Build the long report", Body: "#fake:sleep=60"})
	e.WaitState(long.ID, 15*time.Second, "running")
	out, err := e.CLI("", "cancel", long.ID)
	t.Logf("smif cancel <running>: %s", strings.TrimSpace(out))
	if err != nil || strings.TrimSpace(out) != "cancelling "+long.ID+": the worker is stopping; see smif thread "+long.ID {
		t.Fatalf("cli cancel of a running item: %v %q", err, out)
	}
	e.WaitState(long.ID, 15*time.Second, "rejected")
}
