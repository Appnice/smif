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

	"github.com/appnice/mailroom/internal/api"
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

type focusedMsg struct {
	name string
	err  error
}

// focusSession is o on a session: its tab comes to the front.
func (m *Model) focusSession(sv *api.SessionView) tea.Cmd {
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
	if msg.err != nil {
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

// newAnswers counts the sessions with a new answer.
func (m *Model) newAnswers() int {
	n := 0
	if m.snap != nil {
		for i := range m.snap.Sessions {
			if sv := &m.snap.Sessions[i]; sv.Turn != "busy" && m.newAnswer(sv) {
				n++
			}
		}
	}
	return n
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
			out = append(out, stHunk.Render(ansi.Truncate(l, w, "…")))
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

// promptLabel is "› you", "› you → worker" or "› web asks".
func promptLabel(t *api.SessionTurn) string {
	switch {
	case t.Role == "ask" && t.From != "" && t.From != "you":
		return stAccent.Render("› " + t.From + " asks a worker")
	case t.Role == "ask":
		return stAccent.Render("› you → worker")
	}
	return stAccent.Render("› you")
}

// answerLines draws one answer: its rule, then its final message (or a
// working line for a worker that has not answered yet).
func answerLines(you, answer *api.SessionTurn, w int, now time.Time) []string {
	if answer.Role == "worker" {
		switch {
		case answer.State == "queued" || answer.State == "running" || answer.State == "accepted" || answer.State == "retry_wait":
			return []string{rule(stWarn.Render(spinner(now)+" worker"), "a copy of this session · "+answer.State, w)}
		case answer.State != "ok" && answer.State != "":
			return []string{rule(stBad.Render("✗ worker"), answer.State, w), "  " + stBad.Render(answer.Text)}
		}
		out := []string{rule(stTool.Render("◇ worker"), "a copy of this session · "+answerMeta(you, answer, now), w), ""}
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
		parts = append(parts, fmt.Sprintf("%d tools", answer.Tools))
	}
	return strings.Join(parts, " · ")
}

// sessionRowLine is one session row: mark, name, then the first line of
// the last answer (or "working · 18s"), age.
func (m *Model) sessionRowLine(r homeRow, selected bool, w int, now time.Time) string {
	sv := r.sess
	fresh := m.newAnswer(sv) && sv.Turn != "busy"
	name := sessionName(sv)
	if sv.Title == "" && m.home.agent != "" {
		name = "session " + api.ShortSession(sv.SessionID)
	}
	nw := m.sessionNameWidth()
	shown := ansi.Truncate(name, nw, "…")
	if fresh {
		shown = stTitle.Render(shown)
	}
	head := " " + m.sessionMark(sv, now) + " " + shown
	head += strings.Repeat(" ", max(2, nw+5-ansi.StringWidth(head)))
	var what string
	switch {
	case sv.Turn == "waiting":
		what = stBad.Render("waits for you in its tab")
	case sv.Turn == "busy":
		what = stWarn.Render("working · " + dur(now.Sub(sv.TurnAt)))
	case sv.LastAnswer != "" && fresh:
		what = stTitle.Render(plainFirstLine(sv.LastAnswer))
	case sv.LastAnswer != "":
		what = stDim.Render(plainFirstLine(sv.LastAnswer))
	case sv.LastPrompt != "":
		what = stDim.Render("› " + sv.LastPrompt)
	default:
		what = stDim.Render(sessionState(sv))
	}
	at := sv.LastAnswerAt
	if sv.Turn == "busy" || at.IsZero() {
		at = r.msg.UpdatedAt
	}
	ageS := stDim.Render(ago(now.Sub(at)))
	w--
	room := w - ansi.StringWidth(head) - ansi.StringWidth(ageS) - 1
	line := head + ansi.Truncate(what, max(0, room), "…")
	line += strings.Repeat(" ", max(1, w-ansi.StringWidth(line)-ansi.StringWidth(ageS))) + ageS
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

// sessionHeader is the first preview line: state, name, agent, tty, time.
func (m *Model) sessionHeader(sv *api.SessionView, w int, now time.Time) string {
	label := map[string]string{"idle": stOK.Render("○ IDLE"), "busy": stWarn.Render("◉ WORKING"),
		"waiting": stBad.Render("! WAITS FOR YOU")}[sv.Turn]
	if label == "" {
		label = stDim.Render("· OPEN")
	}
	var meta []string
	if sv.Agent != "" {
		meta = append(meta, sv.Agent)
	} else {
		meta = append(meta, shortHome(sv.Repo))
	}
	if sv.TTY != "" {
		meta = append(meta, strings.TrimPrefix(sv.TTY, "/dev/"))
	}
	switch {
	case sv.Turn == "busy" && !sv.TurnAt.IsZero():
		meta = append(meta, "for "+dur(now.Sub(sv.TurnAt)))
	case !sv.LastAnswerAt.IsZero():
		meta = append(meta, "answered "+ago(now.Sub(sv.LastAnswerAt))+" ago")
	}
	return ansi.Truncate(label+"  "+stTitle.Render(sessionName(sv))+stDim.Render(" · "+strings.Join(meta, " · ")), w, "…")
}

// sessionBody is the last exchange: earlier answers as one line each, your
// prompt, then the final answer (or one working line). full: the whole
// answer, else what fits in h (cut with a pointer to enter).
func (m *Model) sessionBody(sv *api.SessionView, w, h int, full bool) []string {
	now := m.now()
	turns, err := m.sessionTurns(sv)
	if len(turns) == 0 {
		var out []string
		if err != nil {
			out = append(out, stWarn.Render("Cannot read the conversation: "+oneLine(rpcMessage(err))))
		} else if sv.LastPrompt != "" {
			out = append(out, stAccent.Render("› you"), "  "+sv.LastPrompt)
		} else {
			out = append(out, stDim.Render("No conversation yet."))
		}
		return out
	}
	earlier, you, answer := lastExchange(turns)
	if (sv.Turn == "busy" || sv.Turn == "waiting") && answer != nil && answer.Role == "claude" && you != nil && you.Role == "you" {
		answer = nil // the answer to this prompt is not done yet
	}
	var out []string
	if !full {
		if len(earlier) > 2 {
			earlier = earlier[len(earlier)-2:]
		}
	}
	for _, t := range earlier {
		out = append(out, ansi.Truncate(stDim.Render(clock(t.At, now)+" · "+plainFirstLine(t.Text)), w, "…"))
	}
	if len(earlier) > 0 {
		out = append(out, "")
	}
	if you != nil {
		out = append(out, promptLabel(you)+" "+stDim.Render(clock(you.At, now)))
		ys := strings.Split(ansi.Wrap(you.Text, max(10, w-2), ""), "\n")
		if !full && len(ys) > 4 {
			ys = append(ys[:3], "…")
		}
		for _, l := range ys {
			out = append(out, "  "+l)
		}
		out = append(out, "")
	}
	switch {
	case answer != nil && answer.Role == "worker":
		body := answerLines(you, answer, w, now)
		if !full && h > 0 && len(out)+len(body) > h {
			body = append(body[:max(0, h-len(out)-1)], stDim.Render("… enter shows the whole answer"))
		}
		out = append(out, body...)
	case you != nil && you.Role == "ask" && answer == nil:
		out = append(out, rule(stWarn.Render(spinner(now)+" worker"), "a copy of this session", w))
	case sv.Turn == "waiting":
		out = append(out, rule(stBad.Render("! waits for you"), "for "+dur(now.Sub(sv.TurnAt)), w), "",
			"  It asks for a permission or a question in its tab. o shows the tab.")
	case sv.Turn == "busy":
		meta := "for " + dur(now.Sub(sv.TurnAt))
		if answer == nil {
			if n := len(turns); n > 0 && turns[n-1].Role == "claude" && turns[n-1].Tools > 0 {
				meta += fmt.Sprintf(" · %d tools so far", turns[n-1].Tools)
			}
		}
		out = append(out, rule(stWarn.Render(spinner(now)+" working"), meta, w), "",
			stDim.Render("  The answer shows here when it is done."))
	case answer == nil || answer.Text == "":
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

// canFocus reports a terminal whose tab or pane Mailroom can bring to the
// front (session.focus).
func canFocus(term string) bool {
	return term == "Apple_Terminal" || term == "iTerm.app" || term == "tmux"
}

// canWrite reports whether the human can write to the session now: typed
// in when idle, or to a worker with its context when busy or waiting (the
// worker needs an agent that owns the session's folder).
func canWrite(sv *api.SessionView) bool {
	return sv.CanPrompt || (sv.Turn == "busy" || sv.Turn == "waiting") && sv.Agent != ""
}

// writeHint says where the text goes.
func writeHint(sv *api.SessionView) string {
	name := sessionName(sv)
	switch {
	case sv.CanPrompt:
		return "enter types it into " + name
	case canWrite(sv):
		return name + " is working: enter asks a worker that knows this session (read only); nothing is typed into it"
	case sv.Turn == "busy" || sv.Turn == "waiting":
		return name + " is working, and no agent owns its folder: write when it is idle"
	}
	return "cannot write now: " + sv.Why
}

// sessionKeys is the key line under a session preview.
func sessionKeys(sv *api.SessionView) string {
	var ks []string
	switch {
	case sv.CanPrompt:
		ks = append(ks, stAccent.Render("[s]")+" reply")
	case canWrite(sv):
		ks = append(ks, stAccent.Render("[s]")+" ask a worker")
	}
	ks = append(ks, stAccent.Render("[enter]")+" chat")
	if sv.TTY != "" && canFocus(sv.Term) {
		ks = append(ks, stAccent.Render("[o]")+" show its tab")
	}
	return strings.Join(ks, "   ")
}

// sessionPreview is the right pane for a session row.
func (m *Model) sessionPreview(r homeRow, w, h int) []string {
	sv := r.sess
	out := []string{m.sessionHeader(sv, w, m.now()), ""}
	var bottom []string
	switch {
	case m.home.typing:
		bottom = append([]string{""}, m.home.draft.boxLines(stAccent.Render("› "), w, 6)...)
		bottom = append(bottom, stDim.Render(ansi.Truncate(writeHint(sv)+" · esc cancel", w, "…")))
	case !canWrite(sv):
		bottom = []string{"", stDim.Render(ansi.Truncate(writeHint(sv)+".", w, "…")), sessionKeys(sv)}
	default:
		bottom = []string{"", sessionKeys(sv)}
	}
	out = append(out, m.sessionBody(sv, w, h-len(out)-len(bottom), false)...)
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
// or an ask to a worker) and its final answer.
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
			for _, l := range strings.Split(ansi.Wrap(t.Text, max(10, w-2), ""), "\n") {
				out = append(out, "  "+l)
			}
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
	switch {
	case sv.Turn == "waiting":
		out = append(out, "", rule(stBad.Render("! waits for you"), "for "+dur(now.Sub(sv.TurnAt)), w),
			"  It asks for a permission or a question in its tab. ctrl+o shows the tab.")
	case sv.Turn == "busy":
		out = append(out, "", rule(stWarn.Render(spinner(now)+" "+sessionName(sv)+" is working"), "for "+dur(now.Sub(sv.TurnAt)), w))
	}
	if len(out) == 0 {
		out = []string{stDim.Render("No conversation yet. Write below.")}
	}
	return out
}

// sessionFullLines is the chat with the selected session (enter on a
// session): the conversation, newest at the bottom, and the box to write.
// m.home.fullScroll counts lines up from the bottom.
func (m *Model) sessionFullLines(sv *api.SessionView, w, h int) []string {
	m.markAnswerRead(sv)
	head := []string{m.sessionHeader(sv, w, m.now()), ""}
	bottom := append([]string{""}, m.home.draft.boxLines(stAccent.Render("› "), w, 8)...)
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
	default:
		return nil, false
	}
	m.home.fullScroll = max(0, m.home.fullScroll)
	return nil, true
}
