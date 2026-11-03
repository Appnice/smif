package ui

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 11, 2, 13, 10, 5, 0, time.Local)

// fakeSource returns queued snapshots, then repeats the last one.
type fakeSource struct {
	snaps []Snapshot
	calls int
}

func (f *fakeSource) Fetch(ctx context.Context) Snapshot {
	i := min(f.calls, len(f.snaps)-1)
	f.calls++
	return f.snaps[i]
}

func ptr(f float64) *float64 { return &f }

func sample() Snapshot {
	return Snapshot{
		At: t0,
		Status: api.StatusView{
			Daemon: api.HealthView{OK: true, Version: "v0.1.0", SchemaVersion: 4, ClaudeVersion: "2.1.316", ClaudeOK: true,
				PID: 4242, StartedAt: t0.Add(-2*time.Hour - 3*time.Minute)},
			Agents: []api.AgentView{
				{ID: "cafe", Queued: 2, Running: 1, NeedsHuman: 1, DeadLetters: 0, BudgetUSD: 3, SpentUSD: 0.25, BudgetLeft: ptr(2.75), Interactive: true},
				{ID: "observability", Queued: 0, Running: 0, NeedsHuman: 0, DeadLetters: 1, SpentUSD: 0},
				{ID: "web", Queued: 0, Running: 0, NeedsHuman: 0, DeadLetters: 0, BudgetUSD: 1, SpentUSD: 1.5, BudgetLeft: ptr(-0.5)},
			},
			Running: []api.RunView{{ID: "01JRUN000000000000AAAAAAAA", MessageID: "01JMSG0000000000000000RUN1", Agent: "cafe",
				Handler: "worker", Profile: "triage", Attempt: 1, State: "running", StartedAt: t0.Add(-65 * time.Second)}},
			DLQ:     1,
			Metrics: api.Metrics{QueueDepth: 2, Running: 1, SuccessRate: 0.8, P50Seconds: 1.2, P95Seconds: 3, RunsLast24h: 5},
		},
		NeedsYou: []api.MessageView{{ID: "01JMSG000000000000000NEED1", To: "cafe", From: envelope.From{Agent: "web"}, Kind: "ask",
			Intent: "store.release", State: "needs_human", Subject: "Ship?", HumanQuestion: "Ship 1.0.2 now or wait for review?",
			UpdatedAt: t0.Add(-4 * time.Minute)}},
		DLQ: []api.MessageView{{ID: "01JMSG0000000000000000DLQ1", To: "observability", From: envelope.From{Agent: "cafe"}, Kind: "tell",
			Intent: "crash.report", State: "dead_letter", Reason: "timeout after 2 attempts", UpdatedAt: t0.Add(-10 * time.Minute)}},
		Recent: []api.MessageView{
			{ID: "01JMSG000000000000000NEED1", From: envelope.From{Agent: "web"}, To: "cafe", Kind: "ask", Intent: "store.release",
				State: "needs_human", Subject: "Ship?", CreatedAt: t0.Add(-5 * time.Minute)},
			{ID: "01JMSG0000000000000000DONE", From: envelope.From{Agent: "web"}, To: "cafe", Kind: "tell", Intent: "x.y",
				State: "done", Subject: "hello", Summary: "fake handled x.y", Coalesced: 2, CreatedAt: t0.Add(-30 * time.Minute)},
		},
		Events: sampleEvents(),
	}
}

// sampleEvents are three audit events, oldest first.
func sampleEvents() []api.AuditEvent {
	return []api.AuditEvent{
		{Seq: 40, TS: t0.Add(-5 * time.Minute), Event: "message.accepted", Agent: "cafe", MessageID: "01JMSG000000000000000NEED1",
			ThreadID: "01JMSG000000000000000NEED1", Detail: map[string]any{"from": "web", "kind": "ask", "intent": "store.release",
				"origin": "interactive", "hop": float64(0), "state": "accepted"}},
		{Seq: 41, TS: t0.Add(-4 * time.Minute), Event: "message.transition", Agent: "cafe", MessageID: "01JMSG000000000000000NEED1",
			Detail: map[string]any{"from_state": "running", "to_state": "needs_human", "human_question": "Ship 1.0.2 now or wait for review?"}},
		{Seq: 42, TS: t0.Add(-65 * time.Second), Event: "run.started", Agent: "cafe", MessageID: "01JMSG0000000000000000RUN1",
			RunID: "01JRUN000000000000AAAAAAAA", Detail: map[string]any{"handler": "worker", "profile": "triage", "attempt": float64(1)}},
	}
}

