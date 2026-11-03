package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
)

// MessageStates are the message states in lifecycle order (store.AllStates).
var MessageStates = []string{"accepted", "queued", "running", "retry_wait", "needs_human", "done", "rejected", "expired", "dead_letter"}

// InboxLimit is how many messages the inbox screen loads.
const InboxLimit = 200

// inboxScreen lists recent messages with an agent and a state filter.
type inboxScreen struct {
	agent, state string // "" = all
	rows         []api.MessageView
	err          error
	loading      bool
	loaded       bool
	cursor       int
}

type inboxMsg struct {
	agent, state string
	rows         []api.MessageView
	err          error
}

// InboxOpen reports whether the inbox screen is open, and its filter.
func (m *Model) InboxOpen() (open bool, agent, state string) {
	if m.inbox == nil {
		return false, "", ""
	}
	return true, m.inbox.agent, m.inbox.state
}

func (m *Model) openInbox(agent string) tea.Cmd {
	if m.insp == nil {
		m.setStatus("This UI has no daemon connection to list messages.", true)
		return nil
	}
	m.inbox = &inboxScreen{agent: agent}
	return m.loadInbox()
}

func (m *Model) loadInbox() tea.Cmd {
	ib := m.inbox
	if m.insp == nil || ib == nil || ib.loading {
		return nil
	}
	ib.loading = true
	insp, p := m.insp, api.ListParams{Agent: ib.agent, State: ib.state, Limit: InboxLimit}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := insp.Messages(ctx, p)
		return inboxMsg{agent: p.Agent, state: p.State, rows: rows, err: err}
	}
}

// onInbox keeps the cursor on the same message when the list moves.
func (m *Model) onInbox(msg inboxMsg) tea.Cmd {
	ib := m.inbox
	if ib == nil {
		return nil
	}
	ib.loading = false
	if msg.agent != ib.agent || msg.state != ib.state {
		return m.loadInbox() // the filter changed while the call was in flight
	}
	if msg.err != nil {
		ib.err = msg.err
		return nil
	}
	sel := ""
	if ib.cursor < len(ib.rows) {
		sel = ib.rows[ib.cursor].ID
	}
	ib.rows, ib.err, ib.loaded = msg.rows, nil, true
	for i, x := range ib.rows {
		if x.ID == sel {
			ib.cursor = i
		}
	}
	ib.clamp()
	return nil
}

func (ib *inboxScreen) clamp() {
	ib.cursor = max(0, min(ib.cursor, len(ib.rows)-1))
}

// agentNames are the agents of the last snapshot, for the agent filter.
func (m *Model) agentNames() []string {
	var out []string
	if m.snap != nil {
		for _, a := range m.snap.Status.Agents {
			out = append(out, a.ID)
		}
	}
	return out
}

// cycle returns the value after (step 1) or before (step -1) cur in
// "" + list; "" means all.
func cycle(list []string, cur string, step int) string {
	all := append([]string{""}, list...)
	i := 0
	for j, v := range all {
		if v == cur {
			i = j
		}
	}
	return all[(i+step+len(all))%len(all)]
}

func (m *Model) inboxKey(k string) tea.Cmd {
	ib := m.inbox
	refilter := func() tea.Cmd {
		ib.cursor, ib.rows, ib.loaded, ib.err = 0, nil, false, nil
		return m.loadInbox()
	}
	switch k {
	case "q":
		return tea.Quit
	case "esc", "backspace":
		m.inbox = nil
	case "?":
		m.openHelp()
	case "up", "k":
		ib.cursor--
	case "down", "j":
		ib.cursor++
	case "pgup":
		ib.cursor -= 10
	case "pgdown", " ":
		ib.cursor += 10
	case "home", "g":
		ib.cursor = 0
	case "end", "G":
		ib.cursor = 1 << 30
	case "f":
		ib.agent = cycle(m.agentNames(), ib.agent, 1)
		return refilter()
	case "F":
		ib.agent = cycle(m.agentNames(), ib.agent, -1)
		return refilter()
	case "s":
		ib.state = cycle(MessageStates, ib.state, 1)
		return refilter()
	case "S":
		ib.state = cycle(MessageStates, ib.state, -1)
		return refilter()
	case "x":
		ib.agent, ib.state = "", ""
		return refilter()
	case "r":
		var cmds []tea.Cmd
		if !m.fetching {
			m.fetching = true
			cmds = append(cmds, m.fetch())
		}
		return tea.Batch(append(cmds, m.loadInbox())...)
	case "enter":
		ib.clamp()
		if len(ib.rows) == 0 {
			return nil
		}
		return m.openMessage(ib.rows[ib.cursor])
	}
	ib.clamp()
	return nil
}

// openMessage opens the thread screen of any message.
func (m *Model) openMessage(x api.MessageView) tea.Cmd {
	m.item = &itemScreen{id: x.ID, msg: x}
	return m.loadThread()
}

func (m *Model) inboxLines(w, avail int) []string {
	ib := m.inbox
	now := m.now()
	title := fmt.Sprintf(" INBOX · agent: %s · state: %s · %d messages ", allIfEmpty(ib.agent), allIfEmpty(ib.state), len(ib.rows))
	if len(ib.rows) >= InboxLimit {
		title = fmt.Sprintf(" INBOX · agent: %s · state: %s · newest %d messages ", allIfEmpty(ib.agent), allIfEmpty(ib.state), InboxLimit)
	}
	head := []string{stFocus.Render(title)}
	if ib.err != nil {
		head = append(head, stBad.Render("  cannot load the messages: "+oneLine(ib.err.Error())))
	}
	t := table{panel: PanelRecent, cols: []string{"MESSAGE", "WHEN", "FROM", "TO", "KIND", "INTENT", "STATE", "SUMMARY"}}
	for _, x := range ib.rows {
		t.rows = append(t.rows, []string{Short(x.ID), dur(now.Sub(x.CreatedAt)) + " ago", x.From.Agent, x.To,
			x.Kind, x.Intent, x.State, oneLine(summary(x))})
	}
	switch {
	case !ib.loaded && ib.err == nil:
		return append(head, stDim.Render("  loading…"))
	case len(t.rows) == 0 && ib.agent == "" && ib.state == "":
		return append(head, "", "  No messages yet. A message shows here when an agent sends one.")
	case len(t.rows) == 0:
		return append(head, "", "  No messages match the filter (agent: "+allIfEmpty(ib.agent)+", state: "+allIfEmpty(ib.state)+").",
			stDim.Render("  f/F and s/S change the filter, x clears it."))
	}
	room := max(1, avail-len(head)-1)
	lines := renderRows(t, ib.cursor, room, w, true)
	return append(head, lines...)
}

func allIfEmpty(s string) string {
	if s == "" {
		return "all"
	}
	return s
}
