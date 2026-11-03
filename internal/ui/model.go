package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/mailroom/internal/api"
)

// Panel is one section of the overview.
type Panel int

// The overview panels, in tab order.
const (
	PanelAgents Panel = iota
	PanelRunning
	PanelNeedsYou
	PanelDLQ
	PanelRecent
	PanelEvents
	numPanels
)

var panelNames = [numPanels]string{"AGENTS", "RUNNING", "NEEDS YOU", "DEAD LETTERS", "RECENT ACTIVITY", "EVENTS"}

func (p Panel) String() string { return panelNames[p] }

// Options configure a Model.
type Options struct {
	// Interval between polls (default 1 s).
	Interval time.Duration
	// Now is the clock for elapsed times (default time.Now).
	Now func() time.Time
	// Dashboard starts on the six-panel overview instead of the home
	// screen (`v` switches between them).
	Dashboard bool
	// SeenFile keeps which session answers the human has seen, across
	// restarts ("" = in memory only, as in tests).
	SeenFile string
}

// Model is the Bubble Tea model of `mailroom ui`.
type Model struct {
	src      Source
	interval time.Duration
	now      func() time.Time

	width, height int
	snap          *Snapshot // last good snapshot
	lastErr       error     // set while the daemon is down
	lastErrAt     time.Time
	fetching      bool
	focus         Panel
	cursor        [numPanels]int
	dashboard     bool         // the six-panel overview instead of the home screen
	home          homeState    // the home screen
	prompt        *promptState // the open `:` prompt, or nil
	help          bool
	helpScroll    int // first help line shown

	act       Actions                // nil: read-only (the source cannot act)
	insp      Inspector              // nil: no inbox, run or apply screens
	digr      Digester               // nil: no digest screen
	inbox     *inboxScreen           // open inbox list, or nil
	digest    *digestScreen          // open digest list, or nil
	item      *itemScreen            // open thread screen (over the inbox), or nil
	run       *runScreen             // open run screen (over the thread), or nil
	confirm   *confirmation          // open yes/no question
	busy      string                 // the action in flight ("" when none)
	cancels   map[string]*cancelWait // accepted cancels, by message id, until the item leaves running
	status    string                 // result of the last action
	statusBad bool
	statusAt  time.Time
}

// SnapshotMsg delivers a poll result.
type SnapshotMsg Snapshot

type tickMsg time.Time

// New returns a model that polls src.
func New(src Source, o Options) *Model {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Model{src: src, interval: o.Interval, now: o.Now, dashboard: o.Dashboard}
	m.home.seenFile = o.SeenFile
	m.home.seen = loadSeen(o.SeenFile)
	if a, ok := src.(Actions); ok {
		m.act = a
	}
	if in, ok := src.(Inspector); ok {
		m.insp = in
	}
	if d, ok := src.(Digester); ok {
		m.digr = d
	}
	return m
}

// Init starts the first poll.
func (m *Model) Init() tea.Cmd {
	m.fetching = true
	return m.fetch()
}

func (m *Model) fetch() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return SnapshotMsg(src.Fetch(ctx))
	}
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles keys, window sizes, ticks and poll results.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		if !m.fetching {
			m.fetching = true
			return m, m.fetch()
		}
	case SnapshotMsg:
		m.fetching = false
		s := Snapshot(msg)
		if s.Err != nil {
			m.lastErr, m.lastErrAt = s.Err, s.At
		} else {
			sel := m.selectedEventSeq()
			m.snap, m.lastErr = &s, nil
			m.noteSessions()
			m.keepEventCursor(sel)
			m.clamp()
		}
		return m, tea.Batch(m.tick(), m.reloadTop(), m.checkCancels())
	case threadMsg:
		m.onThread(msg)
	case actionMsg:
		return m, m.onAction(msg)
	case cancelCheckMsg:
		m.onCancelCheck(msg)
	case inboxMsg:
		return m, m.onInbox(msg)
	case runMsg:
		return m, m.onRun(msg)
	case previewMsg:
		m.onPreview(msg)
	case toolsMsg:
		m.onTools(msg)
	case promptedMsg:
		return m, m.onPrompted(msg)
	case tailMsg:
		m.onTail(msg)
	case focusedMsg:
		m.onFocused(msg)
	case sentMsg:
		return m, m.onSent(msg)
	case transcriptMsg:
		m.onTranscript(msg)
	case diffMsg:
		m.onDiff(msg)
	case applyMsg:
		return m, m.onApply(msg)
	case digestMsg:
		m.onDigest(msg)
	case digestMarkMsg:
		return m, m.onDigestMark(msg)
	case tea.KeyPressMsg:
		return m, m.keyMsg(msg)
	case tea.PasteMsg:
		// A paste goes into the reply box as text; elsewhere it is ignored,
		// so pasted words never act as key bindings.
		switch {
		case m.confirm != nil:
		case m.prompt != nil:
			m.prompt.in.insert([]rune(strings.ReplaceAll(msg.Content, "\n", " ")))
		case m.item != nil && m.item.typing:
			m.item.draft.insert([]rune(msg.Content))
		case m.homeActive() && m.home.typing:
			m.home.draft.insert([]rune(msg.Content))
		case m.homeActive() && m.home.filtering:
			m.home.filter.insert([]rune(strings.ReplaceAll(msg.Content, "\n", " ")))
		}
	}
	return m, nil
}

