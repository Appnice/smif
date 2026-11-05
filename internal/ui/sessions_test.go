package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
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

func (f *fakePrompter) NewSession(ctx context.Context, dir, name, term string) error {
	f.prompts = append(f.prompts, "new "+dir+" "+name+" "+term)
	return nil
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
			TTY: "/dev/ttys004", Term: "Apple_Terminal", Why: "a prompt is running", Activity: busyActivity()},
		{Agent: "cafe", SessionID: "idle-session-0001", Repo: "/r/cafe", Turn: "idle", TurnAt: t0.Add(-9 * time.Minute),
			TTY: "/dev/ttys003", Term: "Apple_Terminal", CanPrompt: true, Title: "release", LastPrompt: "bump the version and run the store checks",
			LastAnswer: "Yes, 1.0.2 is live in both stores:\n- **App Store:** READY_FOR_SALE", LastAnswerAt: t0.Add(-10 * time.Minute)},
	}
	return s
}

// busyActivity is the busy session's newest exchange: two steps done,
// the third one running.
func busyActivity() *api.SessionActivity {
	run := api.SessionStep{Tool: "Bash", Target: "go test -run TestCheckout -count 20 ./cart/", At: t0.Add(-20 * time.Second)}
	return &api.SessionActivity{Prompt: "fix the flaky test", PromptAt: t0.Add(-3 * time.Minute), Tools: 3, WroteAt: t0.Add(-10 * time.Second),
		Steps: []api.SessionStep{
			{Tool: "Read", Target: "cart/cart_test.go", At: t0.Add(-170 * time.Second), Done: true},
			{Tool: "Edit", Target: "cart/cart.go", At: t0.Add(-90 * time.Second), Done: true},
			run,
		}, Now: &run}
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
	for _, want := range []string{"SESSIONS · 2", "○ idle   release  Yes, 1.0.2 is live", "▶ 3m00s  web      Bash  go test -run",
		"3 tools", "1 working  1 quiet", "● live"} {
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
	// Only the final answer, under the last prompt.
	for _, want := range []string{"○ IDLE", "release", "ttys003", "you asked", "is the new version in the stores?", "◆ answer",
		"3 tools", "• App Store: READY_FOR_SALE", "s reply…", "It is idle: what you write is typed into its tab.", "s write", "o its tab"} {
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
	if m.home.typing || !strings.HasPrefix(m.Status(), "Sent to release") {
		t.Fatalf("after enter: typing %v status %q", m.home.typing, m.Status())
	}
	// The daemon types in the background: a prompt that took no turn
	// comes back on the session and shows in the status line.
	for i := range m.snap.Sessions {
		if sv := &m.snap.Sessions[i]; sv.SessionID == "idle-session-0001" {
			sv.Unsent, sv.UnsentAt = "claude started no turn", time.Now()
		}
	}
	m.notePending()
	if !strings.HasPrefix(m.Status(), "Not sent to release: claude started no turn") {
		t.Fatalf("unsent prompt: status %q", m.Status())
	}
	// A busy session: what it does now, and the text goes to a fork.
	key(m, "j")
	if got := m.HomeSelected(); got != "session:busy-session-0001" {
		t.Fatalf("second j selected %q", got)
	}
	out = view(m)
	for _, want := range []string{"▶ WORKING 3m00s", "working on · you asked", "fix the flaky test", "now · for 20s",
		"Bash  go test -run TestCheckout -count 20 ./cart/", "steps so far", "Edit  cart/cart.go", "› you → fork", "◇ fork",
		"TestCheckout in cart_test.go.", "s ask while it works…", "s ask a fork"} {
		if !strings.Contains(out, want) {
			t.Fatalf("busy session lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s reply…") {
		t.Fatalf("a busy session offers a reply:\n%s", out)
	}
	golden(t, "home_session_busy_120x40.golden", out)
	key(m, "s")
	if !m.home.typing || !strings.Contains(view(m), "a fork of this session answers") {
		t.Fatalf("busy session box:\n%s", view(m))
	}
	key(m, "what now?", "enter")
	if len(f.prompts) != 2 || f.prompts[1] != "busy-session-0001 what now?" || !strings.Contains(m.Status(), "a fork of it answers") {
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
		"it is idle, so enter types into its tab"} {
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
	if !strings.Contains(out, "1 new") || !strings.Contains(out, "● new    release") {
		t.Fatalf("new answer not marked:\n%s", out)
	}
	key(m, "k")
	view(m)
	if out = view(m); strings.Contains(out, "● new") || strings.Contains(out, "1 new") {
		t.Fatalf("seen answer still new:\n%s", out)
	}
	// A session that waits for a permission says what it asks for, and
	// tab goes to it.
	m.snap.Sessions[0].Turn = "waiting"
	key(m, "tab")
	if got := m.HomeSelected(); got != "session:busy-session-0001" {
		t.Fatalf("tab went to %q", got)
	}
	out = view(m)
	for _, want := range []string{"! 2m     web", "Bash  go test -run TestCheckout", "needs you", "1 waiting", "! WAITING 2m",
		"it stopped on a permission prompt in its tab", "nothing has run since", "before it asked", "o answer in its tab",
		"o bring its tab to the front and answer there"} {
		if !strings.Contains(out, want) {
			t.Fatalf("waiting session lacks %q:\n%s", want, out)
		}
	}
	golden(t, "home_session_waiting_120x40.golden", out)
	// z hides the quiet ones; the header says how many.
	m.snap.Sessions[0].Turn = "idle"
	key(m, "z")
	if out = view(m); strings.Contains(out, "○ idle   release") || !strings.Contains(out, "2 quiet hidden (z)") {
		t.Fatalf("z did not hide the quiet sessions:\n%s", out)
	}
	key(m, "z")
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
	if out := view(m); !strings.Contains(out, "● new    release") || !strings.Contains(out, "1 new") {
		t.Fatalf("answer under the cursor not marked new:\n%s", out)
	}
	// A restart keeps it new.
	m2 := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond, SeenFile: seenFile})
	m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m2, m2.Init())
	m2.snap.Sessions[1].LastAnswerAt = t0
	if out := view(m2); !strings.Contains(out, "1 new") {
		t.Fatalf("new answer lost on restart:\n%s", out)
	}
	key(m, "j", "k") // away and back
	if out := view(m); strings.Contains(out, "1 new") {
		t.Fatalf("moving onto the session did not read it:\n%s", out)
	}
}

// p on a session: the saved prompts, sent like s; n: a new session in the
// selected session's folder and terminal, with the name typed in.
func TestSavedPromptsAndNewSession(t *testing.T) {
	m, f := newSessionModel(t)
	key(m, "j") // release, idle
	key(m, "p")
	if !strings.Contains(m.Status(), "No saved prompts") {
		t.Fatalf("p without prompts: %q", m.Status())
	}
	saved := []api.SavedPrompt{{Name: "review", Text: "review your last change"}, {Name: "tests", Text: "run the tests and report"}}
	m.snap.Prompts = saved
	for i := range f.snaps {
		f.snaps[i].Prompts = saved // the refresh after a send keeps them
	}
	key(m, "p")
	out := view(m)
	for _, want := range []string{"saved prompts", "1 review", "2 tests", "run the tests and report", "enter or 1-9 sends"} {
		if !strings.Contains(out, want) {
			t.Fatalf("picker lacks %q:\n%s", want, out)
		}
	}
	golden(t, "home_session_prompts_120x40.golden", out)
	key(m, "2")
	if m.home.picking || len(f.prompts) != 1 || f.prompts[0] != "idle-session-0001 run the tests and report" {
		t.Fatalf("picked: %v %q", m.home.picking, f.prompts)
	}
	key(m, "p", "down", "up", "enter")
	if len(f.prompts) != 2 || f.prompts[1] != "idle-session-0001 review your last change" {
		t.Fatalf("picked with enter: %q", f.prompts)
	}
	key(m, "n")
	if !m.home.newing || !strings.Contains(view(m), "new session in /r/cafe · name:") {
		t.Fatalf("n:\n%s", view(m))
	}
	key(m, "api", "enter")
	if m.home.newing || len(f.prompts) != 3 || f.prompts[2] != "new /r/cafe api Apple_Terminal" || !strings.HasPrefix(m.Status(), "Opened api in /r/cafe") {
		t.Fatalf("new session: %q %q", f.prompts, m.Status())
	}
	key(m, "n", "esc")
	if m.home.newing || len(f.prompts) != 3 {
		t.Fatal("esc did not cancel the new session")
	}
}

// levelSample has one row of each kind that no provider reports.
func levelSample() Snapshot {
	s := sessionSample()
	s.Sessions = append(s.Sessions,
		api.SessionView{Harness: "codex", Level: "herdr", SessionID: "h1000000000000000", Repo: "/r/api", Turn: "idle", TurnAt: t0.Add(-5 * time.Minute),
			TTY: "/dev/ttys010", Term: "herdr", Title: "codex", CanPrompt: true},
		api.SessionView{Harness: "codex", Level: "herdr", SessionID: "h2000000000000000", Repo: "/r/api", Turn: "waiting", TurnAt: t0.Add(-2 * time.Minute),
			TTY: "/dev/ttys011", Term: "herdr", Title: "reviewer", Why: "it waits for you in its tab (a permission or a question); o shows the tab"},
		api.SessionView{Harness: "gemini", Level: "herdr", SessionID: "h3000000000000000", Repo: "/r/api", Turn: "unknown", TurnAt: t0.Add(-time.Minute),
			TTY: "/dev/ttys012", Term: "herdr", Title: "gemini", Why: "herdr cannot tell this agent's state, so Smif does not type"},
		api.SessionView{Harness: "aider", Level: "listed", SessionID: "p4000000000000000", Repo: "/r/api", Turn: "unknown", LastSeen: t0,
			TTY: "/dev/ttys013", Term: "Apple_Terminal", Title: "aider", Why: "listed only: Smif sees the process but not its state, so it does not type"},
	)
	return s
}

// The herdr and listed levels show the agent and a text mark in the row,
// and the preview says why typing is off, with no style needed.
func TestHomeSessionLevels(t *testing.T) {
	f := &fakePrompter{fakeHome: &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{levelSample()}}, threads: sampleThreads()},
	}}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	out := view(m)
	for _, want := range []string{"? unknown aider    [listed] no typing", "○ idle   codex     [herdr] idle", "! 2m     reviewer  codex [herdr] waits",
		"? unknown gemini   [herdr] no typing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("home lacks %q:\n%s", want, out)
		}
	}
	golden(t, "home_session_levels_120x40.golden", out)

	sel := func(id string) *api.SessionView {
		t.Helper()
		for range len(m.homeRows()) {
			key(m, "k")
		}
		for range len(m.homeRows()) {
			if r := m.homeSelected(); r != nil && r.sess != nil && r.sess.SessionID == id {
				return r.sess
			}
			key(m, "j")
		}
		t.Fatalf("no row %s", id)
		return nil
	}

	// Listed: the preview gives the reason; s does not open a box.
	sel("p4000000000000000")
	out = view(m)
	for _, want := range []string{"? UNKNOWN", "Level listed: Smif sees only the aider process", "state, no steps, no answers.", "Cannot write now: listed only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("listed preview lacks %q:\n%s", want, out)
		}
	}
	key(m, "s")
	if m.home.typing {
		t.Fatal("s opened the box on a listed session")
	}

	// herdr, unknown state: the same.
	sel("h3000000000000000")
	out = view(m)
	for _, want := range []string{"Level herdr: the state of gemini comes from", "Cannot write now: herdr cannot tell", "does not type"} {
		if !strings.Contains(out, want) {
			t.Fatalf("herdr preview lacks %q:\n%s", want, out)
		}
	}
	key(m, "s")
	if m.home.typing {
		t.Fatal("s opened the box at unknown state")
	}

	// herdr, blocked, in a folder of an agent: no fork is offered.
	waiting := sel("h2000000000000000")
	waiting.Agent = "cafe"
	if canWrite(waiting) {
		t.Fatal("a fork is offered at the herdr level")
	}
	out = view(m)
	if !strings.Contains(out, "o bring its tab to the front") {
		t.Fatalf("no way to the tab:\n%s", out)
	}

	// herdr, idle: s writes.
	sel("h1000000000000000")
	key(m, "s", "run the tests", "enter")
	if len(f.prompts) != 1 || f.prompts[0] != "h1000000000000000 run the tests" {
		t.Fatalf("prompts %q", f.prompts)
	}
}

