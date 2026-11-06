package ui

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// partState is the prompt box's attachments (ADR-0020).
type partState struct {
	parts   []*promptPart
	next    int  // the number of the last part made
	pick    bool // ctrl+p: the cursor is on the parts
	sel     int
	open    bool // the selected part fills the screen
	scroll  int
	body    []string // the open part's lines, numbered and wrapped
	bodyFor string   // the part and width of body
	check   bool     // the check before sending is open
	cleaned bool     // old part files were removed in this run
}

// partClipMsg reports the comparison of a part with the clipboard.
type partClipMsg struct {
	n    int
	same int
}

// clipPasteMsg is what ctrl+v found on the clipboard.
type clipPasteMsg struct {
	path    string // an image, saved here
	text    string
	replace int // the part this image replaces (0 = a new part)
	err     error
}

// addPart puts a part in the box and its [n] in the text at the cursor.
func (m *Model) addPart(p *promptPart) {
	h := &m.home.part
	h.parts = append(h.parts, p)
	h.next = p.n
	d := &m.home.draft
	t := token(p.n) + " "
	if d.pos > 0 && d.r[d.pos-1] != ' ' && d.r[d.pos-1] != '\n' {
		t = " " + t
	}
	d.insert([]rune(t))
	m.setStatus(fmt.Sprintf("%s %s: %s", token(p.n), p.label(), p.facts()), false)
}

func (m *Model) partsDirReady() string {
	// With no snapshot the queue is unknown, so nothing goes yet.
	if !m.home.part.cleaned && m.snap != nil {
		m.home.part.cleaned = true
		cleanParts(m.partsDir, m.now(), m.queuedTexts())
	}
	return m.partsDir
}

// homePaste takes a paste in the box of a session: a dropped file or a long
// text becomes a part, anything else is text.
func (m *Model) homePaste(s string) tea.Cmd {
	r := m.homeSelected()
	if r == nil || r.sess == nil {
		m.home.draft.insert([]rune(s))
		return nil
	}
	n := m.home.part.next + 1
	if path, ok := pastedPath(s); ok {
		p, err := newFilePart(n, path)
		if err != nil {
			m.setStatus("Not attached: "+oneLine(err.Error()), true)
			return nil
		}
		m.addPart(p)
		return nil
	}
	if !isLongPaste(s) {
		m.home.draft.insert([]rune(s))
		return nil
	}
	p, err := newTextPart(m.partsDirReady(), n, s, m.now())
	if err != nil {
		m.setStatus("Not attached: "+oneLine(err.Error()), true)
		return nil
	}
	m.addPart(p)
	read := m.clipRead
	return func() tea.Msg {
		c, err := read()
		switch {
		case err != nil || c == "":
			return partClipMsg{n: n}
		case sameText(c, s):
			return partClipMsg{n: n, same: 1}
		}
		return partClipMsg{n: n, same: -1}
	}
}

func (m *Model) onPartClip(msg partClipMsg) {
	for _, p := range m.home.part.parts {
		if p.n == msg.n {
			p.same = msg.same
		}
	}
}

// pasteClipboard is ctrl+v: an image on the clipboard, else its text. A
// terminal cannot paste an image, so smif asks the system for it.
func (m *Model) pasteClipboard(replace int) tea.Cmd {
	dir := m.partsDirReady()
	n := max(replace, m.home.part.next+1)
	path := filepath.Join(dir, partFileName(m.now(), n, "png"))
	img, txt := m.clipImage, m.clipRead
	return func() tea.Msg {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return clipPasteMsg{err: err}
		}
		if err := img(path); err == nil {
			_ = os.Chmod(path, 0o600)
			return clipPasteMsg{path: path, replace: replace}
		}
		_ = os.Remove(path)
		if replace > 0 {
			return clipPasteMsg{err: fmt.Errorf("no image on the clipboard")}
		}
		text, err := txt()
		return clipPasteMsg{text: text, err: err}
	}
}

