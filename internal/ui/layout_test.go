package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// layoutSizes are the terminal sizes every screen must fit: the classic
// 80x24 and a large 160x50.
var layoutSizes = [][2]int{{80, 24}, {160, 50}}

// newFullModel is a model whose source serves every screen: overview,
// thread, run, inbox and digest.
func newFullModel(t *testing.T, snap Snapshot, w, h int) (*Model, *fakeDigest) {
	t.Helper()
	_, insp := newInspModel(t)
	insp.snaps = []Snapshot{snap}
	f := &fakeDigest{fakeInspector: insp, cursor: 40,
		items: []api.DigestItem{
			{MessageID: "01JMSG0000000000000000DONE", ThreadID: "01JMSG0000000000000000DONE", Agent: "cafe", From: "web", Kind: "tell",
				Intent: "x.y", State: "done", Summary: "fake handled x.y", Coalesced: 2, At: t0.Add(-30 * time.Minute)},
			{MessageID: "01JMSG000000000000000NEED1", ThreadID: "01JMSG000000000000000NEED1", Agent: "cafe", From: "web", Kind: "ask",
				Intent: "store.release", State: "needs_human", Subject: "Ship?", HumanQuestion: "Ship 1.0.2 now or wait for review?",
				At: t0.Add(-4 * time.Minute)},
		},
		seqs: []int64{45, 57}}
	m := New(f, Options{Dashboard: true, Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	run(m, m.Init())
	return m, f
}

// emptySnapshot is a daemon with no agents, no messages and no events.
func emptySnapshot() Snapshot {
	s := sample()
	s.Status.Agents, s.Status.Running, s.Status.DLQ = nil, nil, 0
	s.Status.Metrics = api.Metrics{}
	s.NeedsYou, s.DLQ, s.Recent, s.Events = nil, nil, nil, nil
	return s
}

// downSnapshot is a poll that found no daemon.
func downSnapshot() Snapshot {
	return Snapshot{At: t0, Err: errors.New("smif daemon is not reachable at /tmp/mr-x/smif.sock: connection refused")}
}

// longSnapshot has long names and texts in every column, to force
// truncation.
func longSnapshot() Snapshot {
	s := sample()
	long := strings.Repeat("very-long-agent-name-", 3)
	s.Status.Agents = append(s.Status.Agents, api.AgentView{ID: long, Queued: 12345, SpentUSD: 1234.5, BudgetLeft: ptr(99999)})
	s.NeedsYou[0].HumanQuestion = strings.Repeat("Should the release go out tonight or after the review? ", 5)
	s.DLQ[0].Reason = strings.Repeat("worker exited with status 3 after the budget ran out; ", 4)
	for i := 0; i < 40; i++ {
		x := s.Recent[1]
		x.ID = fmt.Sprintf("01JMSG00000000000000LONG%02d", i)
		x.Summary = strings.Repeat("summary text ", 20)
		s.Recent = append(s.Recent, x)
	}
	return s
}

// screens drive a model to each screen of the UI.
var screens = []struct {
	name string
	snap func() Snapshot
	keys []string
	want []string // text the screen shows at every size
}{
	{"overview", sample, nil, []string{"▶ AGENTS (3)", "? help", "q quit"}},
	{"down", downSnapshot, nil, []string{"DAEMON DOWN", "Last error: smif daemon is not reachable", "Start it with `smif daemon`"}},
	{"overview_empty", emptySnapshot, nil, []string{"no agents in the config", "nothing is running", "nothing needs you",
		"no dead letters", "no messages yet", "no audit events yet"}},
	{"overview_long", longSnapshot, []string{"tab", "tab"}, []string{"▶ NEEDS YOU (1)", "…"}},
	{"help", sample, []string{"?"}, []string{"KEYS: HOME", "HELP · lines 1-", "q quit"}},
	{"needs_you_item", sample, []string{"tab", "tab", "enter"}, []string{"NEEDS YOU · 01JMSG000000000000000NEED1", "? help"}},
	{"reply_box", sample, []string{"tab", "tab", "enter", "a", "Ship", "space", "it"}, []string{"REPLY to cafe", "> Ship it"}},
	{"confirm_cancel", sample, []string{"tab", "tab", "c"}, []string{"Cancel 00000NEED1", "y yes"}},
	{"dead_letter_item", sample, []string{"tab", "tab", "tab", "enter"}, []string{"DEAD LETTER · 01JMSG0000000000000000DLQ1", "t retry"}},
	{"run_record", sample, []string{"tab", "enter"}, []string{"RUN · " + runID, "[1 RECORD]"}},
	{"run_transcript", sample, []string{"tab", "enter", "2"}, []string{"[2 TRANSCRIPT]", "result success"}},
	{"run_diff", sample, []string{"tab", "enter", "3"}, []string{"[3 DIFF]", "+new line"}},
	{"confirm_apply", sample, []string{"tab", "enter", "a"}, []string{"Apply run 00AAAAAAAA? [y/N]"}},
	{"inbox", sample, []string{"i"}, []string{"INBOX · agent: all · state: all", "> 00000NEED1"}},
	{"inbox_empty", sample, []string{"i", "s"}, []string{"No messages match the filter (agent: all, state: accepted)."}},
	{"digest", sample, []string{"d"}, []string{"DIGEST · 2 unread handled items", "m marks"}},
	{"digest_empty", sample, []string{"d", "m", "m"}, []string{"Nothing new since the read cursor."}},
}

// TestLayoutGoldens renders every screen at 80x24 and 160x50. Each view
// has exactly h lines, no line is wider than w, and a cut line ends in
// "…". The goldens show the text without styles (go test -update).
func TestLayoutGoldens(t *testing.T) {
	for _, sc := range screens {
		for _, sz := range layoutSizes {
			w, h := sz[0], sz[1]
			t.Run(fmt.Sprintf("%s_%dx%d", sc.name, w, h), func(t *testing.T) {
				m, _ := newFullModel(t, sc.snap(), w, h)
				for _, k := range sc.keys {
					key(m, k)
				}
				out := view(m)
				checkSize(t, out, w, h)
				for _, want := range sc.want {
					if !strings.Contains(out, want) {
						t.Errorf("view lacks %q\n%s", want, out)
					}
				}
				golden(t, fmt.Sprintf("layout/%s_%dx%d.golden", sc.name, w, h), out)
			})
		}
	}
}

// TestLayoutCutsWithEllipsis checks that a line cut at the right edge ends
// in "…" and that the footer always keeps "? help" and "q quit".
func TestLayoutCutsWithEllipsis(t *testing.T) {
	m, _ := newFullModel(t, longSnapshot(), 80, 24)
	key(m, "tab", "tab")
	out := view(m)
	cut := 0
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) == 80 && strings.HasSuffix(l, "…") {
			cut++
		}
	}
	if cut == 0 {
		t.Fatalf("no line ends in an ellipsis at 80 columns:\n%s", out)
	}
	key(m, "h", "h", "G") // AGENTS, last row: the long name
	out = view(m)
	found := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "> very-long-agent-") && strings.Contains(l, "…  ") {
			found = true
		}
	}
	if !found {
		t.Errorf("a long agent name is not cut with an ellipsis:\n%s", out)
	}
	for _, sz := range []int{40, 60, 80} {
		m.Update(tea.WindowSizeMsg{Width: sz, Height: 24})
		lines := strings.Split(view(m), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, "? help") || !strings.Contains(last, "q quit") {
			t.Errorf("footer at %d columns lacks ? help or q quit: %q", sz, last)
		}
	}
}

