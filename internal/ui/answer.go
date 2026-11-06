package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// A session's answer in the UI: only its final message (the text after the
// last tool call, from session.tail and session.list), never the steps in
// between. The row shows its first line, the preview the last exchange
// (your prompt, then the answer), enter the whole answer, o its tab.

// SessionFocuser brings a session's terminal tab to the front
// (session.focus). DaemonSource implements it.
type SessionFocuser interface {
	FocusSession(ctx context.Context, sessionID string) error
}

// FocusSession brings the session's Terminal tab to the front.
func (s *DaemonSource) FocusSession(ctx context.Context, sessionID string) error {
	return s.callT(ctx, "session.focus", api.SessionFocusParams{SessionID: sessionID}, nil, 20*time.Second)
}

// SessionAttacher opens `claude attach` for a background session in a new
// tab (session.attach, ADR-0021). DaemonSource implements it.
type SessionAttacher interface {
	AttachSession(ctx context.Context, sessionID, term string) error
}

// AttachSession opens claude attach for the session.
func (s *DaemonSource) AttachSession(ctx context.Context, sessionID, term string) error {
	return s.callT(ctx, "session.attach", api.SessionAttachParams{SessionID: sessionID, Term: term}, nil, 20*time.Second)
}

type focusedMsg struct {
	name   string
	attach bool
	err    error
}

// attachSession is o on a background session. It has no tab, so claude
// attach opens one in the UI's own terminal, else in the daemon's default.
func (m *Model) attachSession(sv *api.SessionView) tea.Cmd {
	a, ok := m.src.(SessionAttacher)
	if !ok {
		m.setStatus("This UI cannot open a background session.", true)
		return nil
	}
	id, name, term := sv.SessionID, sessionName(sv), uiTerm()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return focusedMsg{name: name, attach: true, err: a.AttachSession(ctx, id, term)}
	}
}

// focusSession is o on a session: its tab comes to the front.
func (m *Model) focusSession(sv *api.SessionView) tea.Cmd {
	if sv.Host != "" {
		m.setStatus("This session runs on "+sv.HostLabel()+": ssh there to answer it.", false)
		return nil
	}
	if sv.Background {
		return m.attachSession(sv)
	}
	f, ok := m.src.(SessionFocuser)
	if !ok {
		m.setStatus("This UI cannot show a session's tab.", true)
		return nil
	}
	id, name := sv.SessionID, sessionName(sv)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return focusedMsg{name: name, err: f.FocusSession(ctx, id)}
	}
}

func (m *Model) onFocused(msg focusedMsg) {
	switch {
	case msg.attach && msg.err != nil:
		m.setStatus("Cannot open claude attach: "+oneLine(rpcMessage(msg.err)), true)
		return
	case msg.attach:
		m.setStatus("Opened claude attach for "+msg.name+" in a new tab.", false)
		return
	case msg.err != nil:
		m.setStatus("Cannot show the tab: "+oneLine(rpcMessage(msg.err)), true)
		return
	}
	m.setStatus("Terminal shows "+msg.name+".", false)
}

// noteSessions runs on each snapshot: a session seen for the first time
// starts with its answer read (only answers that come later are new). The
// selected session is NOT marked here: an answer that comes while the
// cursor rests on a session stays new until the human moves onto it or
// opens its chat.
func (m *Model) noteSessions() {
	if m.snap == nil {
		return
	}
	if m.home.seen == nil {
		m.home.seen = map[string]time.Time{}
	}
	added := false
	for i := range m.snap.Sessions {
		sv := &m.snap.Sessions[i]
		if _, ok := m.home.seen[sv.SessionID]; !ok {
			m.home.seen[sv.SessionID] = sv.LastAnswerAt
			added = true
		}
	}
	if added {
		m.saveSeen()
	}
	m.notePending()
}

// notePending reports a prompt sent in the background that started no
// turn; after 20 s without that, the prompt counts as taken.
func (m *Model) notePending() {
	for i := range m.snap.Sessions {
		sv := &m.snap.Sessions[i]
		at, ok := m.home.pending[sv.SessionID]
		switch {
		case !ok:
		case sv.Unsent != "" && !sv.UnsentAt.Before(at):
			m.setStatus("Not sent to "+sessionName(sv)+": "+sv.Unsent, true)
			delete(m.home.pending, sv.SessionID)
		case time.Since(at) > 20*time.Second:
			delete(m.home.pending, sv.SessionID)
		}
	}
}

