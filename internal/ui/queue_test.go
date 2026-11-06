package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
)

func queueSample() Snapshot {
	s := sessionSample()
	s.Sessions[0].Queue = []api.QueuedPrompt{
		{ID: 7, Text: "run the tests and report", CreatedAt: t0.Add(-3 * time.Minute)},
		{ID: 8, Text: "then update the changelog", CreatedAt: t0.Add(-time.Minute), Error: "process 42 is gone"},
	}
	return s
}

func queueModel(t *testing.T) (*Model, *fakePrompter) {
	t.Helper()
	f := &fakePrompter{fakeHome: &fakeHome{fakeDigest: &fakeDigest{fakeInspector: &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{queueSample()}}, threads: sampleThreads()},
	}}}}
	m := New(f, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	return m, f
}

// The row's mark and the list carry their meaning in text, so NO_COLOR
// keeps it. x on the row opens the chat with the cursor on the items.
func TestQueueRowChatAndRemove(t *testing.T) {
	m, f := queueModel(t)
	key(m, "j", "j") // the busy session
	out := view(m)
	for _, want := range []string{"⧖ 2 queued", "run the tests and report", "not typed yet: process 42 is gone", "x removes", "x remove queued"} {
		if !strings.Contains(out, want) {
			t.Fatalf("home lacks %q:\n%s", want, out)
		}
	}
	key(m, "x")
	out = view(m)
	if !m.home.full || !strings.Contains(out, "▶ 3m · run the tests and report") || !strings.Contains(out, "x remove · esc back") {
		t.Fatalf("x did not open the list:\n%s", out)
	}
	key(m, "down", "x")
	if len(f.prompts) != 1 || f.prompts[0] != "cancel 8" {
		t.Fatalf("removed %q", f.prompts)
	}
	key(m, "esc")
	if m.home.queuePick || !m.home.full {
		t.Fatal("esc did not go back to the text")
	}
	// In the box, x is a letter; ctrl+q moves to the list.
	key(m, "x")
	if m.home.queuePick || m.home.draft.Value() != "x" {
		t.Fatalf("x in the box: %q", m.home.draft.Value())
	}
	key(m, "ctrl+q")
	if !m.home.queuePick || m.home.queueSel != 1 {
		t.Fatal("ctrl+q did not move to the newest item")
	}
	key(m, "ctrl+q")
	if m.home.queuePick {
		t.Fatal("ctrl+q did not leave the list")
	}
	// The list closes when its last item went.
	m.snap.Sessions[0].Queue = nil
	key(m, "ctrl+q")
	if m.home.queuePick || !strings.Contains(m.Status(), "Nothing is queued") {
		t.Fatalf("empty queue: %v %q", m.home.queuePick, m.Status())
	}
}

// Enter queues on a busy or waiting session; ctrl+f asks a fork. On an idle
// session ctrl+f stays the text cursor key, and enter types.
func TestQueueEnterAndFork(t *testing.T) {
	m, f := newSessionModel(t)
	key(m, "j", "j", "enter") // the busy session
	key(m, "ctrl+f")
	if len(f.prompts) != 0 || !strings.Contains(m.Status(), "The question is empty") {
		t.Fatalf("empty fork: %q %q", f.prompts, m.Status())
	}
	key(m, "what now?", "ctrl+f")
	if len(f.prompts) != 1 || f.prompts[0] != "busy-session-0001 what now?" {
		t.Fatalf("ctrl+f sent %q", f.prompts)
	}
	key(m, "go on", "enter")
	if len(f.prompts) != 2 || f.prompts[1] != "queue busy-session-0001 go on" {
		t.Fatalf("enter sent %q", f.prompts)
	}

	m.snap.Sessions[0].Turn = "waiting"
	key(m, "yes", "enter")
	if len(f.prompts) != 3 || f.prompts[2] != "queue busy-session-0001 yes" {
		t.Fatalf("enter on a waiting session sent %q", f.prompts)
	}

	// No agent owns the folder: the queue works, a fork cannot.
	m.snap.Sessions[0].Agent = ""
	key(m, "x", "ctrl+f")
	if len(f.prompts) != 3 || !strings.Contains(m.Status(), "No fork can answer") {
		t.Fatalf("fork with no agent: %q %q", f.prompts, m.Status())
	}
	key(m, "esc", "esc")

	key(m, "k", "enter", "backspace", "ab", "left", "left", "ctrl+f", "x", "enter") // the idle session
	if n := len(f.prompts); f.prompts[n-1] != "idle-session-0001 axb" {
		t.Fatalf("idle session: %q", f.prompts)
	}
}

// An unverified or listed session takes no queue: smif may never type there.
func TestQueueRefusesReadOnly(t *testing.T) {
	for _, sv := range []api.SessionView{
		{Turn: "busy", Unverified: true, Why: "read only"},
		{Turn: "unknown", Level: "listed", Why: "listed only"},
		{Turn: "unknown", Why: "no turn seen yet"},
	} {
		if canQueue(&sv) || canWrite(&sv) {
			t.Fatalf("queue offered: %+v", sv)
		}
	}
}

// The queue fits both layout sizes, on the home screen and in the chat.
func TestQueueLayout(t *testing.T) {
	for _, sz := range layoutSizes {
		w, h := sz[0], sz[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m, _ := queueModel(t)
			m.Update(tea.WindowSizeMsg{Width: w, Height: h})
			key(m, "j", "j")
			out := view(m)
			checkSize(t, out, w, h)
			golden(t, fmt.Sprintf("layout/home_queue_%dx%d.golden", w, h), out)
			key(m, "x", "down")
			chat := view(m)
			checkSize(t, chat, w, h)
			if !strings.Contains(chat, "▶ 1m · then update the changelog") {
				t.Fatalf("no cursor on the item:\n%s", chat)
			}
			golden(t, fmt.Sprintf("layout/chat_queue_%dx%d.golden", w, h), chat)
		})
	}
}
