package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// The home screen (ADR-0010): one list of everything that is or was in
// flight, grouped by what it asks of the human, with a preview of the
// selected item on the right. What needs you is always on top; the rest
// stays quiet when nothing happens. The six-panel overview is the
// dashboard view behind `v`.

// homeGroup is one section of the home list, in screen order.
type homeGroup int

const (
	grpNeeds homeGroup = iota
	grpSessions
	grpRunning
	grpWaiting
	grpDone
	grpDead
	numGroups
)

var groupNames = [numGroups]string{"NEEDS YOU", "SESSIONS", "RUNNING", "QUEUED", "DONE TODAY", "DEAD"}

// homeRow is one message in the home list.
type homeRow struct {
	group  homeGroup
	msg    api.MessageView
	run    *api.RunView // the live run of a running message
	unread bool
	sess   *api.SessionView // a SESSIONS row (msg then holds only the id, agent and turn)
}

// homeState is the home screen's own state.
type homeState struct {
	sel        string // selected message id ("" = the first row)
	agent      string // agent tab ("" = all)
	filter     lineInput
	filtering  bool // the filter box has the keys
	draft      lineInput
	typing     bool // the answer box has the keys
	preview    map[string]*previewEntry
	tails      map[string]*tailEntry // session conversations (sessions.go)
	seen       map[string]time.Time  // last answer the human saw, per session (answer.go)
	seenFile   string                // where seen is kept ("" = memory only)
	full       bool                  // the selected session's whole answer fills the screen
	fullScroll int
	hideQuiet  bool // z: idle sessions whose answer was read are hidden
	picking    bool // p: the saved prompts are open (quick.go)
	pickIdx    int
	newing     bool // n: the new session's name is asked (quick.go)
	newName    lineInput
	newDir     string
	newTerm    string
}

// previewEntry caches the thread of a previewed message.
type previewEntry struct {
	tv      *api.ThreadView
	err     error
	at      time.Time
	loading bool
	// The live card of a running worker: its last tool calls.
	tools     []toolStep
	toolsAt   time.Time
	toolsBusy bool
}

type toolsMsg struct {
	id    string
	steps []toolStep
	at    time.Time
}

type previewMsg struct {
	id  string
	tv  *api.ThreadView
	err error
	at  time.Time
}

// homeActive reports whether the home screen is the screen on top.
func (m *Model) homeActive() bool {
	return !m.dashboard && m.run == nil && m.item == nil && m.digest == nil && m.inbox == nil && !m.help
}

// homeRows builds the list from the last snapshot: filtered by the agent
// tab and the filter text, in group order, newest first inside a group
// (oldest first for NEEDS YOU and QUEUED: the longest wait on top).
func (m *Model) homeRows() []homeRow {
	if m.snap == nil {
		return nil
	}
	s := m.snap
	runs := map[string]*api.RunView{}
	for i := range s.Status.Running {
		r := &s.Status.Running[i]
		runs[r.MessageID] = r
	}
	today := dayStart(m.now())
	var rows []homeRow
	add := func(g homeGroup, xs []api.MessageView) {
		for _, x := range xs {
			if g == grpDone && (x.UpdatedAt.Before(today) || x.Kind == "reply") {
				continue // replies show in their thread, not as rows of their own
			}
			rows = append(rows, homeRow{group: g, msg: x, run: runs[x.ID], unread: s.Unread[x.ID]})
		}
	}
	add(grpNeeds, s.NeedsYou)
	for i := range s.Sessions {
		if m.home.hideQuiet && m.sessionKind(&s.Sessions[i]) == kQuiet {
			continue
		}
		rows = append(rows, sessionRow(&s.Sessions[i]))
	}
	add(grpRunning, s.Running)
	add(grpWaiting, s.Waiting)
	add(grpDone, s.Done)
	add(grpDead, s.DLQ)
	q := strings.ToLower(strings.TrimSpace(m.home.filter.Value()))
	out := rows[:0]
	for _, r := range rows {
		if m.home.agent != "" && r.msg.To != m.home.agent {
			continue
		}
		hay := []string{r.msg.To, r.msg.From.Agent, r.msg.Intent, r.msg.Subject, r.msg.Summary, r.msg.HumanQuestion}
		if r.sess != nil {
			hay = append(hay, sessionName(r.sess), shortHome(r.sess.Repo), r.sess.LastAnswer)
		}
		if q != "" && !strings.Contains(strings.ToLower(strings.Join(hay, " ")), q) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if a.group == grpSessions {
			// A fixed order (root folder, name, agent, id): rows do not
			// move when a session turns idle or busy; the mark shows the state.
			if ar, br := sessionRoot(a.sess), sessionRoot(b.sess); ar != br {
				return ar < br
			}
			an, bn := strings.ToLower(sessionName(a.sess)), strings.ToLower(sessionName(b.sess))
			if an != bn {
				return an < bn
			}
			if a.msg.To != b.msg.To {
				return a.msg.To < b.msg.To
			}
			return a.sess.SessionID < b.sess.SessionID
		}
		if a.group == grpNeeds || a.group == grpWaiting {
			return a.msg.UpdatedAt.Before(b.msg.UpdatedAt)
		}
		return a.msg.UpdatedAt.After(b.msg.UpdatedAt)
	})
	return out
}

