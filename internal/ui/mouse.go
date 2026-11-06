package ui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A terminal selects whole lines of the screen, so two panes side by side
// copy together. smif takes the mouse: a drag selects inside the pane where it
// starts, and the release copies. SMIF_NO_MOUSE=1 gives the mouse back.

// selection is a drag in progress. Cells: left <= x < right, within a pane.
type selection struct {
	left, right int
	fromX, y0   int
	toX, y1     int
	moved       bool
}

func mouseOn() bool { return os.Getenv("SMIF_NO_MOUSE") == "" }

// pane is the text columns of the pane at column x: inside its frame on the
// start screen, the whole width elsewhere.
func (m *Model) pane(x int) (left, right int) {
	w := max(m.width, 1)
	if !m.homeActive() {
		return 0, w
	}
	f := m.home
	if w < 90 || f.full || f.part.open || f.part.check {
		return 2, max(3, w-2)
	}
	listW := max(44, w/2)
	if x < listW {
		return 2, listW - 2
	}
	return listW + 3, max(listW+4, w-2)
}

func (m *Model) mouseKey(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		e := msg.Mouse()
		m.sel = nil
		if e.Button == tea.MouseLeft {
			left, right := m.pane(e.X)
			x := min(max(e.X, left), right-1)
			m.sel = &selection{left: left, right: right, fromX: x, y0: e.Y, toX: x, y1: e.Y}
		}
	case tea.MouseMotionMsg:
		if s, e := m.sel, msg.Mouse(); s != nil {
			s.toX, s.y1, s.moved = min(max(e.X, s.left), s.right-1), e.Y, true
		}
	case tea.MouseReleaseMsg:
		s := m.sel
		m.sel = nil
		if s != nil && s.moved {
			return m.copySelection(s)
		}
	case tea.MouseWheelMsg:
		// A key press would answer a yes/no question or leave the prompt.
		if m.confirm != nil || m.prompt != nil {
			return nil
		}
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			return m.keyMsg(tea.KeyPressMsg{Code: tea.KeyUp})
		case tea.MouseWheelDown:
			return m.keyMsg(tea.KeyPressMsg{Code: tea.KeyDown})
		}
	}
	return nil
}

// span puts a selection's ends in reading order.
func (s *selection) span() (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = s.fromX, s.y0, s.toX, s.y1
	if y1 < y0 || y1 == y0 && x1 < x0 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	return x0, y0, x1, y1
}

// cells gives the columns of one row of the selection.
func (s *selection) cells(y int) (from, to int, ok bool) {
	x0, y0, x1, y1 := s.span()
	if y < y0 || y > y1 {
		return 0, 0, false
	}
	from, to = s.left, s.right
	if y == y0 {
		from = x0
	}
	if y == y1 {
		to = x1 + 1
	}
	return from, to, from < to
}

// highlight shows the selection on a drawn screen.
func (m *Model) highlight(screen string) string {
	s := m.sel
	if s == nil || !s.moved {
		return screen
	}
	lines := strings.Split(screen, "\n")
	for y := range lines {
		if from, to, ok := s.cells(y); ok {
			l := lines[y]
			lines[y] = ansi.Cut(l, 0, from) + stCursor.Render(ansi.Strip(ansi.Cut(l, from, to))) + ansi.Cut(l, to, ansi.StringWidth(l))
		}
	}
	return strings.Join(lines, "\n")
}

// selectedText is the selection as text. A line that the frame wrapped joins
// the next one again.
func (m *Model) selectedText(s *selection) string {
	return s.text(strings.Split(ansi.Strip(m.Render()), "\n"))
}

func (s *selection) text(screen []string) string {
	var out []string
	wrapped := false
	for y := range screen {
		from, to, ok := s.cells(y)
		if !ok {
			continue
		}
		l := strings.TrimRight(ansi.Cut(screen[y], from, to), " ")
		if wrapped && l != "" && l[0] != ' ' {
			out[len(out)-1] += " " + l
		} else {
			out = append(out, l)
		}
		wrapped = false
		if y+1 < len(screen) {
			word, _, _ := strings.Cut(strings.TrimSpace(ansi.Cut(screen[y+1], s.left, s.right)), " ")
			wrapped = word != "" && ansi.StringWidth(strings.TrimRight(ansi.Cut(screen[y], s.left, s.right), " "))+1+ansi.StringWidth(word) > s.right-s.left
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n ")
}

func (m *Model) copySelection(s *selection) tea.Cmd {
	text := m.selectedText(s)
	if text == "" {
		return nil
	}
	return m.copied(text, fmt.Sprintf("Copied %s.", countLines(text)))
}

// copied writes text to the clipboard and reports status when it is done.
func (m *Model) copied(text, status string) tea.Cmd {
	clip := m.clip
	return func() tea.Msg { return copiedMsg{status: status, err: clip(text)} }
}

func countLines(s string) string {
	if n := strings.Count(s, "\n") + 1; n != 1 {
		return fmt.Sprintf("%d lines", n)
	}
	return "1 line"
}

// copyAnswer copies the session's newest answer, whole.
func (m *Model) copyAnswer(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		m.setStatus("No answer to copy yet.", false)
		return nil
	}
	return m.copied(text, fmt.Sprintf("Copied the answer, %s.", countLines(text)))
}
