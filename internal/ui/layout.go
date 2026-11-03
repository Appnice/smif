package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// fitKeys fits a key hint line ("a x · b y · ? help · q quit") into w
// cells. It drops hints from the end, but it keeps "? help" and the last
// hint (quit), so the way out and the way to every key stay on screen.
func fitKeys(line string, w int) string {
	ks := strings.Split(line, " · ")
	if ansi.StringWidth(line) <= w || len(ks) < 2 {
		return line
	}
	keep := func(i int) bool { return i == len(ks)-1 || ks[i] == "? help" }
	for ansi.StringWidth(strings.Join(ks, " · ")) > w {
		drop := -1
		for i := len(ks) - 1; i >= 0; i-- {
			if !keep(i) {
				drop = i
				break
			}
		}
		if drop < 0 {
			break
		}
		ks = append(ks[:drop], ks[drop+1:]...)
	}
	return strings.Join(ks, " · ")
}

// part is one piece of a status line. When the line is too wide, the
// part with the highest drop number changes first: a part with an alt
// changes to its alt and then stays, a part without one goes. Parts with
// drop 0 always stay.
type part struct {
	text string
	drop int
	alt  string
}

// fitParts joins parts with sep into at most w cells.
func fitParts(w int, sep string, parts ...part) string {
	join := func() string {
		var ss []string
		for _, p := range parts {
			if p.text != "" {
				ss = append(ss, p.text)
			}
		}
		return strings.Join(ss, sep)
	}
	for ansi.StringWidth(join()) > w {
		best := -1
		for i, p := range parts {
			if p.text != "" && p.drop > 0 && (best < 0 || p.drop > parts[best].drop) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		if parts[best].alt != "" {
			parts[best].text, parts[best].alt, parts[best].drop = parts[best].alt, "", 0
		} else {
			parts[best].text = ""
		}
	}
	return join()
}

// colGap is the space between two table columns.
const colGap = 2

// fitColumns shrinks column widths so that a table row fits in avail
// cells. The last column takes what is left but gets at least lastMin
// cells when it can. The widest other column shrinks first, never below
// 4 cells. The first column (an id or a name) keeps at least 10 cells,
// the width of a short id.
func fitColumns(widths []int, avail, lastMin int) {
	minCol := func(i int) int {
		if i == 0 {
			return 10
		}
		return 4
	}
	n := len(widths)
	if n == 0 {
		return
	}
	total := func() int {
		t := colGap * (n - 1)
		for _, x := range widths[:n-1] {
			t += x
		}
		return t + min(widths[n-1], lastMin)
	}
	for total() > avail {
		wi := -1
		for i := 0; i < n-1; i++ {
			if widths[i] > minCol(i) && (wi < 0 || widths[i] > widths[wi]) {
				wi = i
			}
		}
		if wi < 0 {
			return
		}
		widths[wi]--
	}
}