func dayStart(t time.Time) time.Time {
	y, mo, d := t.Local().Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
}

// homeIndex is the index of the selected row (0 when it is gone).
func (m *Model) homeIndex(rows []homeRow) int {
	for i, r := range rows {
		if r.msg.ID == m.home.sel {
			return i
		}
	}
	return 0
}

// homeSelected is the selected row, or nil when the list is empty.
func (m *Model) homeSelected() *homeRow {
	rows := m.homeRows()
	if len(rows) == 0 {
		return nil
	}
	r := rows[m.homeIndex(rows)]
	return &r
}

// HomeSelected is the id of the selected message on the home screen.
func (m *Model) HomeSelected() string {
	if r := m.homeSelected(); r != nil {
		return r.msg.ID
	}
	return ""
}

// HomeAgent is the agent tab of the home screen ("" = all).
func (m *Model) HomeAgent() string { return m.home.agent }

func (m *Model) homeMove(step int) tea.Cmd {
	rows := m.homeRows()
	if len(rows) == 0 {
		return nil
	}
	i := max(0, min(len(rows)-1, m.homeIndex(rows)+step))
	m.home.sel = rows[i].msg.ID
	if rows[i].sess != nil {
		m.markAnswerRead(rows[i].sess) // moving onto a session reads its answer
	}
	return m.loadPreview(false)
}

// homeJumpGroup moves to the first row of the next (or previous) group
// that has rows.
func (m *Model) homeJumpGroup(step int) tea.Cmd {
	rows := m.homeRows()
	if len(rows) == 0 {
		return nil
	}
	cur := rows[m.homeIndex(rows)].group
	for g := cur + homeGroup(step); g >= 0 && g < numGroups; g += homeGroup(step) {
		for _, r := range rows {
			if r.group == g {
				m.home.sel = r.msg.ID
				if r.sess != nil {
					m.markAnswerRead(r.sess)
				}
				return m.loadPreview(false)
			}
		}
	}
	return nil
}

// homeAgents is the tab list: every configured agent, in config order.
func (m *Model) homeAgents() []string {
	if m.snap == nil {
		return nil
	}
	var ids []string
	for _, a := range m.snap.Status.Agents {
		ids = append(ids, a.ID)
	}
	return ids
}

func (m *Model) homeCycleAgent(step int) tea.Cmd {
	tabs := append([]string{""}, m.homeAgents()...)
	i := 0
	for j, t := range tabs {
		if t == m.home.agent {
			i = j
		}
	}
	m.home.agent = tabs[(i+step+len(tabs))%len(tabs)]
	m.home.sel = ""
	return m.loadPreview(false)
}

// loadTools reads the tail of the selected running worker's transcript
// (every poll while it runs).
func (m *Model) loadTools() tea.Cmd {
	r := m.homeSelected()
	if r == nil || r.run == nil || r.run.Transcript == "" || m.insp == nil {
		return nil
	}
	if m.home.preview == nil {
		m.home.preview = map[string]*previewEntry{}
	}
	e := m.home.preview[r.msg.ID]
	if e == nil {
		e = &previewEntry{}
		m.home.preview[r.msg.ID] = e
	}
	if e.toolsBusy || m.now().Sub(e.toolsAt) < m.interval {
		return nil
	}
	e.toolsBusy = true
	insp, run, id, now := m.insp, *r.run, r.msg.ID, m.now
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t, err := insp.Transcript(ctx, run)
		if err != nil || t == nil {
			return toolsMsg{id: id, at: now()}
		}
		return toolsMsg{id: id, steps: toolTrail(t.Data, 8), at: now()}
	}
}

