package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// homeSample adds the home groups to sample(): one running message with
// its run, two waiting, two done today (one unread), one done yesterday.
func homeSample() Snapshot {
	s := sample()
	run := &s.Status.Running[0]
	run.Transcript = "/tmp/t.jsonl"
	s.Status.Agents[0].Intents = []string{"ask:store.status", "ask:app.version", "event:crash.*", "*"}
	s.Running = []api.MessageView{{ID: run.MessageID, To: "cafe", From: envelope.From{Agent: "observability"}, Kind: "event",
		Intent: "crash.report", State: "running", Subject: "NPE in CheckoutView", UpdatedAt: t0.Add(-70 * time.Second)}}
	s.Waiting = []api.MessageView{
		{ID: "01JMSG0000000000000000WAI1", To: "web", From: envelope.From{Agent: "observability"}, Kind: "event",
			Intent: "crash.report", State: "queued", Subject: "ANR on cart screen", HoldReason: "an interactive session is open in web",
			UpdatedAt: t0.Add(-3 * time.Minute)},
		{ID: "01JMSG0000000000000000WAI2", To: "web", From: envelope.From{Agent: "cafe"}, Kind: "ask",
			Intent: "asset.request", State: "retry_wait", Subject: "Icons for the dark mode", Reason: "claude overloaded",
			UpdatedAt: t0.Add(-1 * time.Minute)},
	}
	s.Done = []api.MessageView{
		{ID: "01JMSG0000000000000000DON1", To: "observability", From: envelope.From{Agent: "human"}, Kind: "event",
			Intent: "notice.harness", State: "done", Subject: "harness run smif-ui: green", Handler: "digest",
			UpdatedAt: t0.Add(-2 * time.Hour)},
		{ID: "01JMSG0000000000000000DON2", To: "cafe", From: envelope.From{Agent: "web"}, Kind: "ask",
			Intent: "store.status", State: "done", Subject: "Is Cafe live?", Summary: "1.0.17 (22) live on iOS",
			UpdatedAt: t0.Add(-5 * time.Hour)},
		{ID: "01JMSG0000000000000000OLD1", To: "cafe", From: envelope.From{Agent: "web"}, Kind: "tell",
			Intent: "x.y", State: "done", Subject: "yesterday", UpdatedAt: t0.Add(-30 * time.Hour)},
	}
	s.Unread = map[string]bool{"01JMSG0000000000000000DON1": true}
	s.DigestCursor = 42
	return s
}

const liveTranscript = `{"type":"system","subtype":"init","session_id":"s"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"src/lesson/CheckoutView.kt"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"git log -5 -- src/lesson"}}]}}
`

// fakeHome is the full fake: thread, actions, transcripts, digest, send.
type fakeHome struct {
	*fakeDigest
	sent []string
}

func (f *fakeHome) Send(ctx context.Context, method string, d envelope.Draft) (*api.SendResult, error) {
	f.sent = append(f.sent, method+" "+d.To+" "+d.Intent+" "+d.Subject)
	return &api.SendResult{ID: "01JMSG0000000000000000NEW1", State: "done"}, nil
}

func newHomeModel(t *testing.T, w, h int) (*Model, *fakeHome) {
	t.Helper()
	runID := homeSample().Status.Running[0].ID
	f := &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{homeSample()}}, threads: sampleThreads()},
		transcripts: map[string]string{runID: liveTranscript},
	}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(m, m.Init())
	return m, f
}

func TestHomeGoldens(t *testing.T) {
	m, _ := newHomeModel(t, 120, 40)
	checkSize(t, view(m), 120, 40)
	golden(t, "home_120x40.golden", view(m))
	key(m, "j") // the running worker and its live card
	golden(t, "home_running_120x40.golden", view(m))
	n, _ := newHomeModel(t, 80, 24)
	checkSize(t, view(n), 80, 24)
	golden(t, "home_80x24.golden", view(n))
}