func newModel(src Source, w, h int) *Model {
	m := New(src, Options{Dashboard: true, Now: func() time.Time { return t0 }})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// poll runs one fetch command and feeds its result back.
func poll(t *testing.T, m *Model) {
	t.Helper()
	var cmd tea.Cmd
	if !m.fetching {
		cmd = m.Init()
	} else {
		cmd = m.fetch()
	}
	msg := cmd()
	if _, ok := msg.(SnapshotMsg); !ok {
		t.Fatalf("fetch returned %T", msg)
	}
	_, next := m.Update(msg)
	if next == nil {
		t.Fatal("no tick scheduled after a poll")
	}
}

func view(m *Model) string { return ansi.Strip(m.Render()) }

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestOverviewView(t *testing.T) {
	m := newModel(&fakeSource{snaps: []Snapshot{sample()}}, 120, 40)
	poll(t, m)
	out := view(m)
	for _, want := range []string{
		"daemon v0.1.0 pid 4242 up 2h03m", "claude 2.1.316 ok", "updated 13:10:05",
		"queue 2 · running 1 · needs you 1 · dead letters 1 · 24h: 5 runs, 80% ok",
		"AGENTS (3)", "RUNNING (1)", "NEEDS YOU (1)", "DEAD LETTERS (1)", "RECENT ACTIVITY (2)",
		"AGENT          QUEUED  RUNNING  NEEDS YOU  DEAD  SPENT TODAY  BUDGET LEFT  SESSION",
		"> cafe           2       1        1          0     $0.25        $2.75        interactive",
		"  observability  0       0        0          1     $0.00        -            -",
		"-$0.50", "1m05s", "worker/triage", "Ship 1.0.2 now or wait for review?", "timeout after 2 attempts",
		"fake handled x.y (+2 coalesced)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q\n%s", want, out)
		}
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 40 {
		t.Errorf("view has %d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > 120 {
			t.Errorf("line %d is %d cells wide", i, ansi.StringWidth(l))
		}
	}
	golden(t, "overview_120x40.golden", out)
}

func TestDaemonDownThenRecovers(t *testing.T) {
	down := Snapshot{At: t0, Err: errors.New("smif daemon is not reachable at /tmp/x.sock: connection refused")}
	src := &fakeSource{snaps: []Snapshot{down, sample(), down}}
	m := newModel(src, 120, 40)
	poll(t, m)
	out := view(m)
	if !m.Down() || !strings.Contains(out, "DAEMON DOWN") || !strings.Contains(out, "connection refused") ||
		!strings.Contains(out, "no data yet") || !strings.Contains(out, "Start it with `smif daemon`") {
		t.Fatalf("down view:\n%s", out)
	}
	golden(t, "down_120x40.golden", out)

	poll(t, m)
	if out := view(m); m.Down() || strings.Contains(out, "DAEMON DOWN") || !strings.Contains(out, "AGENTS (3)") {
		t.Fatalf("recovered view:\n%s", out)
	}
	// Down again: the last data stays on screen, marked stale.
	poll(t, m)
	out = view(m)
	if !strings.Contains(out, "DAEMON DOWN") || !strings.Contains(out, "showing stale data from 13:10:05") || !strings.Contains(out, "AGENTS (3)") {
		t.Fatalf("stale view:\n%s", out)
	}
}