func (m *Model) onTools(msg toolsMsg) {
	if e := m.home.preview[msg.id]; e != nil {
		e.toolsBusy, e.toolsAt = false, msg.at
		if msg.steps != nil {
			e.tools = msg.steps
		}
	}
}

// loadPreview reads the thread of the selected message when the cache is
// missing or older than two poll intervals (force: always).
func (m *Model) loadPreview(force bool) tea.Cmd {
	r := m.homeSelected()
	if r != nil && r.sess != nil {
		return m.loadSessionTail(r, force)
	}
	if r == nil || m.act == nil {
		return nil
	}
	if m.home.preview == nil {
		m.home.preview = map[string]*previewEntry{}
	}
	id := r.msg.ID
	e := m.home.preview[id]
	if e != nil && (e.loading || (!force && m.now().Sub(e.at) < 2*m.interval)) {
		return m.loadTools()
	}
	if e == nil {
		e = &previewEntry{}
		m.home.preview[id] = e
	}
	e.loading = true
	act, now := m.act, m.now
	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tv, err := act.Thread(ctx, id)
		return previewMsg{id: id, tv: tv, err: err, at: now()}
	}, m.loadTools())
}

func (m *Model) onPreview(msg previewMsg) {
	if m.home.preview == nil {
		m.home.preview = map[string]*previewEntry{}
	}
	e := m.home.preview[msg.id]
	if e == nil {
		e = &previewEntry{}
		m.home.preview[msg.id] = e
	}
	e.loading, e.at = false, msg.at
	if msg.err != nil {
		e.err = msg.err
		return
	}
	e.tv, e.err = msg.tv, nil
	// Keep the cache small: the selected item and the last few.
	if len(m.home.preview) > 32 {
		for id := range m.home.preview {
			if id != msg.id {
				delete(m.home.preview, id)
			}
		}
	}
}

// homeKey handles a key on the home screen.
func (m *Model) homeKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.home.typing {
		return m.homeAnswerKey(msg)
	}
	if m.home.picking {
		return m.pickKey(k)
	}
	if m.home.newing {
		return m.newKey(msg)
	}
	if m.home.filtering {
		switch k {
		case "enter":
			m.home.filtering = false
		case "esc":
			m.home.filtering = false
			m.home.filter = lineInput{}
		default:
			m.home.filter.edit(msg)
		}
		m.home.sel = ""
		return m.loadPreview(false)
	}
	r := m.homeSelected()
	switch k {
	case "q":
		return tea.Quit
	case "?":
		m.openHelp()
	case "up", "k":
		return m.homeMove(-1)
	case "down", "j":
		return m.homeMove(1)
	case "home", "g":
		return m.homeMove(-1 << 20)
	case "end", "G":
		return m.homeMove(1 << 20)
	case "tab", "shift+tab":
		// The next row that needs you (a waiting session, a new answer, a
		// question, a dead letter); with none, the next group.
		step := 1
		if k == "shift+tab" {
			step = -1
		}
		if m.homeNextAttention(step, false) {
			return m.loadPreview(false)
		}
		return m.homeJumpGroup(step)
	case "z":
		m.home.hideQuiet = !m.home.hideQuiet
		if m.home.hideQuiet {
			m.setStatus("Idle sessions you have read are hidden; z shows them.", false)
		}
		return m.loadPreview(false)
	case "]", "right", "l":
		return m.homeCycleAgent(1)
	case "[", "left", "h":
		return m.homeCycleAgent(-1)
	case "0":
		m.home.agent, m.home.sel = "", ""
		return m.loadPreview(false)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		tabs := m.homeAgents()
		if i := int(k[0] - '1'); i < len(tabs) {
			m.home.agent, m.home.sel = tabs[i], ""
			return m.loadPreview(false)
		}
	case "/":
		m.home.filtering = true
	case ":":
		m.prompt = &promptState{}
	case "esc":
		m.home.filter = lineInput{}
		m.home.agent = ""
	case "r":
		if !m.fetching {
			m.fetching = true
			return tea.Batch(m.fetch(), m.loadPreview(true))
		}
	case "v":
		m.dashboard = true
	case "i":
		return m.openInbox(m.home.agent)
	case "d":
		return m.openDigest()
	case "e":
		return m.homeMarkRead()
	case "s":
		if r != nil && r.sess != nil {
			return m.sessionPromptOpen(r)
		}
	case "o":
		if r != nil && r.sess != nil {
			return m.focusSession(r.sess)
		}
	case "p":
		if r != nil && r.sess != nil {
			m.pickOpen(r)
		}
	case "n":
		m.newOpen()
	case "enter":
		if r == nil {
			return nil
		}
		if r.sess != nil {
			m.home.full, m.home.fullScroll, m.home.typing = true, 0, true
			return m.loadPreview(true)
		}
		if r.run != nil && r.group == grpRunning {
			run := *r.run
			return m.openRun(run.ID, &run)
		}
		return m.openMessage(r.msg)
	case "a":
		if r != nil && canReply(r.msg.State) {
			if m.act == nil {
				m.setStatus("This UI has no daemon connection for actions.", true)
				return nil
			}
			m.home.typing = true
		}
	case "c":
		if r != nil && canCancel(r.msg.State) {
			x := r.msg
			if r.run != nil {
				x.Subject = "run " + Short(r.run.ID) + ", attempt " + itoa(r.run.Attempt)
			}
			m.askCancel(x)
		}
	case "t":
		if r != nil && canRetry(r.msg.State) {
			m.askRetry(r.msg)
		}
	}
	return nil
}

