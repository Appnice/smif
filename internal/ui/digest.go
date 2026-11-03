package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

// DigestLimit is how many unread items per agent the digest screen loads.
const DigestLimit = 500

// Digester is digest.get. DaemonSource implements it.
type Digester interface {
	Digest(ctx context.Context, p api.DigestParams) (*api.DigestResult, error)
}

// Digest reads the digest (digest.get). With p.Advance it marks the items
// read: it moves each agent's read cursor.
func (s *DaemonSource) Digest(ctx context.Context, p api.DigestParams) (*api.DigestResult, error) {
	var d api.DigestResult
	if err := s.call(ctx, "digest.get", p, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// digestScreen lists the unread handled items (digest.get without
// advance). "m" marks the shown items read.
type digestScreen struct {
	res     *api.DigestResult
	err     error
	loading bool
	cursor  int
}

type digestMsg struct {
	res *api.DigestResult
	err error
}

type digestMarkMsg struct {
	res   *api.DigestResult
	err   error
	until int64
}

// DigestOpen reports whether the digest screen is open and how many
// unread items it shows (-1 before the first load).
func (m *Model) DigestOpen() (open bool, items int) {
	if m.digest == nil {
		return false, 0
	}
	if m.digest.res == nil {
		return true, -1
	}
	return true, len(m.digest.res.Items)
}

func (m *Model) openDigest() tea.Cmd {
	if m.digr == nil {
		m.setStatus("This UI has no daemon connection to read the digest.", true)
		return nil
	}
	m.digest = &digestScreen{}
	return m.loadDigest()
}

// loadDigest peeks at the digest: it never moves the read cursor.
func (m *Model) loadDigest() tea.Cmd {
	ds := m.digest
	if m.digr == nil || ds == nil || ds.loading {
		return nil
	}
	ds.loading = true
	digr := m.digr
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := digr.Digest(ctx, api.DigestParams{Since: "last", Limit: DigestLimit})
		return digestMsg{res: res, err: err}
	}
}

func (m *Model) onDigest(msg digestMsg) {
	ds := m.digest
	if ds == nil {
		return
	}
	ds.loading = false
	if msg.err != nil {
		ds.err = msg.err
		return
	}
	ds.res, ds.err = msg.res, nil
	ds.clamp()
}

func (ds *digestScreen) clamp() {
	n := 0
	if ds.res != nil {
		n = len(ds.res.Items)
	}
	ds.cursor = max(0, min(ds.cursor, n-1))
}

// markDigest advances the read cursors up to the newest item on the
// screen (digest.get with advance and until). Items handled after the
// screen loaded stay unread.
func (m *Model) markDigest() tea.Cmd {
	ds := m.digest
	if ds.res == nil && ds.loading {
		m.setStatus("Still loading the digest; try again in a moment.", false)
		return nil
	}
	if ds.res == nil || len(ds.res.Items) == 0 {
		m.setStatus("Nothing unread to mark read.", false)
		return nil
	}
	if m.busy != "" {
		m.setStatus("Wait: the "+m.busy+" is still in flight.", true)
		return nil
	}
	m.busy = "mark read"
	until := ds.res.Cursor
	m.setStatus(fmt.Sprintf("Marking %d items read…", len(ds.res.Items)), false)
	digr := m.digr
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := digr.Digest(ctx, api.DigestParams{Since: "last", Advance: true, Until: until, Limit: DigestLimit})
		return digestMarkMsg{res: res, err: err, until: until}
	}
}

func (m *Model) onDigestMark(msg digestMarkMsg) tea.Cmd {
	m.busy = ""
	if msg.err != nil {
		m.setStatus("Mark read failed: "+oneLine(rpcMessage(msg.err)), true)
	} else {
		m.setStatus(fmt.Sprintf("Marked %d items read (read cursor %d).", len(msg.res.Items), msg.until), false)
	}
	var cmds []tea.Cmd
	if !m.fetching {
		m.fetching = true
		cmds = append(cmds, m.fetch())
	}
	if c := m.loadDigest(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (m *Model) digestKey(k string) tea.Cmd {
	ds := m.digest
	switch k {
	case "q":
		return tea.Quit
	case "esc", "backspace":
		m.digest = nil
		return nil
	case "?":
		m.openHelp()
	case "up", "k":
		ds.cursor--
	case "down", "j":
		ds.cursor++
	case "pgup":
		ds.cursor -= 10
	case "pgdown", " ":
		ds.cursor += 10
	case "home", "g":
		ds.cursor = 0
	case "end", "G":
		ds.cursor = 1 << 30
	case "m":
		return m.markDigest()
	case "r":
		var cmds []tea.Cmd
		if !m.fetching {
			m.fetching = true
			cmds = append(cmds, m.fetch())
		}
		return tea.Batch(append(cmds, m.loadDigest())...)
	case "enter":
		ds.clamp()
		if ds.res == nil || len(ds.res.Items) == 0 {
			return nil
		}
		it := ds.res.Items[ds.cursor]
		return m.openMessage(api.MessageView{ID: it.MessageID, ThreadID: it.ThreadID, To: it.Agent, From: envelope.From{Agent: it.From},
			Kind: it.Kind, Intent: it.Intent, State: it.State, Subject: it.Subject, Summary: it.Summary, HumanQuestion: it.HumanQuestion})
	}
	ds.clamp()
	return nil
}

func (m *Model) digestLines(w, avail int) []string {
	ds := m.digest
	now := m.now()
	agents := "all agents"
	n := 0
	if ds.res != nil {
		n = len(ds.res.Items)
		if len(ds.res.Agents) > 0 {
			agents = strings.Join(ds.res.Agents, ", ")
		}
	}
	head := []string{stFocus.Render(fmt.Sprintf(" DIGEST · %d unread handled items · %s ", n, agents))}
	if ds.err != nil {
		head = append(head, stBad.Render("  cannot load the digest: "+oneLine(rpcMessage(ds.err))))
	}
	switch {
	case ds.res == nil && ds.err == nil:
		return append(head, stDim.Render("  loading…"))
	case ds.res == nil:
		return head
	case n == 0:
		return append(head, "", "  Nothing new since the read cursor.")
	}
	head = append(head, stDim.Render("  m marks these items read (the read cursor moves past them); `mailroom digest` shows the same list."))
	t := table{panel: PanelRecent, cols: []string{"MESSAGE", "HANDLED", "AGENT", "FROM", "KIND", "INTENT", "STATE", "SUMMARY"}}
	for _, it := range ds.res.Items {
		s := it.Summary
		if it.State == "needs_human" && it.HumanQuestion != "" {
			s = "asks: " + it.HumanQuestion
		}
		if s == "" {
			s = it.Subject
		}
		if it.Coalesced > 0 {
			s += fmt.Sprintf(" (+%d coalesced)", it.Coalesced)
		}
		t.rows = append(t.rows, []string{Short(it.MessageID), dur(now.Sub(it.At)) + " ago", it.Agent, dash(it.From), it.Kind,
			it.Intent, it.State, oneLine(s)})
	}
	room := max(1, avail-len(head)-1)
	return append(head, renderRows(t, ds.cursor, room, w, true)...)
}