func (m *Model) onClipPaste(msg clipPasteMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.setStatus("Nothing pasted: "+oneLine(msg.err.Error()), true)
	case msg.path != "":
		n := msg.replace
		if n == 0 {
			n = m.home.part.next + 1
		}
		p, err := newImagePart(n, msg.path, clipboardFrom+" "+m.now().Format("15:04"))
		if err != nil {
			m.setStatus("Not attached: "+oneLine(err.Error()), true)
			return nil
		}
		if msg.replace == 0 {
			m.addPart(p)
			return nil
		}
		for i, old := range m.home.part.parts {
			if old.n == n {
				m.dropPartFile(old)
				m.home.part.parts[i] = p
				m.home.part.bodyFor = ""
			}
		}
		m.setStatus(token(n)+" replaced from the clipboard.", false)
	case msg.text == "":
		m.setStatus("The clipboard has no image and no text.", true)
	default:
		return m.homePaste(msg.text)
	}
	return nil
}

// dropPartFile removes a file smif made. A dropped file stays where it is.
func (m *Model) dropPartFile(p *promptPart) {
	if filepath.Dir(p.path) == m.partsDir {
		_ = os.Remove(p.path)
	}
}

// removePart takes a part and its [n] out of the box.
func (m *Model) removePart(i int) {
	h := &m.home.part
	p := h.parts[i]
	m.dropPartFile(p)
	h.parts = append(h.parts[:i], h.parts[i+1:]...)
	if !m.home.draft.remove(token(p.n) + " ") {
		m.home.draft.remove(token(p.n))
	}
	m.setStatus(token(p.n)+" removed.", false)
	h.bodyFor = ""
	h.scroll = 0
	h.sel = max(0, min(h.sel, len(h.parts)-1))
	if len(h.parts) == 0 {
		h.pick, h.open, h.next = false, false, 0
	}
}

// partKey handles the keys of the parts, the part screen and the check. It
// reports false for a key that goes on to the text box.
func (m *Model) partKey(r *homeRow, k string) (tea.Cmd, bool) {
	h := &m.home.part
	session := r != nil && r.sess != nil
	switch {
	case !session:
		return nil, false
	case h.check:
		return m.checkKey(r, k), true
	case h.open:
		return m.partOpenKey(k), true
	case h.pick:
		return m.partPickKey(k), true
	}
	switch k {
	case "ctrl+v":
		return m.pasteClipboard(0), true
	case "ctrl+p":
		if len(h.parts) == 0 {
			m.setStatus("No parts yet: paste a long text, or an image with ctrl+v.", false)
			return nil, true
		}
		h.pick, h.sel = true, len(h.parts)-1
		return nil, true
	}
	return nil, false
}

func (m *Model) partPickKey(k string) tea.Cmd {
	h := &m.home.part
	switch k {
	case "up", "left", "k":
		h.sel = max(0, h.sel-1)
	case "down", "right", "j":
		h.sel = min(len(h.parts)-1, h.sel+1)
	case "x":
		m.removePart(h.sel)
	case "enter":
		h.open, h.scroll, h.bodyFor = true, 0, ""
	case "esc", "ctrl+p":
		h.pick = false
	}
	return nil
}

func (m *Model) partOpenKey(k string) tea.Cmd {
	h := &m.home.part
	page := max(1, m.height-10)
	switch k {
	case "up", "k":
		h.scroll--
	case "down", "j":
		h.scroll++
	case "pgup":
		h.scroll -= page
	case "pgdown", " ", "space":
		h.scroll += page
	case "left", "h":
		h.sel, h.scroll, h.bodyFor = max(0, h.sel-1), 0, ""
	case "right", "l":
		h.sel, h.scroll, h.bodyFor = min(len(h.parts)-1, h.sel+1), 0, ""
	case "x":
		m.removePart(h.sel)
	case "r":
		if p := h.parts[h.sel]; p.kind == partImage && filepath.Dir(p.path) == m.partsDir {
			return m.pasteClipboard(p.n)
		}
	case "o":
		return m.openPart(h.parts[h.sel])
	case "esc":
		h.open = false
	}
	h.scroll = max(0, h.scroll)
	return nil
}

