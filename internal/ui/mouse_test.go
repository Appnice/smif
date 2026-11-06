package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func mouseModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	snap := sessionSample()
	for i := range snap.Sessions {
		snap.Sessions[i].LastAnswer = "line one\nline two"
	}
	m := New(&fakeSource{snaps: []Snapshot{snap}}, Options{Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	run(m, m.Init())
	got := &[]string{}
	m.clip = func(s string) error { *got = append(*got, s); return nil }
	for r := m.homeSelected(); r == nil || r.sess == nil; r = m.homeSelected() {
		key(m, "j")
	}
	return m, got
}

func drag(m *Model, x0, y0, x1, y1 int) {
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	run(m, cmd)
}

// find gives the row and column of a word on the plain screen.
func find(t *testing.T, m *Model, word string) (x, y int) {
	t.Helper()
	for y, l := range strings.Split(ansi.Strip(m.Render()), "\n") {
		if i := strings.Index(l, word); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("no %q on the screen:\n%s", word, ansi.Strip(m.Render()))
	return 0, 0
}

// The reported fault: text on the right must not bring the list along.
func TestDragStaysInsideOnePane(t *testing.T) {
	m, got := mouseModel(t)
	x, y := find(t, m, "IDLE")
	_, y2 := find(t, m, "bump the version")
	drag(m, x, y, 118, y2)
	if len(*got) != 1 || !strings.Contains((*got)[0], "IDLE") || !strings.Contains((*got)[0], "bump the version") {
		t.Fatalf("copied %q", *got)
	}
	for _, list := range []string{"NEEDS YOU", "Ship?", "SESSIONS", "│"} {
		if strings.Contains((*got)[0], list) {
			t.Fatalf("the list came along: %q", *got)
		}
	}
	if s := m.Status(); !strings.Contains(s, "Copied") {
		t.Fatalf("status %q", s)
	}
}

func TestDragBackwardsAndClick(t *testing.T) {
	m, got := mouseModel(t)
	x, y := find(t, m, "you asked")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	run(m, cmd)
	if len(*got) != 0 {
		t.Fatalf("a click copied %q", *got)
	}
	drag(m, x+8, y, x, y)
	if len(*got) != 1 || (*got)[0] != "you asked" {
		t.Fatalf("copied %q", *got)
	}
}

// Text shows while a drag runs, also with NO_COLOR: reverse video stays.
func TestDragShowsAndDrawsTheSameWidth(t *testing.T) {
	m, _ := mouseModel(t)
	x, y := find(t, m, "you asked")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
	plain := m.Render()
	if ansi.Strip(m.highlight(plain)) != ansi.Strip(plain) {
		t.Fatal("the selection changed the text on screen")
	}
}

// The wheel is a key press, so it must never answer a yes/no question.
func TestWheelLeavesAQuestionAlone(t *testing.T) {
	m, _ := mouseModel(t)
	m.confirm = &confirmation{what: "x", yes: func() tea.Cmd { return nil }}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.confirm == nil {
		t.Fatal("the wheel answered the question")
	}
}

func TestCopyWholeAnswer(t *testing.T) {
	m, got := mouseModel(t)
	key(m, "Y")
	if len(*got) != 1 || !strings.Contains((*got)[0], "line two") {
		t.Fatalf("copied %q", *got)
	}
}

func TestWrappedLineJoinsAgain(t *testing.T) {
	s := &selection{left: 0, right: 12, fromX: 0, y0: 0, toX: 11, y1: 3}
	got := s.text([]string{"one two tree", "four five", "", "  code line"})
	if got != "one two tree four five\n\n  code line" {
		t.Fatalf("%q", got)
	}
}
