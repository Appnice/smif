package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/mailroom/internal/api"
)

// fakePrompter is fakeHome plus session.prompt.
type fakePrompter struct {
	*fakeHome
	prompts []string
}

func (f *fakePrompter) Prompt(ctx context.Context, id, text string) (*api.SessionPromptResult, error) {
	f.prompts = append(f.prompts, id+" "+text)
	return &api.SessionPromptResult{Agent: "cafe", SessionID: id}, nil
}

func (f *fakePrompter) SendSession(ctx context.Context, id, text string) (*api.SessionSendResult, error) {
	f.prompts = append(f.prompts, id+" "+text)
	mode := "typed"
	if strings.HasPrefix(id, "busy") {
		mode = "worker"
	}
	return &api.SessionSendResult{Mode: mode, Agent: "cafe", SessionID: id}, nil
}

func (f *fakePrompter) FocusSession(ctx context.Context, id string) error {
	f.prompts = append(f.prompts, "focus "+id)
	return nil
}

func (f *fakePrompter) SessionTail(ctx context.Context, id string) (*api.SessionTail, error) {
	if id == "busy-session-0001" {
		return &api.SessionTail{SessionID: id, Turns: []api.SessionTurn{
			{Role: "you", Text: "fix the flaky test", At: t0.Add(-3 * time.Minute)},
			{Role: "ask", From: "you", Text: "which test was it?", At: t0.Add(-time.Minute), State: "done"},
			{Role: "worker", Text: "**TestCheckout** in `cart_test.go`.", At: t0.Add(-30 * time.Second), State: "ok"},
		}}, nil
	}
	if id != "idle-session-0001" {
		return &api.SessionTail{SessionID: id}, nil
	}
	return &api.SessionTail{SessionID: id, Turns: []api.SessionTurn{
		{Role: "you", Text: "did review pass?", At: t0.Add(-40 * time.Minute)},
		{Role: "claude", Text: "Yes, Apple approved build 12.", At: t0.Add(-39 * time.Minute), Tools: 1},
		{Role: "you", Text: "is the new version in the stores?", At: t0.Add(-11 * time.Minute)},
		{Role: "claude", Text: "Yes, 1.0.2 is live in both stores:\n- **App Store:** READY_FOR_SALE\n- **Google Play:** production, 100% rollout", At: t0.Add(-10 * time.Minute), Tools: 3},
	}}, nil
}

func sessionSample() Snapshot {
	s := homeSample()
	s.Sessions = []api.SessionView{
		{Agent: "web", SessionID: "busy-session-0001", Repo: "/r/web", Turn: "busy", TurnAt: t0.Add(-2 * time.Minute),
			TTY: "/dev/ttys004", Term: "Apple_Terminal", Why: "a prompt is running"},
		{Agent: "cafe", SessionID: "idle-session-0001", Repo: "/r/cafe", Turn: "idle", TurnAt: t0.Add(-9 * time.Minute),
			TTY: "/dev/ttys003", Term: "Apple_Terminal", CanPrompt: true, Title: "release", LastPrompt: "bump the version and run the store checks",
			LastAnswer: "Yes, 1.0.2 is live in both stores:\n- **App Store:** READY_FOR_SALE", LastAnswerAt: t0.Add(-10 * time.Minute)},
	}
	return s
}

func newSessionModel(t *testing.T) (*Model, *fakePrompter) {
	t.Helper()
	f := &fakePrompter{fakeHome: &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{sessionSample()}}, threads: sampleThreads()},
	}}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	return m, f
}

