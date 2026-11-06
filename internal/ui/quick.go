package ui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/harness"
)

// Saved prompts and new sessions (ADR-0015). p on a session opens the
// saved prompts ([prompts] in the config); enter sends the chosen one the
// way s would (typed in when idle, else queued). n opens a new
// terminal tab that runs claude, or another harness (ADR-0021), in the
// selected session's folder.

// SessionOpener opens a new session in a terminal tab (session.new).
type SessionOpener interface {
	NewSession(ctx context.Context, dir, name, term, harness string) error
}

// RemoteOpener starts a session in tmux on another machine (ADR-0024).
type RemoteOpener interface {
	NewRemoteSession(ctx context.Context, host, dir, name string) error
}

// NewRemoteSession starts claude on that machine.
func (s *DaemonSource) NewRemoteSession(ctx context.Context, host, dir, name string) error {
	return s.callT(ctx, "session.new", api.SessionNewParams{Host: host, Dir: dir, Name: name}, nil, 25*time.Second)
}

// NewSession opens a new terminal tab that runs a harness in dir ("" =
// claude). The daemon builds the command from the harness's name.
func (s *DaemonSource) NewSession(ctx context.Context, dir, name, term, harness string) error {
	return s.callT(ctx, "session.new", api.SessionNewParams{Dir: dir, Name: name, Term: term, Harness: harness}, nil, 20*time.Second)
}

type openedMsg struct {
	name, dir, harness string
	on                 string // the other machine it started on
	err                error
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

// harnessChoices are the agents n can start. With one or none, n asks
// nothing about the harness.
func (m *Model) harnessChoices() []string {
	if m.snap == nil {
		return nil
	}
	return m.snap.Harnesses
}

// newIsClaude reports that the new session runs Claude Code, the only
// harness that takes --name.
func (m *Model) newIsClaude() bool {
	return m.home.newHarness == "" || m.home.newHarness == harness.ClaudeName
}

// newCycle moves the harness of the new session, round the list.
func (m *Model) newCycle(step int) {
	hs := m.harnessChoices()
	i := max(0, slices.Index(hs, m.home.newHarness))
	m.home.newHarness = hs[(i+step+len(hs))%len(hs)]
}

// newOpen is n: ask for the harness when there is a choice, and for the new
// session's name. It opens in the selected session's folder and terminal,
// else in the UI's own folder.
func (m *Model) newOpen() {
	if _, ok := m.src.(SessionOpener); !ok {
		m.setStatus("This UI cannot open sessions.", true)
		return
	}
	dir, term := "", ""
	m.home.newHost = ""
	if r := m.homeSelected(); r != nil && r.sess != nil && r.sess.Host != "" {
		if !r.sess.Writable {
			m.setStatus(r.sess.HostLabel()+" is read only. Press w on it under Machines (m) to allow writing.", true)
			return
		}
		m.home.newHost, m.home.newLabel = r.sess.Host, r.sess.HostLabel()
		m.home.newing, m.home.newDir, m.home.newTerm, m.home.newName = true, r.sess.HostDir, "", lineInput{}
		m.home.newHarness = ""
		return
	}
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
	m.home.newHarness = ""
	if hs := m.harnessChoices(); len(hs) > 1 {
		m.home.newHarness = hs[0]
	}
}

// newKey handles a key while the new session is asked for. With a choice
// of harness, left and right change it (ctrl+b and ctrl+f still move the
// text cursor). Only claude takes a name.
func (m *Model) newKey(msg tea.KeyPressMsg) tea.Cmd {
	switch k := msg.String(); k {
	case "esc":
		m.home.newing = false
		return nil
	case "left", "right":
		if m.home.newHost == "" && len(m.harnessChoices()) > 1 {
			step := 1
			if k == "left" {
				step = -1
			}
			m.newCycle(step)
			return nil
		}
	case "enter":
		m.home.newing = false
		op, ok := m.src.(SessionOpener)
		if !ok {
			return nil
		}
		dir, name, term, h := m.home.newDir, strings.TrimSpace(m.home.newName.Value()), m.home.newTerm, m.home.newHarness
		if !m.newIsClaude() {
			name = ""
		}
		if host := m.home.newHost; host != "" {
			r, ok := m.src.(RemoteOpener)
			if !ok {
				m.setStatus("This UI cannot open sessions on another machine.", true)
				return nil
			}
			label := m.home.newLabel
			m.setStatus("Starting it on "+label+", in tmux…", false)
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return openedMsg{name: name, dir: dir, on: label, err: r.NewRemoteSession(ctx, host, dir, name)}
			}
		}
		m.setStatus("Opening a new session in "+shortHome(dir)+"…", false)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return openedMsg{name: name, dir: dir, harness: h, err: op.NewSession(ctx, dir, name, term, h)}
		}
	}
	if m.newIsClaude() {
		m.home.newName.edit(msg)
	}
	return nil
}

func (m *Model) onOpened(msg openedMsg) {
	agent := msg.harness
	if agent == "" {
		agent = harness.ClaudeName
	}
	switch {
	case msg.err != nil:
		m.setStatus("Not opened: "+oneLine(rpcMessage(msg.err)), true)
	case msg.on != "":
		m.setStatus("Started on "+msg.on+". A window opens there if it has a desktop, or run tmux attach there.", false)
	case msg.name != "":
		m.setStatus("Opened "+msg.name+" in "+shortHome(msg.dir)+". It shows here once "+agent+" starts.", false)
	default:
		m.setStatus("Opened a new session in "+shortHome(msg.dir)+". It shows here once "+agent+" starts.", false)
	}
}

// newLine is the footer while the new session is asked for.
func (m *Model) newLine() string {
	parts := []string{"new session in " + shortHome(m.home.newDir)}
	if m.home.newHost != "" {
		parts[0] = "new session on " + m.home.newLabel + " in " + m.home.newDir + ", in tmux"
	}
	if m.home.newHarness != "" {
		parts = append(parts, "← "+m.home.newHarness+" →")
	}
	if m.newIsClaude() {
		parts = append(parts, "name: "+m.home.newName.view(30))
	}
	return strings.Join(append(parts, "enter opens it", "esc cancels"), " · ")
}
