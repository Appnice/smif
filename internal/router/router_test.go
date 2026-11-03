package router

import (
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/envelope"
)

func cfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load("../../testdata/rfc-example.toml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func env(from, to, kind, intent, prio string, hop int) *envelope.Envelope {
	return &envelope.Envelope{From: envelope.From{Agent: from}, To: to, Kind: kind, Intent: intent, Priority: prio, Hop: hop}
}

func TestRouteRFCExample(t *testing.T) {
	c := cfg(t)
	cases := []struct {
		e       *envelope.Envelope
		action  Action
		handler string
		reason  string
	}{
		{env("atlas", "cafe", "ask", "store.status", "normal", 0), Queue, "command", ""},
		{env("observability", "puzzle", "event", "crash.report", "high", 0), Queue, "worker", ""},
		{env("cafe", "puzzle", "event", "crash.report", "high", 0), Escalate, "escalate", ""},
		{env("lab", "puzzle", "ask", "asset.icon", "normal", 0), Queue, "worker", ""},
		{env("observability", "atlas", "tell", "something.else", "low", 0), Digest, "digest", ""},
		{env("stranger", "cafe", "ask", "store.status", "normal", 0), Reject, "", "sender_not_allowed"},
		{env("cafe", "nobody", "tell", "x", "normal", 0), Reject, "", "unknown_agent"},
		{env("observability", "cafe", "event", "crash.x", "normal", 5), Reject, "", "max_hops"},
		{env(config.Human, "cafe", "tell", "x", "normal", 0), Escalate, "escalate", ""},
	}
	for i, tc := range cases {
		d := Route(c, tc.e, ThreadStats{Messages: 1, PairRecent: 1}, false)
		if d.Action != tc.action || (tc.handler != "" && d.Handler != tc.handler) || !strings.Contains(d.Reason, tc.reason) {
			t.Errorf("case %d (%s->%s %s %s): got %+v", i, tc.e.From.Agent, tc.e.To, tc.e.Kind, tc.e.Intent, d)
		}
	}
}

func TestGuards(t *testing.T) {
	c := cfg(t)
	e := env("observability", "cafe", "event", "crash.x", "normal", 1)
	if d := Route(c, e, ThreadStats{Frozen: true, FrozenReason: "x"}, false); d.Action != Reject || !strings.HasPrefix(d.Reason, "thread_frozen") {
		t.Fatalf("frozen: %+v", d)
	}
	if d := Route(c, e, ThreadStats{Messages: 21}, false); d.Action != Reject || !d.Freeze || !d.Notify {
		t.Fatalf("thread cap: %+v", d)
	}
	if d := Route(c, e, ThreadStats{Messages: 5, PairRecent: 7}, false); d.Action != Reject || !strings.HasPrefix(d.Reason, "ping_pong") || !d.Freeze {
		t.Fatalf("ping pong: %+v", d)
	}
	if d := Route(c, e, ThreadStats{Messages: 5, PairRecent: 6}, false); d.Action != Queue {
		t.Fatalf("6 messages is still fine: %+v", d)
	}
	// A reply to your own ask bypasses the allowlist.
	r := env("stranger-no", "observability", "reply", "x", "normal", 1)
	c.Agents["observability"].AcceptFrom = []string{"cafe"}
	if d := Route(c, r, ThreadStats{}, false); d.Action != Reject {
		t.Fatalf("allowlist: %+v", d)
	}
	r.From.Agent = "puzzle"
	if d := Route(c, r, ThreadStats{}, true); d.Action != Digest {
		t.Fatalf("own reply: %+v", d)
	}
}

func TestMatchFields(t *testing.T) {
	c, err := config.Parse([]byte(`
[agents.a]
repo = "/tmp/a"
[agents.b]
repo = "/tmp/b"
[[agents.a.rules]]
match = { from = ["b"], kind = ["ask", "tell"], intent = "x.*", priority_at_least = "high" }
handler = "drop"
[[agents.a.rules]]
match = {}
handler = "digest"
[[agents.b.rules]]
match = {}
handler = "digest"
`))
	if err != nil {
		t.Fatal(err)
	}
	a := c.Agents["a"]
	r := a.Rules[0]
	if !Match(r, env("b", "a", "ask", "x.y", "urgent", 0)) {
		t.Fatal("should match")
	}
	for _, e := range []*envelope.Envelope{env("c", "a", "ask", "x.y", "urgent", 0), env("b", "a", "event", "x.y", "urgent", 0),
		env("b", "a", "ask", "z.y", "urgent", 0), env("b", "a", "ask", "x.y", "normal", 0)} {
		if Match(r, e) {
			t.Fatalf("should not match %+v", e)
		}
	}
	if d := Route(c, env("b", "a", "tell", "x.q", "high", 0), ThreadStats{}, false); d.Action != Reject || d.Handler != "drop" {
		t.Fatalf("drop: %+v", d)
	}
	if FirstRule(a, env("b", "a", "event", "q", "low", 0)) != a.Rules[1] {
		t.Fatal("catch-all")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("token %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("4th allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other sender limited")
	}
	now = now.Add(20 * time.Second) // one token per 20 s at 3/min
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("refill wrong")
	}
	l.SetRate(600)
	now = now.Add(time.Minute)
	for i := 0; i < 10; i++ {
		if !l.Allow("a") {
			t.Fatal("new rate not applied")
		}
	}
}