func TestKeys(t *testing.T) {
	m := newModel(&fakeSource{snaps: []Snapshot{sample()}}, 120, 40)
	poll(t, m)
	press := func(k tea.KeyPressMsg) tea.Cmd { _, c := m.Update(k); return c }
	if m.Focus() != PanelAgents || m.Selected() != "cafe" {
		t.Fatalf("start: %v %q", m.Focus(), m.Selected())
	}
	press(tea.KeyPressMsg{Code: tea.KeyDown})
	press(keyText("j", false))
	if m.Selected() != "web" {
		t.Fatalf("after two downs: %q", m.Selected())
	}
	press(tea.KeyPressMsg{Code: tea.KeyDown}) // clamps at the last row
	if m.Selected() != "web" {
		t.Fatalf("cursor left the table: %q", m.Selected())
	}
	if !strings.Contains(view(m), "> web") {
		t.Fatalf("cursor not drawn:\n%s", view(m))
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Focus() != PanelRunning || m.Selected() != "01JRUN000000000000AAAAAAAA" {
		t.Fatalf("tab: %v %q", m.Focus(), m.Selected())
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	press(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.Focus() != PanelDLQ || m.Selected() != "01JMSG0000000000000000DLQ1" {
		t.Fatalf("tab, right: %v %q", m.Focus(), m.Selected())
	}
	press(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.Focus() != PanelNeedsYou {
		t.Fatalf("shift+tab: %v", m.Focus())
	}
	press(tea.KeyPressMsg{Code: tea.KeyLeft})
	press(tea.KeyPressMsg{Code: tea.KeyLeft})
	press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.Focus() != PanelEvents {
		t.Fatalf("left wraps around: %v", m.Focus())
	}
	press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.Focus() != PanelRecent {
		t.Fatalf("left from EVENTS: %v", m.Focus())
	}
	press(keyText("?", false))
	if out := view(m); !strings.Contains(out, "KEYS") || strings.Contains(out, "AGENTS (3)") {
		t.Fatalf("help:\n%s", out)
	}
	press(tea.KeyPressMsg{Code: tea.KeyEsc})
	if out := view(m); strings.Contains(out, "KEYS") {
		t.Fatalf("help did not close:\n%s", out)
	}
	if c := press(keyText("r", false)); c == nil {
		t.Fatal("r did not start a refresh")
	}
	if c := press(keyText("r", false)); c != nil {
		t.Fatal("r started a second fetch while one is in flight")
	}
	c := press(keyText("q", false))
	if c == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := c().(tea.QuitMsg); !ok {
		t.Fatal("q does not quit")
	}
	if _, ok := press(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c does not quit")
	}
}

func TestTickPollsOnlyWhenIdle(t *testing.T) {
	src := &fakeSource{snaps: []Snapshot{sample()}}
	m := newModel(src, 120, 40)
	first := m.Init()
	if _, c := m.Update(tickMsg(t0)); c != nil {
		t.Fatal("tick started a fetch while the first one is in flight")
	}
	_, next := m.Update(first())
	if next == nil {
		t.Fatal("no tick after a snapshot")
	}
	_, c := m.Update(tickMsg(t0))
	if c == nil {
		t.Fatal("tick did not fetch when idle")
	}
	if _, ok := c().(SnapshotMsg); !ok || src.calls != 2 {
		t.Fatalf("fetch calls = %d", src.calls)
	}
}

func TestSmallTerminalFits(t *testing.T) {
	s := sample()
	for i := 0; i < 30; i++ {
		s.Recent = append(s.Recent, s.Recent[1])
	}
	m := newModel(&fakeSource{snaps: []Snapshot{s}}, 80, 24)
	poll(t, m)
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // focus EVENTS
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // focus RECENT ACTIVITY
	m.Update(keyText("G", false))
	out := view(m)
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("%d lines, want 24\n%s", len(lines), out)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > 80 {
			t.Errorf("line %d is %d cells wide: %q", i, ansi.StringWidth(l), l)
		}
	}
	if !strings.Contains(out, "RECENT ACTIVITY (32)  rows 31-32") || !strings.Contains(out, "EVENTS (3)") || !strings.Contains(lines[23], "? help") {
		t.Fatalf("small view:\n%s", out)
	}
	if !strings.Contains(out, "> 000000DONE") {
		t.Fatalf("cursor on the last row is not visible:\n%s", out)
	}
}

// fakeCaller fails every call after fail is set.
type fakeCaller struct {
	fail   bool
	closed bool
}

func (c *fakeCaller) Call(method string, params, out any, timeout time.Duration) error {
	if c.fail {
		return errors.New("daemon closed the connection")
	}
	if s, ok := out.(*api.StatusView); ok {
		s.Agents = []api.AgentView{{ID: "cafe"}}
	}
	return nil
}

func (c *fakeCaller) Close() error { c.closed = true; return nil }

func TestDaemonSourceRedials(t *testing.T) {
	var dials int
	var cur *fakeCaller
	src := &DaemonSource{Dial: func() (Caller, error) {
		dials++
		if dials == 2 {
			return nil, errors.New("not reachable")
		}
		cur = &fakeCaller{}
		return cur, nil
	}}
	if s := src.Fetch(context.Background()); s.Err != nil || len(s.Status.Agents) != 1 {
		t.Fatalf("first fetch: %+v", s)
	}
	cur.fail = true
	// The reused connection fails, the one fresh dial fails too: down.
	if s := src.Fetch(context.Background()); s.Err == nil || !cur.closed || dials != 2 || s.Err.Error() != "not reachable" {
		t.Fatalf("down not reported: %+v dials=%d", s, dials)
	}
	if s := src.Fetch(context.Background()); s.Err != nil || dials != 3 {
		t.Fatalf("redial: %+v dials=%d", s, dials)
	}
	// A dropped connection with a live daemon is invisible: one redial.
	cur.fail = true
	if s := src.Fetch(context.Background()); s.Err != nil || dials != 4 {
		t.Fatalf("reused connection not redialed: %+v dials=%d", s, dials)
	}
}

// The real Bubble Tea loop: it renders and quits on "q" from the input.
func TestProgramQuitsOnQ(t *testing.T) {
	down := Snapshot{At: t0, Err: errors.New("not reachable")}
	pr, pw := io.Pipe()
	var out bytes.Buffer
	p := tea.NewProgram(New(&fakeSource{snaps: []Snapshot{down}}, Options{Interval: 10 * time.Millisecond}),
		tea.WithInput(pr), tea.WithOutput(&out), tea.WithoutSignalHandler(), tea.WithWindowSize(120, 40))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(200 * time.Millisecond)
	pw.Write([]byte("q"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("q did not quit the program")
	}
	if !strings.Contains(ansi.Strip(out.String()), "DAEMON DOWN") {
		t.Fatalf("program output lacks DAEMON DOWN:\n%q", out.String())
	}
}