// markAnswerRead marks the session's answer read: the human moved onto
// the session or has its chat open.
func (m *Model) markAnswerRead(sv *api.SessionView) {
	if m.home.seen == nil {
		m.home.seen = map[string]time.Time{}
	}
	if old, ok := m.home.seen[sv.SessionID]; ok && !sv.LastAnswerAt.After(old) {
		return
	}
	m.home.seen[sv.SessionID] = sv.LastAnswerAt
	m.saveSeen()
}

// loadSeen reads the seen file; a missing or bad file is an empty map.
func loadSeen(path string) map[string]time.Time {
	out := map[string]time.Time{}
	if path == "" {
		return out
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// saveSeen writes the seen file (the newest 300 sessions), best effort.
func (m *Model) saveSeen() {
	if m.home.seenFile == "" {
		return
	}
	if len(m.home.seen) > 300 {
		type kv struct {
			k string
			t time.Time
		}
		var all []kv
		for k, t := range m.home.seen {
			all = append(all, kv{k, t})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
		m.home.seen = map[string]time.Time{}
		for _, x := range all[:300] {
			m.home.seen[x.k] = x.t
		}
	}
	b, err := json.Marshal(m.home.seen)
	if err != nil {
		return
	}
	tmp := m.home.seenFile + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.home.seenFile)
	}
}

// newAnswer reports an answer that came after the human last looked.
func (m *Model) newAnswer(sv *api.SessionView) bool {
	seen, ok := m.home.seen[sv.SessionID]
	return ok && sv.LastAnswer != "" && sv.LastAnswerAt.After(seen)
}

var (
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdNumber = regexp.MustCompile(`^\d+[.)] `)
)

// plainFirstLine is an answer's first line without Markdown marks, for a row.
func plainFirstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#-*> "))
		if l == "" || strings.HasPrefix(l, "```") {
			continue
		}
		l = mdBold.ReplaceAllString(l, "$1")
		return mdCode.ReplaceAllString(l, "$1")
	}
	return ""
}

// mdInline styles **bold** and `code`.
func mdInline(s string) string {
	s = mdBold.ReplaceAllStringFunc(s, func(x string) string { return stTitle.Render(x[2 : len(x)-2]) })
	return mdCode.ReplaceAllStringFunc(s, func(x string) string { return stHunk.Render(x[1 : len(x)-1]) })
}

// mdLines draws a Markdown answer for a pane w wide: bullets with a hanging
// indent, headings and **bold** in bold, `code` and code blocks in colour.
func mdLines(text string, w int) []string {
	w = max(10, w)
	var out []string
	code := false
	var table []string
	flush := func() {
		if len(table) > 0 {
			out = append(out, mdTable(table, w)...)
			table = nil
		}
	}
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimRight(raw, " \t")
		t := strings.TrimLeft(l, " ")
		if !code && strings.HasPrefix(t, "|") {
			table = append(table, t)
			continue
		}
		flush()
		if strings.HasPrefix(t, "```") {
			code = !code
			continue
		}
		if code {
			// A code line is often a command to copy: break it, never cut it.
			for _, part := range strings.Split(ansi.Hardwrap(l, w, true), "\n") {
				out = append(out, stHunk.Render(part))
			}
			continue
		}
		lead := strings.Repeat(" ", min(len(l)-len(t), 8))
		first := lead
		switch {
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			first, t = lead+"• ", t[2:]
		case mdNumber.MatchString(t):
			n := mdNumber.FindString(t)
			first, t = lead+n, t[len(n):]
		case strings.HasPrefix(t, "#"):
			t = "**" + strings.TrimSpace(strings.TrimLeft(t, "#")) + "**"
		case strings.HasPrefix(t, "> "):
			first, t = lead+"│ ", t[2:]
		}
		indent := strings.Repeat(" ", ansi.StringWidth(first))
		for i, wl := range strings.Split(ansi.Wrap(mdInline(t), max(10, w-len(indent)), ""), "\n") {
			if i == 0 {
				out = append(out, first+wl)
			} else {
				out = append(out, indent+wl)
			}
		}
	}
	flush()
	for len(out) > 0 && strings.TrimSpace(ansi.Strip(out[len(out)-1])) == "" {
		out = out[:len(out)-1]
	}
	return out
}

var mdTableRule = regexp.MustCompile(`^:?-{2,}:?$`)