// keyMsg routes a key: a confirmation takes the next key, the reply box
// takes every key but ctrl+c, then help, the item screen, the overview.
func (m *Model) keyMsg(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		return tea.Quit
	}
	if m.prompt != nil {
		return m.promptKey(msg)
	}
	if c := m.confirm; c != nil {
		m.confirm = nil
		if k == "y" || k == "Y" {
			return c.yes()
		}
		m.setStatus("Not done: "+c.what+".", false)
		return nil
	}
	if m.help {
		return m.key(k)
	}
	switch {
	case m.run != nil:
		return m.runKey(k)
	case m.item != nil && m.item.typing:
		return m.inputKey(msg)
	case m.item != nil:
		return m.itemKey(k)
	case m.digest != nil:
		return m.digestKey(k)
	case m.inbox != nil:
		return m.inboxKey(k)
	case !m.dashboard:
		return m.homeKey(msg)
	}
	return m.key(k)
}

// reloadTop reloads the data of the screen on top after each poll. The
// screens below it reload when they are on top again.
func (m *Model) reloadTop() tea.Cmd {
	switch {
	case m.run != nil:
		return m.loadRun()
	case m.item != nil:
		return m.loadThread()
	case m.digest != nil:
		return m.loadDigest()
	case m.inbox != nil:
		return m.loadInbox()
	case !m.dashboard:
		return m.loadPreview(false)
	}
	return nil
}

func (m *Model) key(k string) tea.Cmd {
	if m.help {
		return m.helpKey(k)
	}
	switch k {
	case "q", "ctrl+c":
		return tea.Quit
	case "?":
		m.openHelp()
	case "tab", "right", "l":
		m.focus = (m.focus + 1) % numPanels
	case "shift+tab", "left", "h":
		m.focus = (m.focus + numPanels - 1) % numPanels
	case "up", "k":
		m.cursor[m.focus]--
	case "down", "j":
		m.cursor[m.focus]++
	case "home", "g":
		m.cursor[m.focus] = 0
	case "end", "G":
		m.cursor[m.focus] = 1 << 30
	case "r":
		if !m.fetching {
			m.fetching = true
			return m.fetch()
		}
	case "enter", "a", "c", "t":
		return m.overviewAction(k)
	case "i":
		return m.openInbox("")
	case "d":
		return m.openDigest()
	case "v", "esc":
		m.dashboard = false
		return m.loadPreview(false)
	}
	m.clamp()
	return nil
}

func (m *Model) rowCount(p Panel) int {
	if m.snap == nil {
		return 0
	}
	switch p {
	case PanelAgents:
		return len(m.snap.Status.Agents)
	case PanelRunning:
		return len(m.snap.Status.Running)
	case PanelNeedsYou:
		return len(m.snap.NeedsYou)
	case PanelDLQ:
		return len(m.snap.DLQ)
	case PanelRecent:
		return len(m.snap.Recent)
	case PanelEvents:
		return len(m.snap.Events)
	}
	return 0
}

func (m *Model) clamp() {
	for p := Panel(0); p < numPanels; p++ {
		n := m.rowCount(p)
		if m.cursor[p] >= n {
			m.cursor[p] = n - 1
		}
		if m.cursor[p] < 0 {
			m.cursor[p] = 0
		}
	}
}