// homeAnswerKey edits the answer box under the preview.
func (m *Model) homeAnswerKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.home.full {
		if r := m.homeSelected(); r != nil && r.sess != nil {
			if cmd, ok := m.chatKey(r, msg.String()); ok {
				return cmd
			}
		} else {
			m.home.full, m.home.typing = false, false
			return nil
		}
	}
	if m.home.draft.edit(msg) {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.home.typing = false
	case "enter":
		r := m.homeSelected()
		text := strings.TrimSpace(m.home.draft.Value())
		switch {
		case r != nil && r.sess != nil && text == "":
			m.setStatus("The prompt is empty; type it first.", true)
		case r != nil && r.sess != nil:
			return m.sendPrompt(r, text)
		case r == nil || !canReply(r.msg.State):
			m.home.typing = false
			m.setStatus("That item no longer waits for an answer.", true)
		case text == "":
			m.setStatus("The answer is empty; type it first.", true)
		default:
			id := r.msg.ID
			return m.act1("reply", id, func(ctx context.Context, a Actions) (*api.SendResult, error) {
				return a.Reply(ctx, id, text)
			})
		}
	}
	return nil
}

// homeMarkRead marks the handled items read up to the cursor the screen
// showed (digest.get with advance and until, ADR-0008).
func (m *Model) homeMarkRead() tea.Cmd {
	if m.snap == nil || len(m.snap.Unread) == 0 {
		m.setStatus("Nothing unread.", false)
		return nil
	}
	if m.digr == nil {
		m.setStatus("This UI cannot mark items read.", true)
		return nil
	}
	n, until, digr := len(m.snap.Unread), m.snap.DigestCursor, m.digr
	m.setStatus(fmt.Sprintf("Marking %d items read…", n), false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := digr.Digest(ctx, api.DigestParams{Since: "last", Advance: true, Until: until, Limit: DigestLimit})
		return digestMarkMsg{res: res, err: err, until: until}
	}
}

// homeTop is the one-line header: the name, the session counts by state,
// what asks for you, the agent filter, and whether the view is live.
func (m *Model) homeTop(w int) string {
	parts := []string{stBadge.Render(" smif ")}
	c := m.sessionCounts()
	if c[kWaiting] > 0 {
		parts = append(parts, stWarn.Render(fmt.Sprintf("%d waiting", c[kWaiting])))
	}
	if c[kNew] > 0 {
		parts = append(parts, stNew.Render(fmt.Sprintf("%d new", c[kNew])))
	}
	if c[kWorking] > 0 {
		parts = append(parts, stLive.Render(fmt.Sprintf("%d working", c[kWorking])))
	}
	if c[kQuiet] > 0 {
		parts = append(parts, stDim.Render(fmt.Sprintf("%d quiet", c[kQuiet])))
	}
	if m.snap != nil {
		if n := len(m.snap.NeedsYou); n > 0 {
			parts = append(parts, stBad.Render(fmt.Sprintf("⚑ %d asks you", n)))
		}
		if n := len(m.snap.DLQ); n > 0 {
			parts = append(parts, stBad.Render(fmt.Sprintf("✗ %d dead", n)))
		}
	}
	if m.home.agent != "" {
		parts = append(parts, stTabOn.Render("agent "+m.home.agent)+stDim.Render(" ([ ] 0)"))
	}
	right := ""
	switch {
	case m.lastErr != nil:
		right = stBad.Render("● DAEMON DOWN") + stDim.Render(" retrying")
	case m.snap != nil:
		spent := 0.0
		for _, a := range m.snap.Status.Agents {
			spent += a.SpentUSD
		}
		right = stOK.Render("● live")
		if age := m.now().Sub(m.snap.At); !m.snap.At.IsZero() && age > 5*time.Second+3*m.interval {
			right = stWarn.Render("● " + ago(age) + " old")
		}
		if !m.snap.Status.Daemon.ClaudeOK {
			right = stWarn.Render("● claude unavailable")
		}
		if spent > 0 {
			right += stDim.Render(" · ≈ " + money(spent) + " at API prices today")
		}
	}
	line := strings.Join(parts, "  ")
	gap := w - ansi.StringWidth(line) - ansi.StringWidth(right)
	if gap < 1 {
		return ansi.Truncate(line, max(0, w-ansi.StringWidth(right)-1), "…") + " " + right
	}
	return line + strings.Repeat(" ", gap) + right
}