// TestHelpListsEveryKey checks that the help screen names every key the
// key handlers act on.
func TestHelpListsEveryKey(t *testing.T) {
	m, _ := newFullModel(t, sample(), 160, 200)
	key(m, "?")
	out := view(m)
	for _, k := range []string{
		"tab", "shift+tab", "↑/k", "↓/j", "pgup", "pgdown", "g", "G", "r", "enter", "i", "f/F", "s/S", "x", "d", "m", "a", "c", "t",
		"1", "2", "3", "esc", "backspace", "?", "q", "ctrl+c", "y",
		"alt+enter", "ctrl+u", "ctrl+k", "ctrl+w", "ctrl+a", "ctrl+e",
	} {
		if !strings.Contains(out, " "+k) {
			t.Errorf("help lacks the key %q", k)
		}
	}
}

// TestHelpScrolls checks that a help screen taller than the terminal
// scrolls and that q still quits.
func TestHelpScrolls(t *testing.T) {
	m, _ := newFullModel(t, sample(), 80, 24)
	key(m, "?")
	first := view(m)
	key(m, "j", "j")
	second := view(m)
	if first == second || !strings.Contains(second, "lines 3-") {
		t.Fatalf("help did not scroll:\n%s", second)
	}
	key(m, "G")
	if !strings.Contains(view(m), "with the human token only.") {
		t.Fatalf("G did not reach the end of the help:\n%s", view(m))
	}
	key(m, "esc")
	if strings.Contains(view(m), "KEYS") {
		t.Fatal("esc did not close the help")
	}
	key(m, "?")
	_, c := m.Update(keyText("q", false))
	if c == nil {
		t.Fatal("q in the help returned no command")
	}
	if _, ok := c().(tea.QuitMsg); !ok {
		t.Fatal("q in the help does not quit")
	}
}