// Focus returns the focused panel.
func (m *Model) Focus() Panel { return m.focus }

// Selected returns the id under the cursor of the focused panel: an agent,
// run or message id, or "" when the panel is empty.
func (m *Model) Selected() string {
	if m.snap == nil || m.rowCount(m.focus) == 0 {
		return ""
	}
	i := m.cursor[m.focus]
	switch m.focus {
	case PanelAgents:
		return m.snap.Status.Agents[i].ID
	case PanelRunning:
		return m.snap.Status.Running[i].ID
	case PanelNeedsYou:
		return m.snap.NeedsYou[i].ID
	case PanelDLQ:
		return m.snap.DLQ[i].ID
	case PanelRecent:
		return m.snap.Recent[i].ID
	case PanelEvents:
		ev := m.eventAt(i)
		if ev.MessageID != "" {
			return ev.MessageID
		}
		return ev.RunID
	}
	return ""
}

// Down reports whether the last poll failed.
func (m *Model) Down() bool { return m.lastErr != nil }

// statusTTL is how long an action result stays in the footer.
const statusTTL = 30 * time.Second

// View renders the screen.
// View is the Bubble Tea v2 view: the screen on the alternate screen.
func (m *Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	v.WindowTitle = "mailroom"
	return v
}

// Render draws the screen as text (tests read it with ansi.Strip).
func (m *Model) Render() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	var lines []string
	if m.homeActive() {
		lines = append(lines, m.homeTop(w))
	} else {
		lines = append(lines, m.header()...)
		lines = append(lines, stDim.Render(strings.Repeat("─", w)))
	}
	footer := []string{stDim.Render(fitKeys(m.footerKeys(), w))}
	if m.status != "" && m.now().Sub(m.statusAt) < statusTTL {
		st := stTitle
		if m.statusBad {
			st = stBad
		}
		footer = append([]string{st.Render(m.status)}, footer...)
	}
	footer = append(m.bottomBox(w), footer...)
	if m.prompt != nil {
		footer = append(m.promptLines(w), footer...)
	}
	body := h - len(lines) - len(footer)
	if m.help {
		lines = append(lines, m.helpView(w, body)...)
	} else if m.snap == nil {
		if m.lastErr != nil {
			lines = append(lines, "")
			for _, p := range []string{
				"The daemon is not reachable. Start it with `mailroom daemon` or `mailroom install`.",
				"This screen retries every " + m.interval.String() + " and fills in when the daemon answers.",
				"Last error: " + oneLine(m.lastErr.Error()),
			} {
				for _, l := range strings.Split(ansi.Wrap(p, max(20, w-4), ""), "\n") {
					lines = append(lines, "  "+l)
				}
			}
		} else {
			lines = append(lines, "", "  loading…")
		}
	} else if m.run != nil {
		lines = append(lines, m.runLines(w, body)...)
	} else if m.item != nil {
		lines = append(lines, m.itemLines(w, body)...)
	} else if m.digest != nil {
		lines = append(lines, m.digestLines(w, body)...)
	} else if m.inbox != nil {
		lines = append(lines, m.inboxLines(w, body)...)
	} else if !m.dashboard {
		lines = append(lines, m.homeLines(w, body)...)
	} else {
		lines = append(lines, m.panels(w, body)...)
	}
	keep := max(0, h-len(footer))
	for len(lines) < keep {
		lines = append(lines, "")
	}
	if len(lines) > keep {
		lines = lines[:keep]
	}
	lines = append(lines, footer...)
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) header() []string {
	now := m.now()
	w := m.width
	if w <= 0 {
		w = 80
	}
	if m.lastErr != nil {
		l1 := "mailroom  " + stBad.Render("DAEMON DOWN") + "  " + oneLine(m.lastErr.Error())
		l2 := "retrying every " + m.interval.String()
		if m.snap != nil {
			l2 += fmt.Sprintf(" · showing stale data from %s (%s ago)", m.snap.At.Local().Format("15:04:05"), dur(now.Sub(m.snap.At)))
		} else {
			l2 += " · no data yet"
		}
		return []string{l1, l2}
	}
	if m.snap == nil {
		return []string{"mailroom  connecting to the daemon…", ""}
	}
	s := m.snap.Status
	d := s.Daemon
	claude := "claude " + d.ClaudeVersion + " ok"
	if !d.ClaudeOK {
		claude = stBad.Render("claude UNAVAILABLE") + " (" + strings.Join(d.ClaudeProblems, "; ") + ")"
	}
	// The parts with the highest number go first when the line is too
	// wide for the terminal.
	l1 := "mailroom  " + fitParts(w-10, " · ",
		part{fmt.Sprintf("daemon %s pid %d up %s", d.Version, d.PID, dur(now.Sub(d.StartedAt))), 3,
			fmt.Sprintf("daemon %s up %s", d.Version, dur(now.Sub(d.StartedAt)))},
		part{fmt.Sprintf("schema %d", d.SchemaVersion), 4, ""}, part{claude, 0, ""},
		part{"updated " + m.snap.At.Local().Format("15:04:05"), 2, ""})
	mt := s.Metrics
	needs := fmt.Sprintf("needs you %d", len(m.snap.NeedsYou))
	if len(m.snap.NeedsYou) > 0 {
		needs = stWarn.Render(needs)
	}
	dead := fmt.Sprintf("dead letters %d", s.DLQ)
	if s.DLQ > 0 {
		dead = stBad.Render(dead)
	}
	runs, runsShort := "24h: no runs", ""
	if mt.RunsLast24h > 0 {
		runs = fmt.Sprintf("24h: %d runs, %.0f%% ok, p50 %.1fs, p95 %.1fs", mt.RunsLast24h, mt.SuccessRate*100, mt.P50Seconds, mt.P95Seconds)
		runsShort = fmt.Sprintf("24h: %d runs, %.0f%% ok", mt.RunsLast24h, mt.SuccessRate*100)
	}
	l2 := fitParts(w, " · ", part{fmt.Sprintf("queue %d", mt.QueueDepth), 0, ""}, part{fmt.Sprintf("running %d", len(s.Running)), 0, ""},
		part{needs, 0, ""}, part{dead, 0, ""}, part{runs, 1, runsShort})
	return []string{l1, l2}
}