// homeRowsAll is homeRows without the agent tab (for the tab counts).
func (m *Model) homeRowsAll() []homeRow {
	a := m.home.agent
	m.home.agent = ""
	defer func() { m.home.agent = a }()
	return m.homeRows()
}

// homeLines draws the list and the preview in avail lines.
func (m *Model) homeLines(w, avail int) []string {
	rows := m.homeRows()
	sel := m.homeIndex(rows)
	if len(rows) == 0 {
		sel = -1
	}
	if m.home.full && sel >= 0 && rows[sel].sess != nil {
		return box("", m.sessionFullLines(rows[sel].sess, w-4, avail-2), w, avail, true)
	}
	// Narrow terminals get the list alone; enter opens the item.
	listW := w
	if w >= 90 {
		listW = max(44, w/2)
	}
	list := m.homeList(rows, sel, listW-4, avail-2)
	out := box(m.homeListTitle(), list, listW, avail, !m.home.typing)
	if listW == w {
		return out
	}
	var pv []string
	if sel >= 0 {
		pv = m.previewLines(rows[sel], w-listW-1-4, avail-2)
	} else {
		pv = m.emptyPreview(w - listW - 1 - 4)
	}
	right := box("", pv, w-listW-1, avail, m.home.typing)
	for i := range out {
		out[i] += " " + right[i]
	}
	return out
}

func (m *Model) homeListTitle() string {
	t := ""
	if m.home.agent != "" {
		t = m.home.agent
	}
	if f := m.home.filter.Value(); f != "" || m.home.filtering {
		if t != "" {
			t += " · "
		}
		if m.home.filtering {
			t += "/" + m.home.filter.view(30)
		} else {
			t += "/" + f
		}
	}
	return t
}

// homeList renders the grouped rows, scrolled so the selection shows.
func (m *Model) homeList(rows []homeRow, sel, w, h int) []string {
	var lines []string
	selLine := 0
	byGroup := map[homeGroup][]int{}
	for i, r := range rows {
		byGroup[r.group] = append(byGroup[r.group], i)
	}
	now := m.now()
	for g := homeGroup(0); g < numGroups; g++ {
		idx := byGroup[g]
		if len(idx) == 0 && !(g == grpSessions && m.home.hideQuiet) {
			continue // an empty group takes no room
		}
		head := fmt.Sprintf("%s · %d", groupNames[g], len(idx))
		if g == grpDone {
			unread := 0
			for _, i := range idx {
				if rows[i].unread {
					unread++
				}
			}
			if unread > 0 {
				head += stDim.Render(fmt.Sprintf(" · %d unread (e marks read)", unread))
			}
		}
		if g == grpSessions && m.home.hideQuiet {
			if n := m.sessionCounts()[kQuiet]; n > 0 {
				head += stDim.Render(fmt.Sprintf(" · %d quiet hidden (z)", n))
			}
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, groupStyle(g, len(idx) > 0).Render(head))
		// Sessions in more than one root folder get a line per folder.
		roots, root := map[string]int{}, ""
		if g == grpSessions {
			for _, i := range idx {
				roots[sessionRoot(rows[i].sess)]++
			}
		}
		for _, i := range idx {
			rw := w
			if len(roots) > 1 {
				if r := sessionRoot(rows[i].sess); r != root {
					root = r
					lines = append(lines, stDim.Render(fmt.Sprintf("  %s · %d", shortHome(r), roots[r])))
				}
				rw = w - 2
			}
			if i == sel {
				selLine = len(lines)
			}
			line := m.homeRowLine(rows[i], i == sel, rw, now)
			if rw != w {
				line = "  " + line
			}
			lines = append(lines, line)
		}
	}
	if len(rows) == 0 && m.snap != nil {
		lines = append(lines, "", stDim.Render(m.quietLine(now)))
	}
	// Scroll: keep the selected line (and its group head) in view.
	start := 0
	if selLine >= h {
		start = selLine - h + 1
	}
	if start > 0 && start < len(lines) {
		lines = lines[start:]
	}
	return lines
}