func TestHomeNavigation(t *testing.T) {
	m, _ := newHomeModel(t, 120, 40)
	if got := m.HomeSelected(); got != "01JMSG000000000000000NEED1" {
		t.Fatalf("first selection %q, want the needs-you item", got)
	}
	key(m, "j")
	if !strings.Contains(view(m), "WORKER · LIVE") || !strings.Contains(view(m), "Read  src/lesson/CheckoutView.kt") {
		t.Fatalf("no live card for the running item:\n%s", view(m))
	}
	key(m, "tab") // the next row that needs you: the dead letter
	if got := m.HomeSelected(); got != "01JMSG0000000000000000DLQ1" {
		t.Fatalf("tab went to %q", got)
	}
	key(m, "tab") // round the list: the question
	if got := m.HomeSelected(); got != "01JMSG000000000000000NEED1" {
		t.Fatalf("second tab went to %q", got)
	}
	key(m, "shift+tab")
	if got := m.HomeSelected(); got != "01JMSG0000000000000000DLQ1" {
		t.Fatalf("shift+tab went to %q", got)
	}
	if strings.Contains(view(m), "yesterday") {
		t.Fatal("a message done yesterday shows under DONE TODAY")
	}
	key(m, "]") // first agent tab: cafe
	if m.HomeAgent() != "cafe" || strings.Contains(view(m), "ANR on cart") {
		t.Fatalf("agent tab %q:\n%s", m.HomeAgent(), view(m))
	}
	key(m, "0", "/", "c", "a", "r", "t", "enter")
	rows := m.homeRows()
	if len(rows) != 1 || rows[0].msg.ID != "01JMSG0000000000000000WAI1" {
		t.Fatalf("filter cart: %d rows", len(rows))
	}
	key(m, "esc")
	if len(m.homeRows()) != 7 {
		t.Fatalf("esc did not clear the filter: %d rows", len(m.homeRows()))
	}
	key(m, "v")
	if !strings.Contains(view(m), "AGENTS (3)") {
		t.Fatalf("v did not open the dashboard:\n%s", view(m))
	}
	key(m, "v")
	if strings.Contains(view(m), "AGENTS (3)") {
		t.Fatal("v did not come back from the dashboard")
	}
}

func TestHomeAnswerInline(t *testing.T) {
	m, f := newHomeModel(t, 120, 40)
	key(m, "a")
	key(m, "Ship it")
	if !strings.Contains(view(m), "› Ship it") {
		t.Fatalf("answer box:\n%s", view(m))
	}
	key(m, "enter")
	if len(f.replies) != 1 || f.replies[0] != "01JMSG000000000000000NEED1: Ship it" {
		t.Fatalf("replies %q", f.replies)
	}
	if m.home.typing || strings.Contains(view(m), "› Ship it") {
		t.Fatal("the answer box stayed open after the reply")
	}
}

func TestHomePrompt(t *testing.T) {
	m, f := newHomeModel(t, 120, 40)
	key(m, ":", "as", "tab")
	if got := m.prompt.in.Value(); got != "ask " {
		t.Fatalf("command completion: %q", got)
	}
	key(m, "ca", "tab")
	if got := m.prompt.in.Value(); got != "ask cafe " {
		t.Fatalf("agent completion: %q", got)
	}
	key(m, "st", "tab") // store.release and store.status: the common prefix
	if got := m.prompt.in.Value(); got != "ask cafe store." {
		t.Fatalf("common prefix: %q", got)
	}
	key(m, "sta", "tab")
	if got := m.prompt.in.Value(); got != "ask cafe store.status " {
		t.Fatalf("intent completion: %q", got)
	}
	key(m, "Is it live?", "enter")
	if len(f.sent) != 1 || f.sent[0] != "message.ask cafe store.status Is it live?" {
		t.Fatalf("sent %q", f.sent)
	}
	if m.prompt != nil || !strings.HasPrefix(m.Status(), "Sent ask") {
		t.Fatalf("after enter: prompt %v status %q", m.prompt != nil, m.Status())
	}
	key(m, ":", "nope", "enter")
	if !strings.Contains(m.Status(), `Unknown command "nope"`) {
		t.Fatalf("unknown command status %q", m.Status())
	}
	key(m, ":", "ask", " ", "nobody", " ", "x.y", "enter")
	if !strings.Contains(m.Status(), `No agent "nobody"`) || len(f.sent) != 1 {
		t.Fatalf("ask to an unknown agent: %q %q", m.Status(), f.sent)
	}
}

func TestHomeMarkRead(t *testing.T) {
	m, f := newHomeModel(t, 120, 40)
	if !strings.Contains(view(m), "1 unread (e marks read)") {
		t.Fatalf("no unread count:\n%s", view(m))
	}
	key(m, "e")
	if len(f.calls) == 0 || !f.calls[len(f.calls)-1].Advance || f.calls[len(f.calls)-1].Until != 42 {
		t.Fatalf("digest calls %+v", f.calls)
	}
}

func TestToolTrail(t *testing.T) {
	steps := toolTrail([]byte(liveTranscript), 8)
	if len(steps) != 2 || !steps[0].Done || steps[1].Done || steps[1].Name != "Bash" {
		t.Fatalf("steps %+v", steps)
	}
	if got := toolTrail([]byte(liveTranscript), 1); len(got) != 1 || got[0].Name != "Bash" {
		t.Fatalf("last 1: %+v", got)
	}
}