type table struct {
	panel Panel
	cols  []string
	short []string // shorter column names for a narrow terminal (nil: cols)
	rows  [][]string
	note  string // drawn after the title (an error)
	empty string // drawn in place of the rows when there are none
}

func (m *Model) tables() []table {
	s := m.snap
	now := m.now()
	ts := make([]table, numPanels)
	for p := range ts {
		ts[p].panel = Panel(p)
	}
	ts[PanelAgents].cols = []string{"AGENT", "QUEUED", "RUNNING", "NEEDS YOU", "DEAD", "SPENT TODAY", "BUDGET LEFT", "SESSION"}
	ts[PanelAgents].short = []string{"AGENT", "QUEUED", "RUNNING", "NEEDS", "DEAD", "SPENT", "LEFT", "SESSION"}
	ts[PanelAgents].empty = "no agents in the config; add one with `mailroom init --agent id=repo-path`"
	ts[PanelRunning].empty = "nothing is running"
	ts[PanelNeedsYou].empty = "nothing needs you"
	ts[PanelDLQ].empty = "no dead letters"
	ts[PanelRecent].empty = "no messages yet"
	ts[PanelEvents].empty = "no audit events yet"
	for _, a := range s.Status.Agents {
		left := "-"
		if a.BudgetLeft != nil {
			left = money(*a.BudgetLeft)
		}
		sess := "-"
		if a.Interactive {
			sess = "interactive"
		}
		ts[PanelAgents].rows = append(ts[PanelAgents].rows, []string{a.ID, itoa(a.Queued), itoa(a.Running), itoa(a.NeedsHuman),
			itoa(a.DeadLetters), money(a.SpentUSD), left, sess})
	}
	ts[PanelRunning].cols = []string{"RUN", "AGENT", "HANDLER", "ATTEMPT", "ELAPSED", "MESSAGE"}
	for _, r := range s.Status.Running {
		hd := r.Handler
		if r.Profile != "" {
			hd += "/" + r.Profile
		}
		ts[PanelRunning].rows = append(ts[PanelRunning].rows, []string{Short(r.ID), r.Agent, hd, itoa(r.Attempt),
			dur(now.Sub(r.StartedAt)), Short(r.MessageID)})
	}
	ts[PanelNeedsYou].cols = []string{"MESSAGE", "AGENT", "FROM", "WAITING", "QUESTION"}
	for _, x := range s.NeedsYou {
		q := x.HumanQuestion
		if q == "" {
			q = x.Subject
		}
		ts[PanelNeedsYou].rows = append(ts[PanelNeedsYou].rows, []string{Short(x.ID), x.To, x.From.Agent, dur(now.Sub(x.UpdatedAt)), oneLine(q)})
	}
	ts[PanelDLQ].cols = []string{"MESSAGE", "AGENT", "FROM", "INTENT", "AGE", "REASON"}
	for _, x := range s.DLQ {
		ts[PanelDLQ].rows = append(ts[PanelDLQ].rows, []string{Short(x.ID), x.To, x.From.Agent, x.Intent, dur(now.Sub(x.UpdatedAt)), oneLine(x.Reason)})
	}
	ts[PanelRecent].cols = []string{"MESSAGE", "WHEN", "FROM", "TO", "KIND", "INTENT", "STATE", "SUMMARY"}
	for _, x := range s.Recent {
		ts[PanelRecent].rows = append(ts[PanelRecent].rows, []string{Short(x.ID), dur(now.Sub(x.CreatedAt)) + " ago", x.From.Agent, x.To,
			x.Kind, x.Intent, x.State, oneLine(summary(x))})
	}
	ts[PanelEvents].cols = []string{"WHEN", "SEQ", "EVENT", "AGENT", "MESSAGE", "RUN", "DETAIL"}
	for i := range s.Events {
		ev := m.eventAt(i)
		ts[PanelEvents].rows = append(ts[PanelEvents].rows, []string{ev.TS.Local().Format("15:04:05"), fmt.Sprintf("#%d", ev.Seq),
			ev.Event, dash(ev.Agent), dash(Short(ev.MessageID)), dash(Short(ev.RunID)), EventDetail(ev)})
	}
	if s.EventsErr != nil {
		ts[PanelEvents].note = "cannot read the audit feed: " + oneLine(rpcMessage(s.EventsErr))
	}
	return ts
}

