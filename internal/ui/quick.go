package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// Saved prompts and new sessions (ADR-0015). p on a session opens the
// saved prompts ([prompts] in the config); enter sends the chosen one the
// way s would (typed in when idle, else a fork answers). n opens a new
// terminal tab that runs claude in the selected session's folder.

// SessionOpener opens a new session in a terminal tab (session.new).
type SessionOpener interface {
	NewSession(ctx context.Context, dir, name, term string) error
}

// NewSession opens a new terminal tab that runs claude in dir.
func (s *DaemonSource) NewSession(ctx context.Context, dir, name, term string) error {
	return s.callT(ctx, "session.new", api.SessionNewParams{Dir: dir, Name: name, Term: term}, nil, 20*time.Second)
}

type openedMsg struct {
	name, dir string
	err       error
}

// prompts are the saved prompts from the last snapshot.
func (m *Model) prompts() []api.SavedPrompt {
	if m.snap == nil {
		return nil
	}
	return m.snap.Prompts
}

// pickOpen is p on a session: the saved prompts to choose from.
func (m *Model) pickOpen(r *homeRow) {
	switch {
	case len(m.prompts()) == 0:
		m.setStatus("No saved prompts. Add them to the config: [prompts] then tests = \"run the tests and report\".", true)
	case !canWrite(r.sess):
		m.setStatus("Cannot write to this session now: "+writeHint(r.sess), true)
	default:
		m.home.picking, m.home.pickIdx = true, 0
	}
}

// pickKey handles a key while the saved prompts are open.
func (m *Model) pickKey(k string) tea.Cmd {
	ps := m.prompts()
	switch k {
	case "esc", "q":
		m.home.picking = false
	case "up", "k":
		m.home.pickIdx = max(0, m.home.pickIdx-1)
	case "down", "j":
		m.home.pickIdx = min(len(ps)-1, m.home.pickIdx+1)
	case "enter":
		m.home.picking = false
		r := m.homeSelected()
		if r == nil || r.sess == nil || m.home.pickIdx >= len(ps) {
			return nil
		}
		return m.sendPrompt(r, ps[m.home.pickIdx].Text)
	default:
		// A digit picks by number.
		if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
			if i := int(k[0] - '1'); i < len(ps) {
				m.home.pickIdx = i
				return m.pickKey("enter")
			}
		}
	}
	return nil
}

// pickLines draws the saved prompts for the preview's bottom.
func (m *Model) pickLines(sv *api.SessionView, w int) []string {
	out := []string{"", stTitle.Render("saved prompts") + stDim.Render(" · enter or 1-9 sends · esc closes")}
	for i, p := range m.prompts() {
		if i >= 9 {
			break
		}
		line := fmt.Sprintf("%d %-12s %s", i+1, p.Name, p.Text)
		if i == m.home.pickIdx {
			out = append(out, stSelRow.Render(ansi.Truncate("▌"+line, w, "…")))
			continue
		}
		out = append(out, " "+ansi.Truncate(stAccent.Render(fmt.Sprintf("%d", i+1))+" "+fmt.Sprintf("%-12s ", p.Name)+stDim.Render(p.Text), w-1, "…"))
	}
	return append(out, stDim.Render(ansi.Truncate(writeHint(sv), w, "…")))
}

// newOpen is n: ask for the new session's name. It opens in the selected
// session's folder and terminal, else in the UI's own folder.
func (m *Model) newOpen() {
	if _, ok := m.src.(SessionOpener); !ok {
		m.setStatus("This UI cannot open sessions.", true)
		return
	}
	dir, term := "", ""
	if r := m.homeSelected(); r != nil && r.sess != nil {
		dir = r.sess.Repo
		if canFocus(r.sess.Term) {
			term = r.sess.Term
		}
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	m.home.newing, m.home.newDir, m.home.newTerm, m.home.newName = true, dir, term, lineInput{}
}

// newKey handles a key while the new session's name is asked.
func (m *Model) newKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.home.newing = false
		return nil
	case "enter":
		m.home.newing = false
		op, ok := m.src.(SessionOpener)
		if !ok {
			return nil
		}
		dir, name, term := m.home.newDir, strings.TrimSpace(m.home.newName.Value()), m.home.newTerm
		m.setStatus("Opening a new session in "+shortHome(dir)+"…", false)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return openedMsg{name: name, dir: dir, err: op.NewSession(ctx, dir, name, term)}
		}
	}
	m.home.newName.edit(msg)
	return nil
}

func (m *Model) onOpened(msg openedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus("Not opened: "+oneLine(rpcMessage(msg.err)), true)
	case msg.name != "":
		m.setStatus("Opened "+msg.name+" in "+shortHome(msg.dir)+". It shows here once claude starts.", false)
	default:
		m.setStatus("Opened a new session in "+shortHome(msg.dir)+". It shows here once claude starts.", false)
	}
}

// newLine is the footer while the name is asked.
func (m *Model) newLine() string {
	return "new session in " + shortHome(m.home.newDir) + " · name: " + m.home.newName.view(30) + " · enter opens it · esc cancels"
}