func TestHomeSessions(t *testing.T) {
	m, f := newSessionModel(t)
	out := view(m)
	for _, want := range []string{"SESSIONS · 2", "○ release  Yes, 1.0.2 is live", "web      working · 2m", "All 7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("home lacks %q:\n%s", want, out)
		}
	}
	// Sessions sort by name: release (idle), then web (busy).
	key(m, "j")
	if got := m.HomeSelected(); got != "session:idle-session-0001" {
		t.Fatalf("j selected %q, want the idle session", got)
	}
	out = view(m)
	// Only the final answer, under the last prompt; earlier answers one line each.
	for _, want := range []string{"○ IDLE", "release", "ttys003", "› you", "is the new version in the stores?", "◆ answer",
		"3 tools", "• App Store: READY_FOR_SALE", "Yes, Apple approved build 12.", "[s] reply", "[o] show its tab", "s write"} {
		if !strings.Contains(out, want) {
			t.Fatalf("session preview lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "**") || strings.Contains(out, "did review pass?") {
		t.Fatalf("session preview shows raw Markdown or an old prompt:\n%s", out)
	}
	golden(t, "home_sessions_120x40.golden", out)
	key(m, "s", "run the tests", "enter")
	if len(f.prompts) != 1 || f.prompts[0] != "idle-session-0001 run the tests" {
		t.Fatalf("prompts %q", f.prompts)
	}
	if m.home.typing || !strings.HasPrefix(m.Status(), "Typed into release") {
		t.Fatalf("after enter: typing %v status %q", m.home.typing, m.Status())
	}
	// A busy session: the text goes to a worker with its context.
	key(m, "j")
	if got := m.HomeSelected(); got != "session:busy-session-0001" {
		t.Fatalf("second j selected %q", got)
	}
	if out := view(m); !strings.Contains(out, "[s] ask a worker") || strings.Contains(out, "[s] reply") || !strings.Contains(out, "◇ worker") ||
		!strings.Contains(out, "TestCheckout in cart_test.go.") || !strings.Contains(out, "› you → worker") {
		t.Fatalf("busy session keys:\n%s", out)
	}
	key(m, "s")
	if !m.home.typing || !strings.Contains(view(m), "enter asks a worker that knows this session") {
		t.Fatalf("busy session box:\n%s", view(m))
	}
	key(m, "what now?", "enter")
	if len(f.prompts) != 2 || f.prompts[1] != "busy-session-0001 what now?" || !strings.Contains(m.Status(), "a worker with its context answers") {
		t.Fatalf("busy send: %q %q", f.prompts, m.Status())
	}
	// The command prompt: prompt <agent> <text> picks the one idle session.
	key(m, ":", "pro", "tab", "ca", "tab", "fix the build", "enter")
	if len(f.prompts) != 3 || f.prompts[2] != "idle-session-0001 fix the build" {
		t.Fatalf("prompt command sent %q", f.prompts)
	}
	key(m, ":", "prompt web hello", "enter")
	if len(f.prompts) != 3 || !strings.Contains(m.Status(), "web has no idle session") {
		t.Fatalf("prompt to a busy agent: %q %q", f.prompts, m.Status())
	}
	// ... or the session with that name.
	key(m, ":", "prompt Release ship it", "enter")
	if len(f.prompts) != 4 || f.prompts[3] != "idle-session-0001 ship it" {
		t.Fatalf("prompt by name sent %q (%s)", f.prompts, m.Status())
	}
}

// Session rows keep one order by name whatever order the daemon sends and
// whatever their state, so they do not jump on each refresh.
func TestHomeSessionsKeepOrder(t *testing.T) {
	order := func(ss []api.SessionView) string {
		m := New(&fakeSource{snaps: []Snapshot{{Sessions: ss}}}, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		run(m, m.Init())
		var names []string
		for _, r := range m.homeRows() {
			if r.sess != nil {
				names = append(names, sessionName(r.sess))
			}
		}
		return strings.Join(names, ",")
	}
	a := api.SessionView{Agent: "obs", SessionID: "a1", Title: "bonsai", Turn: "busy"}
	b := api.SessionView{Agent: "obs", SessionID: "b1", Title: "Lab", Turn: "idle", CanPrompt: true}
	c := api.SessionView{Agent: "obs", SessionID: "c1", Title: "issues", Turn: "idle", CanPrompt: true}
	want := "bonsai,issues,Lab"
	for _, ss := range [][]api.SessionView{{a, b, c}, {c, b, a}, {b, a, c}} {
		if got := order(ss); got != want {
			t.Fatalf("order %q, want %q", got, want)
		}
	}
}

// enter opens the chat with a session: the whole conversation and a box
// that has the keys at once; enter sends and the chat stays; ctrl+o shows
// its tab; esc comes back. An answer that comes while the UI runs is
// marked new until it is seen.
func TestSessionAnswerViews(t *testing.T) {
	m, f := newSessionModel(t)
	key(m, "j")
	key(m, "enter")
	out := view(m)
	for _, want := range []string{"esc back", "did review pass?", "Yes, Apple approved build 12.", "◆ answer", "• App Store: READY_FOR_SALE",
		"enter types it into release"} {
		if !strings.Contains(out, want) {
			t.Fatalf("chat lacks %q:\n%s", want, out)
		}
	}
	if !m.home.full || !m.home.typing || strings.Contains(out, "NEEDS YOU") {
		t.Fatalf("chat state full=%v typing=%v:\n%s", m.home.full, m.home.typing, out)
	}
	golden(t, "session_chat_120x40.golden", out)
	key(m, "hello there", "enter")
	if len(f.prompts) != 1 || f.prompts[0] != "idle-session-0001 hello there" || !m.home.full || !m.home.typing {
		t.Fatalf("chat send: %q full=%v typing=%v", f.prompts, m.home.full, m.home.typing)
	}
	key(m, "ctrl+o")
	if len(f.prompts) != 2 || f.prompts[1] != "focus idle-session-0001" {
		t.Fatalf("ctrl+o sent %q", f.prompts)
	}
	key(m, "esc")
	if m.home.full || m.home.typing || !strings.Contains(view(m), "NEEDS YOU") {
		t.Fatalf("esc did not close the chat:\n%s", view(m))
	}
	key(m, "o")
	if len(f.prompts) != 3 || f.prompts[2] != "focus idle-session-0001" {
		t.Fatalf("o sent %q", f.prompts)
	}
	// A newer answer while another row is selected: ● and the count.
	key(m, "j")
	m.snap.Sessions[1].LastAnswerAt = t0
	out = view(m)
	if !strings.Contains(out, "1 new answer") || !strings.Contains(out, "● release") {
		t.Fatalf("new answer not marked:\n%s", out)
	}
	key(m, "k")
	view(m)
	if out = view(m); strings.Contains(out, "new answer") {
		t.Fatalf("seen answer still new:\n%s", out)
	}
	// A session that waits for a permission or a question says so.
	m.snap.Sessions[0].Turn = "waiting"
	key(m, "j")
	if out = view(m); !strings.Contains(out, "! web") || !strings.Contains(out, "waits for you in its tab") || !strings.Contains(out, "! WAITS FOR YOU") {
		t.Fatalf("waiting session:\n%s", out)
	}
}

// An answer that comes while the cursor rests on the session stays new
// (● and the count) until the human moves onto the session again; what
// was seen survives a restart through the seen file.
func TestSessionNewAnswerWhileSelected(t *testing.T) {
	seenFile := filepath.Join(t.TempDir(), "ui-seen.json")
	f := &fakePrompter{fakeHome: &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{sessionSample()}}, threads: sampleThreads()},
	}}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond, SeenFile: seenFile})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	key(m, "j") // on release
	m.snap.Sessions[1].LastAnswerAt = t0
	m.noteSessions() // a refresh while the cursor stays
	if out := view(m); !strings.Contains(out, "● release") || !strings.Contains(out, "1 new answer") {
		t.Fatalf("answer under the cursor not marked new:\n%s", out)
	}
	// A restart keeps it new.
	m2 := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond, SeenFile: seenFile})
	m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m2, m2.Init())
	m2.snap.Sessions[1].LastAnswerAt = t0
	if out := view(m2); !strings.Contains(out, "1 new answer") {
		t.Fatalf("new answer lost on restart:\n%s", out)
	}
	key(m, "j", "k") // away and back
	if out := view(m); strings.Contains(out, "new answer") {
		t.Fatalf("moving onto the session did not read it:\n%s", out)
	}
}