// eventAt is row i of the EVENTS panel: newest first.
func (m *Model) eventAt(i int) api.AuditEvent {
	evs := m.snap.Events
	return evs[len(evs)-1-i]
}

// selectedEventSeq is the seq under the EVENTS cursor, or 0 when the
// cursor is on the newest row (it then stays on the newest row).
func (m *Model) selectedEventSeq() int64 {
	if m.snap == nil || m.cursor[PanelEvents] <= 0 || m.cursor[PanelEvents] >= len(m.snap.Events) {
		return 0
	}
	return m.eventAt(m.cursor[PanelEvents]).Seq
}

// keepEventCursor puts the EVENTS cursor back on seq after new events
// moved the rows down.
func (m *Model) keepEventCursor(seq int64) {
	if seq == 0 {
		return
	}
	for i := range m.snap.Events {
		if m.eventAt(i).Seq == seq {
			m.cursor[PanelEvents] = i
			return
		}
	}
}

// panels lays the five tables into avail lines.
func (m *Model) panels(w, avail int) []string {
	ts := m.tables()
	want := make([]int, numPanels)
	shown := make([]int, numPanels)
	for i, t := range ts {
		want[i] = max(1, len(t.rows))
	}
	fixed := 2 * int(numPanels) // title and column header per panel
	spacing := 0
	total := 0
	for _, x := range want {
		total += x
	}
	if fixed+total+int(numPanels)-1 <= avail {
		spacing = 1
	}
	budget := avail - fixed - spacing*(int(numPanels)-1)
	if total <= budget {
		copy(shown, want)
	} else {
		left := budget
		for i := range shown {
			shown[i] = 1
			left--
		}
		// Hand out the rest one row at a time, the focused panel first.
		for left > 0 {
			gave := false
			for k := 0; k < int(numPanels) && left > 0; k++ {
				i := (int(m.focus) + k) % int(numPanels)
				if shown[i] < want[i] {
					shown[i]++
					left--
					gave = true
				}
			}
			if !gave {
				break
			}
		}
	}
	var out []string
	for i, t := range ts {
		if i > 0 && spacing > 0 {
			out = append(out, "")
		}
		out = append(out, m.renderTable(t, shown[i], w)...)
	}
	return out
}

