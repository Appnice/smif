package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// Send when idle (ADR-0021). Enter in the box of a busy or waiting session
// puts the line in the session's queue, and ctrl+f asks a read-only fork
// (ADR-0013). The row shows ⧖ 2 queued. The chat lists the items, and
// ctrl+q, or x on the row, moves the cursor there; x removes one.

// queueShown is how many items a screen lists.
const queueShown = 5

// queueMark is the row's mark. The word stays in NO_COLOR.
func queueMark(sv *api.SessionView) string {
	if len(sv.Queue) == 0 {
		return ""
	}
	return fmt.Sprintf("⧖ %d queued", len(sv.Queue))
}

// enterWord says what enter does in the box of this session.
func enterWord(sv *api.SessionView) string {
	if sv.CanPrompt || !canQueue(sv) {
		return "send"
	}
	return "queue"
}

// queueKeyHints are the box's queue keys that work now, for the footer.
func queueKeyHints(sv *api.SessionView) string {
	var ks []string
	if !sv.CanPrompt && canFork(sv) {
		ks = append(ks, "ctrl+f ask a fork")
	}
	if len(sv.Queue) > 0 {
		ks = append(ks, "ctrl+q queued")
	}
	if len(ks) == 0 {
		return ""
	}
	return " · " + strings.Join(ks, " · ")
}

// queueLine puts one finished line in the queue.
func (m *Model) queueLine(r *homeRow, text string) tea.Cmd {
	q, ok := m.src.(SessionQueuer)
	if !ok {
		m.setStatus("This UI cannot queue prompts.", true)
		return nil
	}
	name, id := sessionName(r.sess), r.sess.SessionID
	m.setStatus("Queueing for "+name+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := q.QueueSession(ctx, id, text)
		return promptedMsg{agent: name, queued: res, err: err}
	}
}

// askFork is ctrl+f on a busy or waiting session: a read-only fork answers
// now. Parts stay out, because a fork reads no attachments (ADR-0020).
func (m *Model) askFork(r *homeRow) tea.Cmd {
	text := strings.Join(strings.Fields(m.home.draft.Value()), " ")
	switch {
	case !canFork(r.sess):
		m.setStatus("No fork can answer for this session: "+writeHint(r.sess), true)
	case len(m.home.part.parts) > 0:
		m.setStatus("A fork reads no attachments. Enter queues the parts for when it is idle; ctrl+p removes them.", true)
	case text == "":
		m.setStatus("The question is empty; type it first.", true)
	default:
		return m.sendLine(r, text)
	}
	return nil
}

// queueKey handles ctrl+q, ctrl+f and the keys of the queued prompts. It
// reports false for a key that goes on to the text box.
func (m *Model) queueKey(r *homeRow, k string) (tea.Cmd, bool) {
	if r == nil || r.sess == nil {
		return nil, false
	}
	q := r.sess.Queue
	if m.home.queuePick {
		if len(q) == 0 {
			m.home.queuePick = false // the last item went while it was open
			return nil, false
		}
		m.home.queueSel = min(m.home.queueSel, len(q)-1)
		switch k {
		case "up", "k":
			m.home.queueSel = max(0, m.home.queueSel-1)
		case "down", "j":
			m.home.queueSel = min(len(q)-1, m.home.queueSel+1)
		case "x":
			return m.removeQueued(r.sess, q[m.home.queueSel].ID), true
		case "esc", "ctrl+q":
			m.home.queuePick = false
		}
		return nil, true
	}
	switch k {
	case "ctrl+q":
		if len(q) == 0 {
			m.setStatus("Nothing is queued for this session.", false)
			return nil, true
		}
		m.home.queuePick, m.home.queueSel = true, len(q)-1
		return nil, true
	case "ctrl+f":
		if r.sess.CanPrompt {
			return nil, false // the text cursor moves right
		}
		return m.askFork(r), true
	}
	return nil, false
}

type queueRemovedMsg struct{ err error }

// removeQueued cancels one item. Its part files stay for the 24 h cleanup.
func (m *Model) removeQueued(sv *api.SessionView, id int64) tea.Cmd {
	q, ok := m.src.(SessionQueuer)
	if !ok {
		m.setStatus("This UI cannot queue prompts.", true)
		return nil
	}
	session := ""
	if sv != nil && sv.Host != "" {
		session = sv.SessionID // the id belongs to that machine's queue
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return queueRemovedMsg{err: q.CancelQueued(ctx, session, id)}
	}
}

func (m *Model) onQueueRemoved(msg queueRemovedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("Not removed: "+oneLine(rpcMessage(msg.err)), true)
	} else {
		m.setStatus("Removed from the queue.", false)
	}
	if m.fetching {
		return nil
	}
	m.fetching = true
	return m.fetch()
}

// queuedTexts are the lines of every item still queued.
func (m *Model) queuedTexts() []string {
	if m.snap == nil {
		return nil
	}
	var out []string
	for i := range m.snap.Sessions {
		for _, q := range m.snap.Sessions[i].Queue {
			out = append(out, q.Text)
		}
	}
	return out
}

// queueLines lists the queued prompts over the box, oldest first, each
// with why its last try did not type it (nil with none).
func (m *Model) queueLines(sv *api.SessionView, w int) []string {
	q := sv.Queue
	if len(q) == 0 {
		return nil
	}
	head := queueMark(sv) + " · one goes in per idle"
	switch {
	case m.home.queuePick:
	case m.home.typing:
		head += " · ctrl+q picks"
	default:
		head += " · x removes"
	}
	out := []string{"", stAccent.Render(ansi.Truncate(head, w, "…"))}
	sel := min(m.home.queueSel, len(q)-1)
	start := 0
	if m.home.queuePick {
		start = max(0, sel-queueShown+1)
	}
	now := m.now()
	for i := start; i < len(q) && i < start+queueShown; i++ {
		mark, text := "  ", ago(now.Sub(q[i].CreatedAt))+" · "+q[i].Text
		if m.home.queuePick && i == sel {
			mark = stAccent.Render("▶ ")
			text = stTitle.Render(ansi.Truncate(text, w-2, "…"))
		} else {
			text = stDim.Render(ansi.Truncate(text, w-2, "…"))
		}
		out = append(out, mark+text)
		if q[i].Error != "" {
			out = append(out, stWarn.Render(ansi.Truncate("    not typed yet: "+q[i].Error, w, "…")))
		}
	}
	if rest := len(q) - start - queueShown; rest > 0 {
		out = append(out, stDim.Render(fmt.Sprintf("  … %d more", rest)))
	}
	return out
}
