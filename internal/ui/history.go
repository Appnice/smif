package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/presence"
)

// The history screen (R, ADR-0021): past Claude Code sessions, the closed
// ones first. / filters the rows, ctrl+s searches the text of the sessions,
// enter reopens one and A reopens the restore list.

// SessionHistorian reads past sessions and reopens them. DaemonSource
// implements it.
type SessionHistorian interface {
	SessionHistory(ctx context.Context) ([]api.HistoryRow, error)
	SearchSessions(ctx context.Context, query string) (*api.SessionSearchResult, error)
	ResumeSession(ctx context.Context, id, term string) error
	ReopenSessions(ctx context.Context, term string) (*api.SessionReopenResult, error)
}

// The first index of a big folder can take seconds, and a search takes up
// to 10 s, so these two wait longer than a poll, on their own connection:
// the screen keeps its polls.
const (
	historyWait = 20 * time.Second
	searchWait  = 15 * time.Second
)

// callAlone makes one call on a connection of its own.
func (s *DaemonSource) callAlone(method string, params, out any, timeout time.Duration) error {
	c, err := s.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(method, params, out, timeout)
}

// SessionHistory lists the past sessions, newest first.
func (s *DaemonSource) SessionHistory(ctx context.Context) ([]api.HistoryRow, error) {
	var rows []api.HistoryRow
	err := s.callAlone("session.history", nil, &rows, historyWait)
	return rows, err
}

// SearchSessions looks for words in the text of past sessions.
func (s *DaemonSource) SearchSessions(ctx context.Context, query string) (*api.SessionSearchResult, error) {
	var res api.SessionSearchResult
	if err := s.callAlone("session.search", api.SessionSearchParams{Query: query}, &res, searchWait); err != nil {
		return nil, err
	}
	return &res, nil
}

// ResumeSession opens claude --resume for the session in a new tab.
func (s *DaemonSource) ResumeSession(ctx context.Context, id, term string) error {
	return s.callT(ctx, "session.resume", api.SessionResumeParams{SessionID: id, Term: term}, nil, historyWait)
}