func (m *Model) renderTable(t table, shown, w int) []string {
	focused := t.panel == m.focus
	n := len(t.rows)
	cur := m.cursor[t.panel]
	off := rowOffset(cur, shown)
	title := fmt.Sprintf("%s (%d)", t.panel, n)
	if n > shown && shown > 0 {
		title += fmt.Sprintf("  rows %d-%d", off+1, min(n, off+shown))
	}
	// "▶" marks the focused panel also where styles do not show (NO_COLOR
	// on a terminal without attributes, a copied screen).
	if focused {
		title = "▶" + stFocus.Render(" "+title+" ")
	} else {
		title = " " + stTitle.Render(" "+title+" ")
	}
	if t.note != "" {
		title += " " + stBad.Render(t.note)
	}
	return append([]string{title}, renderRows(t, cur, shown, w, focused)...)
}

// rowOffset is the first row shown so that row cur is in a window of shown rows.
func rowOffset(cur, shown int) int {
	if cur >= shown {
		return cur - shown + 1
	}
	return 0
}

// renderRows is the column header and up to shown rows around cur. A
// focused table marks row cur with "> ".
func renderRows(t table, cur, shown, w int, focused bool) []string {
	n := len(t.rows)
	off := rowOffset(cur, shown)
	cols := t.cols
	measure := func() []int {
		widths := make([]int, len(cols))
		for i, c := range cols {
			widths[i] = ansi.StringWidth(c)
		}
		for _, r := range t.rows {
			for i, c := range r {
				widths[i] = max(widths[i], min(ansi.StringWidth(c), 40))
			}
		}
		return widths
	}
	// The row starts after a 2-cell cursor column. The last column (the
	// free text) keeps at least lastMin cells while the other columns
	// can shrink; it is cut with "…" at the edge.
	avail := w - 2
	widths := measure()
	lastMin := min(widths[len(widths)-1], 8)
	need := colGap*(len(widths)-1) + lastMin
	for _, x := range widths[:len(widths)-1] {
		need += x
	}
	if need > avail && t.short != nil {
		cols = t.short
		widths = measure()
	}
	fitColumns(widths, avail, lastMin)
	line := func(cells []string) string {
		var b strings.Builder
		for i, c := range cells {
			if i == len(cells)-1 {
				b.WriteString(c)
				break
			}
			c = ansi.Truncate(c, widths[i], "…")
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(c)+colGap))
		}
		return b.String()
	}
	out := []string{"  " + stHeading.Render(line(cols))}
	if n == 0 {
		empty := t.empty
		if empty == "" {
			empty = "none"
		}
		return append(out, stDim.Render("  ("+empty+")"))
	}
	for i := off; i < off+shown && i < n; i++ {
		row := line(t.rows[i])
		if focused && i == cur {
			out = append(out, "> "+stCursor.Render(ansi.Truncate(row, w-2, "…")))
		} else {
			out = append(out, "  "+row)
		}
	}
	return out
}

func summary(x api.MessageView) string {
	s := x.Summary
	if s == "" {
		s = x.Subject
	}
	switch x.State {
	case "rejected", "dead_letter", "expired":
		if x.Reason != "" {
			s = x.Reason
		}
	case "queued":
		if x.HoldReason != "" {
			s = "held: " + x.HoldReason
		}
	}
	if x.Coalesced > 0 {
		s += fmt.Sprintf(" (+%d coalesced)", x.Coalesced)
	}
	return s
}

// Short is the last 10 characters of an id, as the CLI prints it.
func Short(id string) string {
	if len(id) > 10 {
		return id[len(id)-10:]
	}
	return id
}

// money formats dollars: $0.25, -$0.50.
// money shows cents, but keeps a small non-zero spend visible ($0.0017,
// not $0.00): haiku runs cost fractions of a cent.
func money(f float64) string {
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	if f > 0 && f < 0.01 {
		return fmt.Sprintf("%s$%.4f", sign, f)
	}
	return fmt.Sprintf("%s$%.2f", sign, f)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	return s
}

// dur formats an elapsed time: 45s, 3m05s, 2h03m, 3d04h.
func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