// quietLine is the empty state: when the last thing happened.
func (m *Model) quietLine(now time.Time) string {
	if m.home.agent != "" || m.home.filter.Value() != "" {
		return "Nothing matches. esc clears the tab and the filter."
	}
	last := time.Time{}
	for _, x := range m.snap.Recent {
		if x.UpdatedAt.After(last) {
			last = x.UpdatedAt
		}
	}
	if last.IsZero() {
		return "All quiet. No messages yet."
	}
	return "All quiet. Last activity " + dur(now.Sub(last)) + " ago."
}

func groupStyle(g homeGroup, any bool) interface{ Render(...string) string } {
	if !any {
		return stDim
	}
	switch g {
	case grpNeeds, grpDead:
		return stBad
	case grpRunning:
		return stWarn
	case grpSessions:
		return stTitle
	case grpDone:
		return stOK
	}
	return stTitle
}

// homeRowLine is one list row: mark, agent ← sender, subject, age.
func (m *Model) homeRowLine(r homeRow, selected bool, w int, now time.Time) string {
	if r.sess != nil {
		return m.sessionRowLine(r, selected, w, now)
	}
	x := r.msg
	mark := map[homeGroup]string{grpNeeds: stBad.Render("⚑"), grpRunning: stWarn.Render("◉"), grpWaiting: "◌",
		grpDone: stOK.Render("✓"), grpDead: stBad.Render("✗")}[r.group]
	if r.group == grpDone && x.State != "done" {
		mark = stDim.Render("·") // rejected or expired today
	}
	dot := " "
	if r.unread {
		dot = stNew.Render("•")
	}
	age := now.Sub(x.UpdatedAt)
	if r.run != nil {
		age = now.Sub(r.run.StartedAt)
	}
	what := x.Subject
	if what == "" {
		what = x.Intent
	}
	if r.group == grpWaiting && x.State == "retry_wait" {
		what = "retry · " + what
	}
	who := x.To
	if m.home.agent != "" {
		who = ""
	}
	from := ""
	if x.From.Agent != "" {
		from = stDim.Render("← " + x.From.Agent)
	}
	ageS := stDim.Render(ago(age))
	parts := []string{dot + mark}
	if who != "" {
		parts = append(parts, who)
	}
	if from != "" {
		parts = append(parts, from)
	}
	head := strings.Join(parts, " ")
	w-- // the selection mark column
	room := w - ansi.StringWidth(head) - ansi.StringWidth(ageS) - 2
	line := head + " " + ansi.Truncate(oneLine(what), max(0, room), "…")
	line += strings.Repeat(" ", max(1, w-ansi.StringWidth(line)-ansi.StringWidth(ageS))) + ageS
	if selected {
		return stSelRow.Render("▌" + line)
	}
	return " " + line
}

// spinner is a frame of the activity mark, one per second.
func spinner(now time.Time) string {
	return [...]string{"◐", "◓", "◑", "◒"}[now.Unix()%4]
}

// ago is a short age: 40s, 4m, 2h, 3d.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}

func (m *Model) emptyPreview(w int) []string {
	if m.snap == nil {
		return []string{stDim.Render("connecting to the daemon…")}
	}
	return []string{
		stDim.Render("Nothing selected."),
		"",
		stDim.Render(ansi.Wrap("Messages that need you, running workers, the queue and what was handled today show here. "+
			"v opens the dashboard (agents, events), i the full inbox.", max(20, w), "")),
	}
}