// openPart shows the part in the system's viewer. The path goes as one
// argument and comes from smif or from a file the human dropped.
func (m *Model) openPart(p *promptPart) tea.Cmd {
	opener, path := "xdg-open", p.path
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, opener, path).Run(); err != nil {
			return clipPasteMsg{err: fmt.Errorf("cannot open it: %v", err)}
		}
		return nil
	}
}

func (m *Model) checkKey(r *homeRow, k string) tea.Cmd {
	h := &m.home.part
	switch k {
	case "enter":
		h.check = false
		return m.sendParts(r)
	case "e", "esc":
		h.check = false
	case "g":
		for i, p := range h.parts {
			for _, w := range p.warns {
				if w.line > 0 {
					h.check, h.sel, h.open, h.bodyFor = false, i, true, ""
					h.scroll = -w.line // placed once the lines are drawn
					return nil
				}
			}
		}
	}
	return nil
}

// trySendParts sends the box with its parts: typed in when the session is
// idle, else into the queue. Warnings stop it once, on the check screen.
func (m *Model) trySendParts(r *homeRow) tea.Cmd {
	h := &m.home.part
	if !canWrite(r.sess) {
		m.setStatus(writeHint(r.sess), true)
		return nil
	}
	if r.sess.Host != "" {
		m.setStatus("Files stay on this machine, so they cannot go to "+r.sess.HostLabel()+".", true)
		return nil
	}
	if gone := partsGone(h.parts); gone != "" {
		m.setStatus(gone+" is gone from the disk. Remove it with ctrl+p and attach it again.", true)
		return nil
	}
	if len(allWarns(h.parts)) > 0 {
		h.check = true
		return nil
	}
	return m.sendParts(r)
}

func (m *Model) sendParts(r *homeRow) tea.Cmd {
	return m.routeLine(r, partsPrompt(m.home.draft.Value(), m.home.part.parts))
}

// partKeys is the footer while the parts have the keys ("" = they do not).
func (m *Model) partKeys() string {
	h := &m.home.part
	switch {
	case h.check:
		return "enter send · e back to edit · g go to the first warning · esc cancel"
	case h.open && h.parts[h.sel].kind == partImage:
		return "x remove · r replace from clipboard · o open · ←→ other parts · esc back"
	case h.open:
		return "↑↓ pgup pgdn scroll · x remove · o open · ←→ other parts · esc back"
	case h.pick:
		return "↑↓ choose a part · enter open · x remove · esc back to the text"
	}
	return ""
}

// thumbSize fits an image into cells, each two pixels high.
func thumbSize(img image.Image, maxCols, maxRows int) (cols, rows int) {
	b := img.Bounds()
	w, h := max(1, b.Dx()), max(1, b.Dy())
	cols = maxCols
	rows = max(1, cols*h/w/2)
	if rows > maxRows {
		rows = maxRows
		cols = max(1, rows*2*w/h)
	}
	return cols, rows
}

// thumbLines draws an image with ▀: the top half of a cell is one pixel and
// the bottom half another, which any colour terminal can show.
func thumbLines(img image.Image, maxCols, maxRows int) []string {
	if img == nil || !themeColor {
		return nil
	}
	cols, rows := thumbSize(img, maxCols, maxRows)
	b := img.Bounds()
	hex := func(x, y int) string {
		c := img.At(b.Min.X+x*b.Dx()/cols, b.Min.Y+y*b.Dy()/(rows*2))
		r, g, bl, _ := c.RGBA()
		return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, bl>>8)
	}
	out := make([]string, rows)
	for y := range rows {
		var s strings.Builder
		for x := range cols {
			s.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex(x, y*2))).Background(lipgloss.Color(hex(x, y*2+1))).Render("▀"))
		}
		out[y] = s.String()
	}
	return out
}