// mdCells splits one Markdown table row into trimmed cells.
func mdCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	var cells []string
	for _, c := range strings.Split(row, "|") {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// mdTable draws a Markdown table w cells wide: columns fitted to the
// width (the wide ones shrink and wrap), the header bold with a rule
// under it, cells joined by │.
func mdTable(rows []string, w int) []string {
	var grid [][]string
	header := -1
	for _, r := range rows {
		cells := mdCells(r)
		isRule := len(cells) > 0
		for _, c := range cells {
			if !mdTableRule.MatchString(c) {
				isRule = false
			}
		}
		if isRule {
			if len(grid) == 1 {
				header = 0
			}
			continue
		}
		grid = append(grid, cells)
	}
	n := 0
	for _, r := range grid {
		n = max(n, len(r))
	}
	if n == 0 {
		return nil
	}
	styled := make([][]string, len(grid))
	natural := make([]int, n)
	for i, r := range grid {
		styled[i] = make([]string, n)
		for j := 0; j < n; j++ {
			c := ""
			if j < len(r) {
				c = mdInline(r[j])
			}
			if i == header {
				c = stTitle.Render(ansi.Strip(c))
			}
			styled[i][j] = c
			natural[j] = max(natural[j], ansi.StringWidth(c))
		}
	}
	// Fit: every column gets its natural width when it fits; else the
	// narrow ones keep theirs and the rest share what is left.
	sep := " " + stFrame.Render("│") + " "
	avail := max(n*4, w-3*(n-1))
	widths := append([]int{}, natural...)
	for total := sum(widths); total > avail; total = sum(widths) {
		widest := 0
		for j := range widths {
			if widths[j] > widths[widest] {
				widest = j
			}
		}
		next := 0
		for j := range widths {
			if j != widest && widths[j] > next {
				next = widths[j]
			}
		}
		cut := min(total-avail, max(1, widths[widest]-max(next, 4)))
		widths[widest] = max(4, widths[widest]-cut)
		if widths[widest] == 4 && sum(widths) > avail {
			break
		}
	}
	var out []string
	for i, r := range styled {
		cols := make([][]string, n)
		h := 1
		for j, c := range r {
			cols[j] = strings.Split(ansi.Wrap(c, widths[j], ""), "\n")
			h = max(h, len(cols[j]))
		}
		for k := 0; k < h; k++ {
			var parts []string
			for j := range cols {
				c := ""
				if k < len(cols[j]) {
					c = cols[j][k]
				}
				if j < n-1 {
					c += strings.Repeat(" ", max(0, widths[j]-ansi.StringWidth(c)))
				}
				parts = append(parts, c)
			}
			out = append(out, strings.Join(parts, sep))
		}
		if i == header {
			var rule []string
			for _, cw := range widths {
				rule = append(rule, strings.Repeat("─", cw))
			}
			out = append(out, stFrame.Render(strings.Join(rule, "─┼─")))
		}
	}
	return out
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

// rule is a section line: label ──────── meta.
func rule(label, meta string, w int) string {
	fill := w - ansi.StringWidth(label) - ansi.StringWidth(meta) - 2
	if fill < 3 {
		return label + " " + meta
	}
	return label + " " + stFrame.Render(strings.Repeat("─", fill)) + " " + stDim.Render(meta)
}

// sessionTurns is the selected session's conversation from the cache.
func (m *Model) sessionTurns(sv *api.SessionView) ([]api.SessionTurn, error) {
	e := m.home.tails[sv.SessionID]
	if e == nil {
		return nil, nil
	}
	if e.tail == nil {
		return nil, e.err
	}
	return e.tail.Turns, e.err
}

// isPrompt and isAnswer group the turn roles: a prompt typed into the
// session or an ask to its fork; Claude's answer or the fork's.
func isPrompt(t api.SessionTurn) bool { return t.Role == "you" || t.Role == "ask" }
func isAnswer(t api.SessionTurn) bool { return t.Role == "claude" || t.Role == "worker" }

// lastExchange splits the turns: the earlier answers, the last prompt and
// the answer to it (nil while there is none).
func lastExchange(turns []api.SessionTurn) (earlier []api.SessionTurn, you, answer *api.SessionTurn) {
	end := len(turns)
	if end > 0 && isAnswer(turns[end-1]) {
		answer = &turns[end-1]
		end--
	}
	if end > 0 && isPrompt(turns[end-1]) {
		you = &turns[end-1]
		end--
	}
	for _, t := range turns[:end] {
		if isAnswer(t) && t.Text != "" {
			earlier = append(earlier, t)
		}
	}
	return earlier, you, answer
}

// promptLabel is "› you", "› you → fork" or "› web asks a fork".
func promptLabel(t *api.SessionTurn) string {
	switch {
	case t.Role == "ask" && t.From != "" && t.From != "you":
		return stAccent.Render("› " + t.From + " asks a fork")
	case t.Role == "ask":
		return stAccent.Render("› you → fork")
	}
	return stAccent.Render("› you")
}

// answerLines draws one answer: its rule, then its final message (or a
// working line for a worker that has not answered yet).
func answerLines(you, answer *api.SessionTurn, w int, now time.Time) []string {
	if answer.Role == "worker" {
		switch {
		case answer.State == "queued" || answer.State == "running" || answer.State == "accepted" || answer.State == "retry_wait":
			return []string{rule(stLive.Render(spinner(now)+" fork"), "a copy of this session · "+answer.State, w)}
		case answer.State != "ok" && answer.State != "":
			return []string{rule(stBad.Render("✗ fork"), answer.State, w), "  " + stBad.Render(answer.Text)}
		}
		out := []string{rule(stLive.Render("◇ fork"), "a copy of this session · "+answerMeta(you, answer, now), w), ""}
		for _, l := range mdLines(answer.Text, w-2) {
			out = append(out, "  "+l)
		}
		return out
	}
	out := []string{rule(stOK.Render("◆ answer"), answerMeta(you, answer, now), w), ""}
	for _, l := range mdLines(answer.Text, w-2) {
		out = append(out, "  "+l)
	}
	return out
}

func clock(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	if y, mo, d := now.Local().Date(); t.Year() != y || t.Month() != mo || t.Day() != d {
		if t.After(now.Add(-48 * time.Hour)) {
			return "yesterday " + t.Format("15:04")
		}
		return t.Format("2 Jan 15:04")
	}
	return t.Format("15:04")
}

// answerMeta is "09:01 · 41 s · 2 tools" for an answer to a prompt.
func answerMeta(you, answer *api.SessionTurn, now time.Time) string {
	parts := []string{clock(answer.At, now)}
	if you != nil && answer.At.After(you.At) {
		parts = append(parts, dur(answer.At.Sub(you.At)))
	}
	if answer.Tools > 0 {
		parts = append(parts, toolCount(answer.Tools))
	}
	return strings.Join(parts, " · ")
}

// sessionRowLine is one session row: the state (! 4m, ▶ 2m14s, ● new,
// ○ idle), the name, then what it does now (the step that runs, or what
// it asks for) or the first line of its last answer, and on the right the
// tool count, "silent 6m" or the answer's age.
func (m *Model) sessionRowLine(r homeRow, selected bool, w int, now time.Time) string {
	sv := r.sess
	kind := m.sessionKind(sv)
	name := sessionName(sv)
	if sv.Title == "" && m.home.agent != "" {
		name = "session " + api.ShortSession(sv.SessionID)
	}
	nw := m.sessionNameWidth()
	shown := ansi.Truncate(name, nw, "…")
	if kind == kNew || kind == kWaiting {
		shown = stTitle.Render(shown)
	}
	state := m.sessionStateCell(sv, now)
	head := " " + state + strings.Repeat(" ", max(1, 9-ansi.StringWidth(state))) + shown
	head += strings.Repeat(" ", max(2, nw+12-ansi.StringWidth(head)))
	var what, tail string
	switch kind {
	case kWaiting:
		if s := nowStep(sv); s != nil {
			what = stepLabel(s, stWarn)
		} else {
			what = "waits for you in its tab"
		}
		tail = stWarn.Render("needs you")
	case kWorking:
		switch s := nowStep(sv); {
		case s != nil:
			what = stepLabel(s, stLive)
		case sv.Activity != nil && sv.Activity.Tools > 0:
			what = stDim.Render("thinking")
		default:
			what = stDim.Render("starting")
		}
		var t []string
		if a := sv.Activity; a != nil && a.Tools > 0 {
			t = append(t, stDim.Render(toolCount(a.Tools)))
		}
		if d := silentFor(sv, now); d > 0 {
			t = append(t, stWarn.Render("silent "+ago(d)))
		}
		tail = strings.Join(t, stDim.Render(" · "))
	default:
		switch {
		case sv.Turn == "unknown" && nowStep(sv) != nil:
			what = stepLabel(nowStep(sv), stDim)
		case sv.LastAnswer != "" && kind == kNew:
			what = stTitle.Render(plainFirstLine(sv.LastAnswer))
		case sv.LastAnswer != "":
			what = stDim.Render(plainFirstLine(sv.LastAnswer))
		case sv.LastPrompt != "":
			what = stDim.Render("› " + sv.LastPrompt)
		default:
			what = stDim.Render(sessionState(sv))
		}
		at := sv.LastAnswerAt
		if at.IsZero() {
			at = r.msg.UpdatedAt
		}
		if !at.IsZero() {
			tail = stDim.Render(ago(now.Sub(at)))
		}
	}
	if !fullLevel(sv) {
		what = stDim.Render(levelState(sv))
	}
	if mark := harnessMark(sv); mark != "" {
		what = stDim.Render(mark+" ") + what
	}
	if mark := contextMark(sv); mark != "" && tail != "" {
		tail = stWarn.Render(mark) + stDim.Render(" · ") + tail
	} else if mark != "" {
		tail = stWarn.Render(mark)
	}
	if mark := queueMark(sv); mark != "" && tail != "" {
		tail = stAccent.Render(mark) + stDim.Render(" · ") + tail
	} else if mark != "" {
		tail = stAccent.Render(mark)
	}
	w--
	room := w - ansi.StringWidth(head) - ansi.StringWidth(tail) - 1
	line := head + ansi.Truncate(what, max(0, room), "…")
	line += strings.Repeat(" ", max(1, w-ansi.StringWidth(line)-ansi.StringWidth(tail))) + tail
	if selected {
		return stSelRow.Render("▌" + line)
	}
	return " " + line
}

// sessionRoot is the folder a session groups under: its agent's repo, or
// the git root of its folder when no agent owns it.
func sessionRoot(sv *api.SessionView) string {
	if sv == nil {
		return ""
	}
	return sv.Repo
}

// shortHome writes a path under the home folder as ~/….
func shortHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// sessionNameWidth is the name column: the longest name, at most 16.
func (m *Model) sessionNameWidth() int {
	w := 4
	if m.snap != nil {
		for i := range m.snap.Sessions {
			w = max(w, ansi.StringWidth(sessionName(&m.snap.Sessions[i])))
		}
	}
	return min(w, 16)
}

// sessionHeader is the first preview line: the state, the name, the
// folder, the tty and how long it has been idle.
func (m *Model) sessionHeader(sv *api.SessionView, w int, now time.Time) string {
	meta := []string{shortHome(sv.Repo)}
	if sv.TTY != "" {
		meta = append(meta, strings.TrimPrefix(sv.TTY, "/dev/"))
	}
	if sv.Turn == "idle" {
		at := sv.LastAnswerAt
		if at.IsZero() {
			at = sv.TurnAt
		}
		if !at.IsZero() {
			meta = append(meta, "idle "+ago(now.Sub(at)))
		}
	}
	return ansi.Truncate(m.sessionLabel(sv, now)+"  "+stTitle.Render(sessionName(sv))+stDim.Render("  "+strings.Join(meta, " · ")), w, "…")
}

// wrapLines wraps s to w, indented by two, at most n lines (0: all).
func wrapLines(s string, w, n int) []string {
	ls := strings.Split(ansi.Wrap(s, max(10, w-2), ""), "\n")
	if n > 0 && len(ls) > n {
		ls = append(ls[:n-1], "…")
	}
	for i := range ls {
		ls[i] = "  " + ls[i]
	}
	return ls
}

// dimLines renders each line faint.
func dimLines(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = stDim.Render(l)
	}
	return out
}

// stepRows are done steps as "15:00  Edit  path", faint.
func stepRows(steps []api.SessionStep, w int, now time.Time) []string {
	var out []string
	for _, s := range steps {
		out = append(out, ansi.Truncate(stDim.Render(fmt.Sprintf("  %-5s  %-5s %s", clock(s.At, now), s.Tool, s.Target)), w, "…"))
	}
	return out
}

// sessionBody is the preview of one session. Working: the prompt it works
// on, the step that runs now, the steps so far, its last answer. Waiting:
// what it asks for in its tab, the prompt, the steps before it asked. Idle:
// your last prompt and the final answer to it (full: the whole answer,
// else what fits in h, cut with a pointer to enter).
func (m *Model) sessionBody(sv *api.SessionView, w, h int, full bool) []string {
	if !fullLevel(sv) {
		return dimLines(wrapLines(levelNote(sv), w, 0))
	}
	now := m.now()
	turns, err := m.sessionTurns(sv)
	kind := m.sessionKind(sv)
	if kind == kWorking || kind == kWaiting || sv.Turn == "unknown" && nowStep(sv) != nil {
		return m.liveBody(sv, turns, w, now)
	}
	if len(turns) == 0 {
		var out []string
		if err != nil {
			out = append(out, stWarn.Render("Cannot read the conversation: "+oneLine(rpcMessage(err))))
		} else if sv.LastPrompt != "" {
			out = append(out, stDim.Render("you asked"), "  "+sv.LastPrompt)
		} else {
			out = append(out, stDim.Render("No conversation yet."))
		}
		return out
	}
	_, you, answer := lastExchange(turns)
	var out []string
	if you != nil {
		label := stDim.Render("you asked · " + clock(you.At, now))
		if you.Role == "ask" {
			label = promptLabel(you) + " " + stDim.Render(clock(you.At, now))
		}
		n := 4
		if full {
			n = 0
		}
		out = append(out, label)
		out = append(out, wrapLines(you.Text, w, n)...)
		out = append(out, "")
	}
	switch {
	case you != nil && you.Role == "ask" && answer == nil:
		out = append(out, rule(stLive.Render(spinner(now)+" fork"), "a copy of this session answers", w))
	case answer == nil || answer.Text == "" && answer.Role == "claude":
		out = append(out, stDim.Render("No answer yet."))
	default:
		body := answerLines(you, answer, w, now)
		if !full && h > 0 && len(out)+len(body) > h {
			body = append(body[:max(0, h-len(out)-1)], stDim.Render("… enter shows the whole answer"))
		}
		out = append(out, body...)
	}
	return out
}

// liveBody is the preview of a working or waiting session.
func (m *Model) liveBody(sv *api.SessionView, turns []api.SessionTurn, w int, now time.Time) []string {
	a := sv.Activity
	if a == nil {
		a = &api.SessionActivity{}
	}
	var out []string
	cur := nowStep(sv)
	if sv.Turn == "waiting" {
		out = append(out, stWarn.Render(waitReason(sv)))
		if cur != nil {
			out = append(out, "  "+stWarn.Render("│")+" "+ansi.Truncate(stepLabel(cur, stWarn), w-4, "…"))
			out = append(out, "  "+stWarn.Render("│")+" "+stDim.Render("asked "+clock(cur.At, now)+" · nothing has run since"))
		} else {
			out = append(out, "  "+stDim.Render(openWord(sv)))
		}
		out = append(out, "")
	}
	if a.Prompt != "" {
		out = append(out, stDim.Render("working on · you asked "+clock(a.PromptAt, now)))
		out = append(out, wrapLines(a.Prompt, w, 4)...)
		out = append(out, "")
	}
	var done []api.SessionStep
	for _, s := range a.Steps {
		if s.Done {
			done = append(done, s)
		}
	}
	if sv.Turn == "unknown" && cur != nil {
		out = append(out, stDim.Render("now · a tool runs, or an approval waits"))
		out = append(out, "  "+stDim.Render("│")+" "+ansi.Truncate(stepLabel(cur, stDim), w-4, "…"), "")
	}
	if sv.Turn == "busy" {
		switch {
		case cur != nil:
			out = append(out, stDim.Render("now · for "+elapsed(now.Sub(cur.At))))
			out = append(out, "  "+stLive.Render("│")+" "+ansi.Truncate(stepLabel(cur, stLive), w-4, "…"))
		default:
			out = append(out, stDim.Render("now"), "  "+stLive.Render("│")+" "+stDim.Render("thinking"))
		}
		if d := silentFor(sv, now); d > 0 {
			out = append(out, "  "+stWarn.Render("silent "+ago(d)+": nothing written since; a long command, or stuck"))
		}
		out = append(out, "")
		if len(done) > 5 {
			done = done[len(done)-5:]
		}
		if len(done) > 0 {
			out = append(out, rule(stDim.Render("steps so far"), toolCount(a.Tools), w))
			out = append(out, stepRows(done, w, now)...)
			out = append(out, "")
		}
	} else if len(done) > 0 {
		if len(done) > 3 {
			done = done[len(done)-3:]
		}
		out = append(out, rule(stDim.Render("before it asked"), toolCount(a.Tools), w))
		out = append(out, stepRows(done, w, now)...)
		out = append(out, "")
	}
	// Asks to its fork while it works, and their answers.
	for i := range turns {
		t := &turns[i]
		if !t.At.Before(a.PromptAt) && (t.Role == "ask" || t.Role == "worker") {
			if t.Role == "ask" {
				out = append(out, promptLabel(t)+" "+stDim.Render(clock(t.At, now)))
				out = append(out, wrapLines(t.Text, w, 3)...)
				if i == len(turns)-1 {
					out = append(out, rule(stLive.Render(spinner(now)+" fork"), "a copy of this session answers", w))
				}
				continue
			}
			out = append(out, answerLines(nil, t, w, now)...)
			out = append(out, "")
		}
	}
	if sv.LastAnswer != "" && sv.LastAnswerAt.Before(a.PromptAt) {
		out = append(out, rule(stDim.Render("last answer"), clock(sv.LastAnswerAt, now), w))
		out = append(out, ansi.Truncate(stDim.Render("  "+plainFirstLine(sv.LastAnswer)), w, "…"))
	}
	return out
}

// canFocus reports a terminal whose tab or pane Smif can bring to the
// front (session.focus).
func canFocus(term string) bool {
	return term == "Apple_Terminal" || term == "iTerm.app" || term == "tmux" || term == "herdr"
}

// canQueue reports whether a prompt can wait for this session to be idle
// (ADR-0021): it works now, and Smif may type into it.
func canQueue(sv *api.SessionView) bool {
	return (sv.Turn == "busy" || sv.Turn == "waiting") && !sv.Unverified && sv.Level != "listed" && !sv.Background && (sv.Host == "" || sv.Writable)
}

// canFork reports whether a read-only fork can answer for the session now:
// it needs an agent that owns the session's folder and the full level.
func canFork(sv *api.SessionView) bool {
	return canQueue(sv) && fullLevel(sv) && sv.Agent != "" && sv.Host == ""
}

// canWrite reports whether the human can write to the session now: typed
// in when idle, or into the queue when it works or waits.
func canWrite(sv *api.SessionView) bool { return sv.CanPrompt || canQueue(sv) }

// writeHint says where the text goes.
func writeHint(sv *api.SessionView) string {
	switch {
	case sv.CanPrompt:
		return "It is idle: what you write is typed into its tab."
	case !canQueue(sv):
		return "Cannot write now: " + sv.Why
	case sv.Turn == "waiting" && canFork(sv):
		return "It waits in its tab: enter queues, ctrl+f asks a fork."
	case sv.Turn == "waiting":
		return "It waits in its tab: enter queues for idle."
	case canFork(sv):
		return "It is busy: enter queues for idle, ctrl+f asks a fork."
	}
	return "It is busy: enter queues for idle. No agent owns its folder, so no fork can answer."
}

// routeShort is the line over the chat's box: where enter sends the text.
func routeShort(sv *api.SessionView) string {
	name := sessionName(sv)
	switch {
	case sv.CanPrompt:
		return "to " + name + " · it is idle, so enter types into its tab"
	case canWrite(sv):
		return "to " + name + " · it works, so enter queues it for when it is idle"
	}
	return "to " + name + " · cannot write now"
}

// sessionPreview is the right pane for a session row: the body, then the
// box (or where s would send the text).
func (m *Model) sessionPreview(r homeRow, w, h int) []string {
	sv := r.sess
	out := []string{m.sessionHeader(sv, w, m.now()), ""}
	var bottom []string
	switch {
	case m.home.picking:
		bottom = m.pickLines(sv, w)
	case m.home.typing:
		bottom = append([]string{""}, m.home.draft.boxLines(stAccent.Render("› "), w, 6)...)
		bottom = append(bottom, m.partBoxLines(w, h)...)
		bottom = append(bottom, stDim.Render(ansi.Truncate(writeHint(sv)+" esc cancels.", w, "…")))
	case sv.Host != "" && !sv.Writable:
		bottom = []string{"", stDim.Render(ansi.Truncate("Runs on "+sv.HostLabel()+": ssh there to answer it. Smif only shows it here.", w, "…"))}
	case sv.Background:
		bottom = []string{"", stAccent.Render("o") + " " + stDim.Render("open claude attach in a new tab"),
			stDim.Render(ansi.Truncate("Background session: it has no tab, so Smif does not write to it.", w, "…"))}
	case sv.Turn == "waiting" && sv.TTY != "" && canFocus(sv.Term):
		bottom = []string{"", stWarn.Render("o") + " bring its tab to the front and answer there"}
		if canWrite(sv) {
			bottom = append(bottom, stDim.Render(ansi.Truncate(writeHint(sv), w, "…")))
		}
	case (!fullLevel(sv) || sv.Unverified) && !canWrite(sv):
		bottom = append([]string{""}, dimLines(wrapLines(writeHint(sv), w, 0))...)
	case canWrite(sv):
		what := "reply…"
		if !sv.CanPrompt {
			what = "queue a prompt for when it is idle…"
		}
		bottom = []string{"", stAccent.Render("s") + " " + stDim.Render(what), stDim.Render(ansi.Truncate(writeHint(sv), w, "…"))}
	default:
		bottom = []string{"", stDim.Render(ansi.Truncate(writeHint(sv), w, "…"))}
	}
	bottom = append(m.queueLines(sv, w), bottom...)
	room := h - len(out) - len(bottom)
	facts := factsLines(sv.Facts, w)
	body := m.sessionBody(sv, w, room-len(facts), false)
	if facts != nil {
		// The facts stay under the answer, however long the answer is.
		body = body[:min(len(body), max(0, room-len(facts)))]
		for len(body) > 0 && body[len(body)-1] == "" {
			body = body[:len(body)-1]
		}
		body = append(body, facts...)
	}
	out = append(out, body...)
	keep := h - len(bottom)
	if len(out) > keep {
		out = out[:max(0, keep)]
	}
	for len(out) < keep {
		out = append(out, "")
	}
	return append(out, bottom...)
}

// chatBody is the whole conversation, oldest first: each prompt (typed in,
// or an ask to a fork) and its final answer, then what it does now.
func (m *Model) chatBody(sv *api.SessionView, w int) []string {
	now := m.now()
	turns, err := m.sessionTurns(sv)
	var out []string
	if err != nil && len(turns) == 0 {
		return []string{stWarn.Render("Cannot read the conversation: " + oneLine(rpcMessage(err)))}
	}
	var you *api.SessionTurn
	for i := range turns {
		t := &turns[i]
		if isPrompt(*t) {
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, promptLabel(t)+" "+stDim.Render(clock(t.At, now)))
			out = append(out, wrapLines(t.Text, w, 0)...)
			you = t
			continue
		}
		if t.Role == "claude" && t.Text == "" {
			continue // still working, or ended on a tool call
		}
		out = append(out, "")
		out = append(out, answerLines(you, t, w, now)...)
		you = nil
	}
	if n := len(turns); n > 0 && turns[n-1].Role == "ask" {
		out = append(out, "", rule(stLive.Render(spinner(now)+" fork"), "a copy of this session answers", w))
	}
	if len(out) > 0 {
		out = append(out, factsLines(sv.Facts, w)...)
	}
	if l := m.liveLine(sv, w, now); l != "" {
		out = append(out, "", l)
	}
	if len(out) == 0 {
		out = []string{stDim.Render("No conversation yet. Write below.")}
	}
	return out
}