// previewLines is the right pane for one row.
func (m *Model) previewLines(r homeRow, w, h int) []string {
	if r.sess != nil {
		return m.sessionPreview(r, w, h)
	}
	x := r.msg
	now := m.now()
	var e *previewEntry
	if m.home.preview != nil {
		e = m.home.preview[x.ID]
	}
	cur := x
	if e != nil && e.tv != nil {
		for _, y := range e.tv.Messages {
			if y.ID == x.ID {
				cur = y
			}
		}
	}
	var out []string
	wrap := func(st interface{ Render(...string) string }, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, max(10, w), ""), "\n") {
			if st != nil {
				l = st.Render(l)
			}
			out = append(out, l)
		}
	}
	section := func(name string) { out = append(out, "", stDim.Render(name)) }
	label := map[homeGroup]string{grpNeeds: stBad.Render("⚑ NEEDS YOU"), grpRunning: stWarn.Render("◉ RUNNING"),
		grpWaiting: "◌ " + strings.ToUpper(strings.ReplaceAll(cur.State, "_", " ")), grpDone: stOK.Render("✓ " + strings.ToUpper(cur.State)),
		grpDead: stBad.Render("✗ DEAD LETTER")}[r.group]
	out = append(out, label+"  "+stDim.Render(fmt.Sprintf("%s → %s · %s %s · %s", dash(cur.From.Agent), cur.To, cur.Kind, cur.Intent,
		ago(now.Sub(cur.UpdatedAt))+" ago")))
	out = append(out, "")
	subj := cur.Subject
	if subj == "" {
		subj = "(no subject)"
	}
	wrap(stTitle, subj)
	switch r.group {
	case grpNeeds:
		if cur.HumanQuestion != "" {
			section("THE WORKER ASKS")
			wrap(stTitle, cur.HumanQuestion)
		}
		if cur.Summary != "" {
			section("WORKER SAYS")
			wrap(nil, cur.Summary)
		}
	case grpRunning:
		if run := r.run; run != nil {
			hd := run.Handler
			if run.Profile != "" {
				hd += "/" + run.Profile
			}
			section("WORKER · LIVE")
			out = append(out, fmt.Sprintf("%s · attempt %d · %s · %s", hd, run.Attempt, dur(now.Sub(run.StartedAt)), money(run.CostUSD)))
			switch {
			case e != nil && len(e.tools) > 0:
				for i, st := range e.tools {
					mark := stOK.Render("✓")
					switch {
					case st.Err:
						mark = stBad.Render("✗")
					case !st.Done && i == len(e.tools)-1:
						mark = stWarn.Render(spinner(now))
					case !st.Done:
						mark = stDim.Render("·")
					}
					out = append(out, ansi.Truncate(fmt.Sprintf("  %s %-5s %s", mark, st.Name, st.Arg), w, "…"))
				}
			case run.Handler == "worker":
				out = append(out, stDim.Render("  "+spinner(now)+" starting…"))
			}
		}
	case grpWaiting:
		if cur.HoldReason != "" {
			section("WAITING BECAUSE")
			wrap(nil, cur.HoldReason)
		}
		if cur.Reason != "" {
			section("LAST ATTEMPT")
			wrap(nil, cur.Reason)
		}
	case grpDone:
		if cur.Summary != "" {
			section("RESULT")
			wrap(nil, cur.Summary)
		}
	case grpDead:
		section("WHY")
		wrap(stBad, dash(cur.Reason))
		out = append(out, stDim.Render(fmt.Sprintf("after %d attempts · t puts it back in the queue", cur.Attempt)))
	}
	if cur.Body != "" && strings.TrimSpace(cur.Body) != strings.TrimSpace(cur.Subject) {
		section("MESSAGE")
		body := strings.Split(ansi.Wrap(cur.Body, max(10, w), ""), "\n")
		if len(body) > 8 {
			body = append(body[:8], "… (enter shows all)")
		}
		out = append(out, body...)
	}
	switch {
	case e == nil || (e.tv == nil && e.err == nil):
		if m.act != nil {
			section("THREAD")
			out = append(out, stDim.Render("loading…"))
		}
	case e.tv == nil && e.err != nil:
		section("THREAD")
		out = append(out, stBad.Render("cannot load: "+oneLine(e.err.Error())))
	default:
		tv := e.tv
		section(fmt.Sprintf("THREAD · %d messages · %d runs", len(tv.Messages), len(tv.Runs)))
		for _, y := range tv.Messages {
			mark := "  "
			if y.ID == x.ID {
				mark = stAccent.Render("▸ ")
			}
			out = append(out, ansi.Truncate(fmt.Sprintf("%s%s  %s → %s  %s  %s", mark, y.CreatedAt.Local().Format("15:04"),
				dash(y.From.Agent), y.To, y.Kind, oneLine(summary(y))), w, "…"))
		}
	}
	// Actions, then the answer box pinned to the bottom.
	var acts []string
	if canReply(cur.State) {
		acts = append(acts, stAccent.Render("[a]")+" answer")
	}
	acts = append(acts, stAccent.Render("[enter]")+" open")
	if canRetry(cur.State) {
		acts = append(acts, stAccent.Render("[t]")+" retry")
	}
	if canCancel(cur.State) {
		acts = append(acts, stAccent.Render("[c]")+" cancel")
	}
	out = append(out, "", strings.Join(acts, "   "))
	var bottom []string
	if m.home.typing {
		bottom = append(m.home.draft.boxLines(stAccent.Render("› "), w, 6), stDim.Render("enter send · alt+enter new line · esc cancel"))
	}
	keep := h - len(bottom)
	if len(out) > keep {
		out = out[:max(0, keep)]
	}
	for len(out) < keep {
		out = append(out, "")
	}
	return append(out, bottom...)
}