// ReopenSessions starts reopening the restore list.
func (s *DaemonSource) ReopenSessions(ctx context.Context, term string) (*api.SessionReopenResult, error) {
	var res api.SessionReopenResult
	if err := s.callT(ctx, "session.reopen", api.SessionReopenParams{Term: term}, &res, 5*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}

// historyScreen is the open history screen.
type historyScreen struct {
	rows      []api.HistoryRow
	err       error
	loaded    bool
	cursor    int
	filter    lineInput
	filtering bool // the filter box has the keys
	// A search: its words, what came back, and whether it still runs.
	query     string
	searching bool
	found     *api.SessionSearchResult
	findErr   error
}

type historyMsg struct {
	rows []api.HistoryRow
	err  error
}

type searchMsg struct {
	query string
	res   *api.SessionSearchResult
	err   error
}

type reopenMsg struct {
	res *api.SessionReopenResult
	err error
}

// historyRow is one line of the list.
type historyRow struct {
	id, dir, name string
	at            time.Time
	state         string // died working, closed, open
	who, text     string // a search hit
}

// openHistory is R.
func (m *Model) openHistory() tea.Cmd {
	if _, ok := m.src.(SessionHistorian); !ok {
		m.setStatus("This UI has no daemon connection to read the history.", true)
		return nil
	}
	m.history = &historyScreen{}
	return m.loadHistory()
}

func (m *Model) loadHistory() tea.Cmd {
	h := m.src.(SessionHistorian)
	return func() tea.Msg {
		rows, err := h.SessionHistory(context.Background())
		return historyMsg{rows: rows, err: err}
	}
}

func (m *Model) onHistory(msg historyMsg) {
	if s := m.history; s != nil {
		s.rows, s.err, s.loaded = msg.rows, msg.err, true
	}
}

func (m *Model) onSearch(msg searchMsg) {
	if s := m.history; s != nil && s.query == msg.query {
		s.found, s.findErr, s.searching, s.cursor = msg.res, msg.err, false, 0
	}
}

func (m *Model) onReopen(msg reopenMsg) {
	switch {
	case msg.err != nil:
		m.setStatus("Not reopened: "+oneLine(rpcMessage(msg.err)), true)
	case msg.res.Left > 0:
		m.setStatus(fmt.Sprintf("Reopening %d sessions. %d stay on the list: A again for the rest.", msg.res.Opening, msg.res.Left), false)
	default:
		m.setStatus(fmt.Sprintf("Reopening %d sessions.", msg.res.Opening), false)
	}
}

// closedRows is the restore list from the last snapshot.
func (m *Model) closedRows() []api.ClosedSession {
	if m.snap == nil {
		return nil
	}
	return m.snap.Closed.Sessions
}

// closedBanner is the home screen's line about the restore list.
func (m *Model) closedBanner() string {
	n := len(m.closedRows())
	if n == 0 {
		return ""
	}
	word := "sessions"
	if n == 1 {
		word = "session"
	}
	if m.snap.Closed.Reopening {
		return stWarn.Render(fmt.Sprintf(" %d %s closed · reopening…", n, word))
	}
	return stWarn.Render(fmt.Sprintf(" %d %s closed · R reopens", n, word))
}

// historyRows is what the screen lists now: the hits of a search, else the
// closed sessions and the history under the filter.
func (m *Model) historyRows() []historyRow {
	s := m.history
	var out []historyRow
	if s.query != "" {
		if s.found != nil {
			for _, h := range s.found.Hits {
				out = append(out, historyRow{id: h.SessionID, dir: h.Dir, name: h.Name, at: h.At, who: h.Role, text: h.Text})
			}
		}
		return out
	}
	f := strings.ToLower(s.filter.Value())
	keep := func(r historyRow) {
		if strings.Contains(strings.ToLower(r.name+" "+r.dir+" "+r.id), f) {
			out = append(out, r)
		}
	}
	closed := map[string]bool{}
	for _, c := range m.closedRows() {
		closed[c.SessionID] = true
		state := "closed"
		if c.DiedWorking {
			state = "died working"
		}
		keep(historyRow{id: c.SessionID, dir: c.Dir, name: c.Name, at: c.ClosedAt, state: state})
	}
	for _, r := range s.rows {
		if closed[r.SessionID] {
			continue
		}
		state := ""
		if r.Open {
			state = "open"
		}
		keep(historyRow{id: r.SessionID, dir: r.Dir, name: r.Name, at: r.At, state: state})
	}
	return out
}

// uiTerm is the terminal a new tab opens in: the UI's own when Smif can
// drive it, else the daemon's default.
func uiTerm() string {
	if t := presence.Term(); canFocus(t) {
		return t
	}
	return ""
}

func (m *Model) resumeRow(r historyRow) tea.Cmd {
	h := m.src.(SessionHistorian)
	id, dir, name, term := r.id, r.dir, r.name, uiTerm()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), historyWait)
		defer cancel()
		return openedMsg{name: firstRunes(name, 40), dir: dir, err: h.ResumeSession(ctx, id, term)}
	}
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (m *Model) reopenAll() tea.Cmd {
	if len(m.closedRows()) == 0 {
		m.setStatus("No closed sessions to reopen.", true)
		return nil
	}
	h := m.src.(SessionHistorian)
	term := uiTerm()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := h.ReopenSessions(ctx, term)
		return reopenMsg{res: res, err: err}
	}
}

// searchText is ctrl+s: it searches for the filter's words.
func (m *Model) searchText() tea.Cmd {
	s := m.history
	q := strings.TrimSpace(s.filter.Value())
	if q == "" {
		s.filtering = true
		m.setStatus("Type the words, then ctrl+s.", false)
		return nil
	}
	s.filtering, s.query, s.found, s.findErr, s.searching, s.cursor = false, q, nil, nil, true, 0
	h := m.src.(SessionHistorian)
	return func() tea.Msg {
		res, err := h.SearchSessions(context.Background(), q)
		return searchMsg{query: q, res: res, err: err}
	}
}