// partHead is a part's first line: its [n], name, size and warnings.
func partHead(p *promptPart, w int, sel bool) string {
	left := token(p.n) + " " + p.label()
	if sel {
		left = "▸ " + left
	}
	right := p.facts()
	if n := len(allWarns([]*promptPart{p})); n > 0 {
		right = "! " + countOf(n, "warning", "warnings") + " · " + right
	}
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		right = ansi.Truncate(right, max(0, w-ansi.StringWidth(left)-2), "…")
		gap = 2
	}
	st := stNew
	if sel {
		st = stFocus
	}
	rs := stDim
	if strings.HasPrefix(right, "!") {
		rs = stWarn
	}
	return st.Render(left) + strings.Repeat(" ", gap) + rs.Render(right)
}

func numbered(n int, l string, w int) string {
	return stDim.Render(fmt.Sprintf("%5d ", n)) + ansi.Truncate(partLine(l), max(1, w-6), "…")
}

// partLines draws a part in the box: a text with its start and its end, an
// image with its thumbnail. compact leaves only the first line.
func partLines(p *promptPart, w int, sel, compact bool) []string {
	out := []string{partHead(p, w, sel)}
	if compact {
		return out
	}
	if p.kind == partImage {
		tl := thumbLines(p.thumb, 16, 4)
		info := []string{stDim.Render("from " + p.from)}
		for i, l := range tl {
			if i < len(info) {
				l += "  " + ansi.Truncate(info[i], max(1, w-20), "…")
			}
			out = append(out, l)
		}
		if len(tl) == 0 {
			out = append(out, info...)
		}
		return out
	}
	if p.binary {
		return out
	}
	for i, l := range p.head {
		out = append(out, numbered(i+1, l, w))
	}
	if len(p.tail) > 0 {
		hid := p.lines - len(p.head) - len(p.tail)
		out = append(out, stDim.Render(fmt.Sprintf("    ⋮ %d lines not shown · ctrl+p, enter opens all", hid)))
		for i, l := range p.tail {
			out = append(out, numbered(p.lines-len(p.tail)+i+1, l, w))
		}
	}
	return out
}

// partBoxLines are the parts under the text of the box. With more than two,
// or little room, each one takes one line.
func (m *Model) partBoxLines(w, h int) []string {
	ps := m.home.part.parts
	compact := len(ps) > 2 || h < 26
	var out []string
	for i, p := range ps {
		out = append(out, partLines(p, w, m.home.part.pick && i == m.home.part.sel, compact)...)
	}
	return out
}

// partBodyLines reads the open part into numbered, wrapped lines.
func (m *Model) partBodyLines(p *promptPart, w int) []string {
	h := &m.home.part
	key := fmt.Sprintf("%s %d", p.path, w)
	if h.bodyFor == key {
		return h.body
	}
	h.bodyFor, h.body = key, nil
	b, err := os.ReadFile(p.path)
	if err != nil {
		h.body = []string{stBad.Render("Cannot read it: " + oneLine(err.Error()))}
		return h.body
	}
	warn := map[int]bool{}
	for _, x := range p.warns {
		warn[x.line] = true
	}
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		parts := strings.Split(ansi.Hardwrap(partLine(l), max(1, w-6), true), "\n")
		if len(parts) > 40 {
			hid := len(parts) - 40
			parts = append(parts[:40], stDim.Render(fmt.Sprintf("… this line goes on for %d more screen lines", hid)))
		}
		for j, s := range parts {
			num := "      "
			if j == 0 {
				num = fmt.Sprintf("%5d ", i+1)
			}
			if warn[i+1] {
				h.body = append(h.body, stWarn.Render(num)+s)
			} else {
				h.body = append(h.body, stDim.Render(num)+s)
			}
		}
	}
	return h.body
}