// An unverified provider's row says so, keeps its step and answer, and the
// UI offers no writing, whatever its state.
func TestHomeUnverifiedSession(t *testing.T) {
	s := sessionSample()
	s.Sessions = []api.SessionView{
		{Harness: "codex", Level: "full", Unverified: true, SessionID: "c1000000000000000", Repo: "/r/api", Agent: "api", Turn: "idle", TurnAt: t0.Add(-time.Minute),
			TTY: "/dev/ttys010", Term: "tmux", Title: "payments", LastAnswer: "The test passes now.", LastAnswerAt: t0.Add(-time.Hour),
			Why: "codex is unverified, no real session checked it: read only"},
		{Harness: "qwen", Level: "full", Unverified: true, SessionID: "q2000000000000000", Repo: "/r/api", Agent: "api", Turn: "busy", TurnAt: t0.Add(-time.Minute),
			TTY: "/dev/ttys011", Term: "tmux", Title: "review",
			Why: "qwen is unverified, no real session checked it: read only"},
		{Harness: "codex", Level: "full", Unverified: true, SessionID: "c3000000000000000", Repo: "/r/api", Agent: "api", Turn: "unknown",
			TTY: "/dev/ttys012", Term: "tmux", Title: "codex", Why: "codex is unverified, no real session checked it: read only",
			Activity: &api.SessionActivity{Prompt: "fix the test", PromptAt: t0.Add(-time.Minute), Tools: 1,
				Now: &api.SessionStep{Tool: "shell", Target: "go test ./...", At: t0.Add(-time.Minute)}}},
	}
	for i := range s.Sessions {
		if canWrite(&s.Sessions[i]) {
			t.Fatalf("canWrite: %+v", s.Sessions[i])
		}
	}
	f := &fakePrompter{fakeHome: &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{s}}, threads: sampleThreads()},
	}}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	out := view(m)
	for _, want := range []string{"codex [unverified] The test pas", "qwen [unverified]", "? unknown codex", "[unverified] shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("home lacks %q:\n%s", want, out)
		}
	}
	golden(t, "home_session_unverified_120x40.golden", out)
	for range len(m.homeRows()) {
		key(m, "k")
	}
	for range len(m.homeRows()) {
		key(m, "j")
		if out := view(m); strings.Contains(out, "Cannot write now") {
			if !strings.Contains(out, "is unverified") || !strings.Contains(out, "read only") {
				t.Fatalf("no reason:\n%s", out)
			}
			return
		}
	}
	t.Fatal("no row says it cannot write")
}