// historyKey handles a key on the history screen.
func (m *Model) historyKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.history
	k := msg.String()
	if s.filtering {
		switch k {
		case "enter":
			s.filtering = false
		case "esc":
			s.filtering = false
			s.filter = lineInput{}
		case "ctrl+s":
			return m.searchText()
		default:
			s.filter.edit(msg)
		}
		s.cursor = 0
		return nil
	}
	rows := m.historyRows()
	move := func(step int) {
		s.cursor = max(0, min(s.cursor+step, len(rows)-1))
	}
	switch k {
	case "q":
		return tea.Quit
	case "?":
		m.openHelp()
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgup":
		move(-10)
	case "pgdown":
		move(10)
	case "home", "g":
		move(-1 << 20)
	case "end", "G":
		move(1 << 20)
	case "/":
		s.query, s.found, s.findErr = "", nil, nil
		s.filtering, s.cursor = true, 0
	case "ctrl+s":
		return m.searchText()
	case "r":
		s.loaded = false
		return m.loadHistory()
	case "enter":
		if s.cursor < len(rows) {
			return m.resumeRow(rows[s.cursor])
		}
	case "A":
		return m.reopenAll()
	case "esc":
		switch {
		case s.query != "":
			s.query, s.found, s.findErr, s.cursor = "", nil, nil, 0
		case s.filter.Value() != "":
			s.filter, s.cursor = lineInput{}, 0
		default:
			m.history = nil
		}
	}
	return nil
}

// historyTitle is the screen's first line.
func (m *Model) historyTitle(rows []historyRow) string {
	s := m.history
	if s.query != "" {
		t := fmt.Sprintf(" SEARCH %q · %d hits", s.query, len(rows))
		if s.found != nil {
			switch s.found.Stopped {
			case "hits":
				t += ", stopped at the limit of hits"
			case "time":
				t += ", stopped at the limit of 10 s"
			}
		}
		return stFocus.Render(t + " ")
	}
	t := fmt.Sprintf(" HISTORY · %d sessions", len(rows))
	if n := len(m.closedRows()); n > 0 {
		t += fmt.Sprintf(" · %d closed", n)
	}
	if s.filtering {
		t += " · /" + s.filter.view(30)
	} else if f := s.filter.Value(); f != "" {
		t += " · /" + f
	}
	return stFocus.Render(t + " ")
}

func (m *Model) historyLines(w, avail int) []string {
	s := m.history
	rows := m.historyRows()
	s.cursor = max(0, min(s.cursor, len(rows)-1))
	now := m.now()
	head := []string{m.historyTitle(rows)}
	if s.err != nil {
		head = append(head, stBad.Render("  cannot read the history: "+oneLine(rpcMessage(s.err))))
	}
	if s.findErr != nil {
		head = append(head, stBad.Render("  cannot search: "+oneLine(rpcMessage(s.findErr))))
	}
	if m.snap != nil && m.snap.Closed.Why != "" {
		head = append(head, stBad.Render("  not reopened: "+oneLine(m.snap.Closed.Why)))
	}
	t := table{panel: PanelRecent}
	if s.query != "" {
		t.cols = []string{"WHEN", "FROM", "SESSION", "TEXT"}
		for _, r := range rows {
			t.rows = append(t.rows, []string{ago(now.Sub(r.at)), r.who, firstRunes(sessionLabel(r), 20), r.text})
		}
	} else {
		t.cols = []string{"WHEN", "STATE", "FOLDER", "SESSION"}
		for _, r := range rows {
			t.rows = append(t.rows, []string{ago(now.Sub(r.at)), r.state, shortHome(r.dir), sessionLabel(r)})
		}
	}
	switch {
	case s.searching:
		return append(head, stDim.Render("  searching, at most 10 s…"))
	case !s.loaded && s.err == nil:
		return append(head, stDim.Render("  loading…"))
	case len(rows) == 0 && s.query != "":
		return append(head, "", "  No session text holds these words. esc goes back to the list.")
	case len(rows) == 0 && s.filter.Value() != "":
		return append(head, "", "  No session matches the filter. esc clears it, ctrl+s searches the text.")
	case len(rows) == 0:
		return append(head, "", "  No past sessions. A Claude Code session shows here once it has a transcript.")
	}
	room := max(1, avail-len(head)-1)
	return append(head, renderRows(t, s.cursor, room, w, true)...)
}

// sessionLabel is a row's name, else its id.
func sessionLabel(r historyRow) string {
	if r.name != "" {
		return r.name
	}
	return api.ShortSession(r.id)
}

// historyKeys is the footer of the screen.
func (m *Model) historyKeys() string {
	s := m.history
	if s.filtering {
		return "type to filter · enter keep · ctrl+s search the text · esc clear"
	}
	ks := []string{"enter reopen"}
	if len(m.closedRows()) > 0 {
		ks = append(ks, "A reopen all")
	}
	return strings.Join(append(ks, "/ filter", "ctrl+s search", "↑↓ move", "r refresh", "esc back", "? help", "q quit"), " · ")
}