// partScreen is the open part: a text to scroll, or an image's card.
func (m *Model) partScreen(sv *api.SessionView, w, h int) []string {
	st := &m.home.part
	p := st.parts[st.sel]
	head := []string{ansi.Truncate(stDim.Render("esc back  ")+stTitle.Render(sessionName(sv))+stDim.Render(fmt.Sprintf(" › part %d of %d", st.sel+1, len(st.parts))), w, "…"), "", partHead(p, w, false), ""}
	if p.kind == partImage {
		return append(head, partCard(p, w, max(1, h-len(head)))...)
	}
	if p.binary {
		return append(head, stDim.Render("No preview: not a text file, or too big to show."))
	}
	body := m.partBodyLines(p, w)
	room := max(1, h-len(head))
	if st.scroll < 0 { // a warning's line: the line itself may sit lower
		want := fmt.Sprintf("%5d ", -st.scroll)
		st.scroll = 0
		for i, l := range body {
			if strings.HasPrefix(ansi.Strip(l), want) {
				st.scroll = max(0, i-2)
				break
			}
		}
	}
	st.scroll = min(st.scroll, max(0, len(body)-room))
	return append(head, body[st.scroll:min(len(body), st.scroll+room)]...)
}

// partCard is an image with what is known about it.
func partCard(p *promptPart, w, h int) []string {
	state := stOK.Render("✓ will be sent")
	if _, err := os.Stat(p.path); err != nil {
		state = stBad.Render("✗ the file is gone")
	}
	info := []string{
		stNew.Render(p.name), "",
		stDim.Render("size   ") + p.facts(),
		stDim.Render("from   ") + ansi.Truncate(p.from, max(1, w-8), "…"),
		stDim.Render("saved  ") + ansi.Truncate(shortHome(p.path), max(1, w-8), "…"),
		stDim.Render("state  ") + state,
	}
	tw := min(70, max(0, w-40))
	tl := thumbLines(p.thumb, tw, h-1)
	if tw < 20 || len(tl) == 0 {
		return append(tl, append([]string{""}, info...)...)
	}
	out := make([]string, 0, max(len(tl), len(info)))
	cols := ansi.StringWidth(tl[0])
	for i := range max(len(tl), len(info)) {
		l := strings.Repeat(" ", cols)
		if i < len(tl) {
			l = tl[i]
		}
		if i < len(info) {
			l += "   " + ansi.Truncate(info[i], max(1, w-cols-3), "…")
		}
		out = append(out, l)
	}
	return out
}

// checkScreen is shown before sending when a part has a warning.
func (m *Model) checkScreen(sv *api.SessionView, w, h int) []string {
	ps := m.home.part.parts
	out := []string{stTitle.Render(sessionName(sv)) + stDim.Render(" › check before sending"), ""}
	if t := strings.TrimSpace(m.home.draft.Value()); t != "" {
		out = append(out, stDim.Render("Your text"))
		out = append(out, wrapLines(t, w, 4)...)
		out = append(out, "")
	}
	for _, p := range ps {
		out = append(out, partHead(p, w, false))
		if len(p.head) > 0 {
			out = append(out, stDim.Render("starts ")+ansi.Truncate(partLine(p.head[0]), max(1, w-7), "…"))
			end := p.head[len(p.head)-1]
			if len(p.tail) > 0 {
				end = p.tail[len(p.tail)-1]
			}
			out = append(out, stDim.Render("ends   ")+ansi.Truncate(partLine(end), max(1, w-7), "…"))
		}
	}
	ws := allWarns(ps)
	out = append(out, "", stWarn.Render("! "+countOf(len(ws), "thing", "things")+" to look at"))
	for _, l := range ws {
		out = append(out, ansi.Truncate(l, w, "…"))
	}
	return append(out, "", "Goes to "+sessionName(sv)+": "+routeShort(sv))
}