// sessionFullLines is the chat with the selected session (enter on a
// session): the conversation, newest at the bottom, where the text goes,
// and the box. m.home.fullScroll counts lines up from the bottom.
func (m *Model) sessionFullLines(sv *api.SessionView, w, h int) []string {
	m.markAnswerRead(sv)
	now := m.now()
	header := stDim.Render("esc back  ") + m.sessionHeader(sv, w, now)
	if n := m.othersNeedYou(sv); n > 0 {
		header = ansi.Truncate(header, max(0, w-24), "…") + "  " + stWarn.Render(fmt.Sprintf("%d need you · tab", n))
	}
	head := []string{ansi.Truncate(header, w, "…"), ""}
	bottom := append(m.queueLines(sv, w), "", stDim.Render(ansi.Truncate(routeShort(sv), w, "…")))
	bottom = append(bottom, m.home.draft.boxLines(stAccent.Render("› "), w, 8)...)
	bottom = append(bottom, m.partBoxLines(w, h)...)
	bottom = append(bottom, stDim.Render(ansi.Truncate(writeHint(sv), w, "…")))
	body := m.chatBody(sv, w)
	room := max(1, h-len(head)-len(bottom))
	maxScroll := max(0, len(body)-room)
	m.home.fullScroll = max(0, min(m.home.fullScroll, maxScroll))
	end := len(body) - m.home.fullScroll
	view := body[max(0, end-room):end]
	if m.home.fullScroll > 0 {
		view[len(view)-1] = stDim.Render(fmt.Sprintf("↓ %d newer lines (↓ or end)", m.home.fullScroll))
	}
	out := head
	for len(out)+len(view) < h-len(bottom) {
		out = append(out, "") // newest at the bottom, next to the box
	}
	out = append(out, view...)
	return append(out, bottom...)
}

// chatKey handles the keys that are not text while the chat is open.
func (m *Model) chatKey(r *homeRow, k string) (tea.Cmd, bool) {
	page := max(1, m.height-10)
	switch k {
	case "esc":
		m.home.full, m.home.typing = false, false
	case "up":
		m.home.fullScroll++
	case "down":
		m.home.fullScroll--
	case "pgup":
		m.home.fullScroll += page
	case "pgdown":
		m.home.fullScroll -= page
	case "ctrl+o":
		return m.focusSession(r.sess), true
	case "ctrl+y":
		return m.copyCode(r.sess), true
	case "tab", "shift+tab":
		step := 1
		if k == "shift+tab" {
			step = -1
		}
		if !m.homeNextAttention(step, true) {
			m.setStatus("No other session needs you.", false)
			return nil, true
		}
		m.home.fullScroll = 0
		return m.loadPreview(true), true
	default:
		return nil, false
	}
	m.home.fullScroll = max(0, m.home.fullScroll)
	return nil, true
}
