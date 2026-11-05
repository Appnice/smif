package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// lineInput is a small one-line text editor for the reply box. A paste
// may carry newlines; alt+enter inserts one. The view shows them as ↵.
type lineInput struct {
	r   []rune
	pos int
}

// Value is the text typed so far.
func (in *lineInput) Value() string { return string(in.r) }

func (in *lineInput) insert(rs []rune) {
	var clean []rune
	for _, c := range rs {
		switch {
		case c == '\r':
			c = '\n'
		case c == '\t':
			c = ' '
		case c < 0x20 && c != '\n':
			continue
		}
		clean = append(clean, c)
	}
	in.r = append(in.r[:in.pos], append(clean, in.r[in.pos:]...)...)
	in.pos += len(clean)
}

// remove takes the first s out of the text and keeps the cursor on the same
// character. It reports whether s was there.
func (in *lineInput) remove(s string) bool {
	rs := []rune(s)
	for i := 0; i+len(rs) <= len(in.r); i++ {
		if string(in.r[i:i+len(rs)]) != s {
			continue
		}
		in.r = append(in.r[:i], in.r[i+len(rs):]...)
		if in.pos >= i+len(rs) {
			in.pos -= len(rs)
		} else if in.pos > i {
			in.pos = i
		}
		return true
	}
	return false
}

// edit applies one key. It reports false for keys it does not handle
// (enter, esc), which the caller acts on.
func (in *lineInput) edit(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "backspace":
		if in.pos > 0 {
			in.r = append(in.r[:in.pos-1], in.r[in.pos:]...)
			in.pos--
		}
	case "delete", "ctrl+d":
		if in.pos < len(in.r) {
			in.r = append(in.r[:in.pos], in.r[in.pos+1:]...)
		}
	case "left", "ctrl+b":
		in.pos = max(0, in.pos-1)
	case "right", "ctrl+f":
		in.pos = min(len(in.r), in.pos+1)
	case "home", "ctrl+a":
		in.pos = 0
	case "end", "ctrl+e":
		in.pos = len(in.r)
	case "ctrl+u":
		in.r = in.r[in.pos:]
		in.pos = 0
	case "ctrl+k":
		in.r = in.r[:in.pos]
	case "ctrl+w":
		i := in.pos
		for i > 0 && in.r[i-1] == ' ' {
			i--
		}
		for i > 0 && in.r[i-1] != ' ' {
			i--
		}
		in.r = append(in.r[:i], in.r[in.pos:]...)
		in.pos = i
	case "alt+enter", "shift+enter", "ctrl+j":
		in.insert([]rune{'\n'})
	case "enter", "esc":
		return false
	default:
		// Printable keys carry their text (shift and caps lock applied).
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return false
		}
		in.insert([]rune(k.Text))
	}
	return true
}

// view renders the text in w cells with the cursor drawn reversed. When
// the text is wider, it shows the part around the cursor.
func (in *lineInput) view(w int) string {
	show := func(c rune) string {
		if c == '\n' {
			return "↵"
		}
		return string(c)
	}
	cells := make([]string, 0, len(in.r)+1)
	for _, c := range in.r {
		cells = append(cells, show(c))
	}
	cells = append(cells, " ")
	start := 0
	width := func(a, b int) int { return ansi.StringWidth(strings.Join(cells[a:b], "")) }
	for start < in.pos && width(start, in.pos+1) > w {
		start++
	}
	var b strings.Builder
	used := 0
	for i := start; i < len(cells); i++ {
		cw := ansi.StringWidth(cells[i])
		if used+cw > w {
			break
		}
		used += cw
		if i == in.pos {
			b.WriteString(stCursor.Render(cells[i]))
		} else {
			b.WriteString(cells[i])
		}
	}
	return b.String()
}

// wrapView renders the text over lines w cells wide, broken at spaces where
// it can (a new line in the text starts a line), with the cursor drawn
// reversed. With more than maxLines lines it shows the maxLines around
// the cursor.
func (in *lineInput) wrapView(w, maxLines int) []string {
	w = max(1, w)
	type cell struct {
		s   string
		cur bool
		nl  bool
	}
	cells := make([]cell, 0, len(in.r)+1)
	for i, c := range in.r {
		if c == '\n' {
			cells = append(cells, cell{s: " ", cur: i == in.pos, nl: true})
			continue
		}
		cells = append(cells, cell{s: string(c), cur: i == in.pos})
	}
	cells = append(cells, cell{s: " ", cur: in.pos == len(in.r)})

	var lines [][]cell
	var line []cell
	used := 0
	for _, c := range cells {
		cw := ansi.StringWidth(c.s)
		if used+cw > w && len(line) > 0 {
			// Break after the last space on the line, else at the edge.
			cut := -1
			for j := len(line) - 1; j > 0; j-- {
				if line[j].s == " " {
					cut = j + 1
					break
				}
			}
			if cut > 0 && cut < len(line) {
				lines = append(lines, line[:cut])
				line = append([]cell{}, line[cut:]...)
			} else {
				lines = append(lines, line)
				line = nil
			}
			used = 0
			for _, x := range line {
				used += ansi.StringWidth(x.s)
			}
		}
		line = append(line, c)
		used += cw
		if c.nl {
			lines = append(lines, line)
			line, used = nil, 0
		}
	}
	if len(line) > 0 || len(lines) == 0 {
		lines = append(lines, line)
	}
	curLine := len(lines) - 1
	for i, l := range lines {
		for _, c := range l {
			if c.cur {
				curLine = i
			}
		}
	}
	start := 0
	if maxLines > 0 && len(lines) > maxLines {
		start = max(0, min(curLine-maxLines+1, len(lines)-maxLines))
		lines = lines[start : start+maxLines]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		var b strings.Builder
		for _, c := range l {
			if c.cur {
				b.WriteString(stCursor.Render(c.s))
			} else {
				b.WriteString(c.s)
			}
		}
		out = append(out, b.String())
	}
	return out
}

// boxLines is the input as wrapped lines behind a prompt mark: mark on the
// first line, the same width of spaces on the others.
func (in *lineInput) boxLines(mark string, w, maxLines int) []string {
	pad := strings.Repeat(" ", ansi.StringWidth(ansi.Strip(mark)))
	ls := in.wrapView(max(10, w-ansi.StringWidth(ansi.Strip(mark))), maxLines)
	for i := range ls {
		if i == 0 {
			ls[i] = mark + ls[i]
		} else {
			ls[i] = pad + ls[i]
		}
	}
	return ls
}