// box draws lines in a rounded frame w wide and h tall (title in the top
// edge). focused frames use the accent colour.
func box(title string, lines []string, w, h int, focused bool) []string {
	if w < 4 || h < 2 {
		return make([]string, max(0, h))
	}
	st := stFrame
	if focused {
		st = stFrameOn
	}
	inner := w - 4
	top := "╭─"
	if title != "" {
		t := " " + ansi.Truncate(title, max(0, inner-2), "…") + " "
		top += t
	}
	top += strings.Repeat("─", max(0, w-1-ansi.StringWidth(top))) + "╮"
	out := []string{st.Render(top)}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = ansi.Truncate(lines[i], inner, "…")
		}
		l += strings.Repeat(" ", max(0, inner-ansi.StringWidth(l)))
		out = append(out, st.Render("│")+" "+l+" "+st.Render("│"))
	}
	out = append(out, st.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

// homeKeys is the footer of the home screen, for the selected row.
func (m *Model) homeKeys() string {
	if m.home.typing {
		if r := m.homeSelected(); r != nil && r.sess != nil {
			if m.home.full {
				return "type to write · enter send · ↑↓ pgup pgdn scroll · tab next that needs you · ctrl+o its tab · esc back · ctrl+c quit"
			}
			return "enter sends it (typed in when idle, else to a fork) · esc cancel · ctrl+c quit"
		}
		return "enter send · alt+enter new line · esc cancel · ctrl+c quit"
	}
	if m.home.filtering {
		return "type to filter · enter keep · esc clear"
	}
	if m.home.newing {
		return m.newLine()
	}
	if m.home.picking {
		return "↑↓ choose · enter or 1-9 sends · esc closes"
	}
	if m.prompt != nil {
		return "tab complete · ↑↓ choose · enter run · esc close"
	}
	if r := m.homeSelected(); r != nil && r.sess != nil {
		if m.home.full {
			return "type to write · enter send · ↑↓ pgup pgdn scroll · tab next that needs you · ctrl+o its tab · esc back"
		}
		var ks []string
		focus := r.sess.TTY != "" && canFocus(r.sess.Term)
		if focus && r.sess.Turn == "waiting" {
			ks = append(ks, "o answer in its tab")
		}
		ks = append(ks, "↑↓ move", "tab next that needs you", "enter chat")
		switch {
		case r.sess.CanPrompt:
			ks = append(ks, "s write")
		case canWrite(r.sess):
			ks = append(ks, "s ask a fork")
		}
		if canWrite(r.sess) && len(m.prompts()) > 0 {
			ks = append(ks, "p prompts")
		}
		ks = append(ks, "n new session")
		if focus && r.sess.Turn != "waiting" {
			ks = append(ks, "o its tab")
		}
		quiet := "z hide quiet"
		if m.home.hideQuiet {
			quiet = "z show quiet"
		}
		return strings.Join(append(ks, "/ filter", quiet, ": command", "v dashboard", "? help", "q quit"), " · ")
	}
	ks := []string{"↑↓ move", "enter open"}
	if r := m.homeSelected(); r != nil {
		if canReply(r.msg.State) {
			ks = append(ks, "a answer")
		}
		if canRetry(r.msg.State) {
			ks = append(ks, "t retry")
		}
		if canCancel(r.msg.State) {
			ks = append(ks, "c cancel")
		}
	}
	if m.snap != nil && len(m.snap.Unread) > 0 {
		ks = append(ks, "e mark read")
	}
	return strings.Join(append(ks, ": command", "[ ] agent", "/ filter", "v dashboard", "? help", "q quit"), " · ")
}
