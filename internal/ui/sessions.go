package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
)

// The SESSIONS group of the home screen (ADR-0012): the live interactive
// sessions, idle or busy, each with the final message of its last answer
// (answer.go draws it). On an idle one, s opens the box under the preview;
// enter types the text into that session and presses Enter. The text is
// the human's own, so no inbound mail reaches a session (D4).

type promptedMsg struct {
	agent string
	res   *api.SessionPromptResult
	sent  *api.SessionSendResult
	err   error
}

// sessionRow turns a session into a home row.
func sessionRow(sv *api.SessionView) homeRow {
	at := sv.TurnAt
	if at.IsZero() {
		at = sv.LastSeen
	}
	return homeRow{group: grpSessions, sess: sv,
		msg: api.MessageView{ID: "session:" + sv.SessionID, To: sv.Agent, Subject: sv.Turn, UpdatedAt: at}}
}

// sessionPromptOpen opens the box for the selected session.
func (m *Model) sessionPromptOpen(r *homeRow) tea.Cmd {
	if _, ok := m.src.(SessionSender); !ok {
		if _, ok := m.src.(Prompter); !ok {
			m.setStatus("This UI cannot write to sessions.", true)
			return nil
		}
	}
	if !canWrite(r.sess) {
		m.setStatus(writeHint(r.sess), true)
		return nil
	}
	m.home.typing = true
	return nil
}

// sendPrompt sends the box's text to the selected session: typed in when it
// is idle, else to a worker with its context (session.send, ADR-0013).
func (m *Model) sendPrompt(r *homeRow, text string) tea.Cmd {
	if !canWrite(r.sess) {
		m.setStatus(writeHint(r.sess), true)
		return nil
	}
	return m.sendLine(r, strings.Join(strings.Fields(text), " ")) // one line, as typed in the session
}

// sendLine sends one finished line.
func (m *Model) sendLine(r *homeRow, text string) tea.Cmd {
	name, id := sessionName(r.sess), r.sess.SessionID
	if snd, ok := m.src.(SessionSender); ok {
		m.setStatus("Sending to "+name+"…", false)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := snd.SendSession(ctx, id, text)
			return promptedMsg{agent: name, sent: res, err: err}
		}
	}
	pr, ok := m.src.(Prompter)
	if !ok || !r.sess.CanPrompt {
		m.setStatus("This UI cannot write to sessions.", true)
		return nil
	}
	m.setStatus("Typing into "+name+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := pr.Prompt(ctx, id, text)
		return promptedMsg{agent: name, res: res, err: err}
	}
}

func (m *Model) onPrompted(msg promptedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus("Not sent: "+oneLine(rpcMessage(msg.err)), true)
		return nil
	}
	m.home.draft, m.home.typing = lineInput{}, m.home.full
	m.home.part = partState{cleaned: m.home.part.cleaned}
	m.home.fullScroll = 0
	switch {
	case msg.sent != nil && msg.sent.Mode == "worker":
		m.setStatus(msg.agent+" is busy, so a fork of it answers here.", false)
	case msg.sent != nil:
		// The daemon types in the background; a prompt that starts no
		// turn comes back as the session's Unsent (notePending).
		if m.home.pending == nil {
			m.home.pending = map[string]time.Time{}
		}
		m.home.pending[msg.sent.SessionID] = time.Now()
		m.setStatus("Sent to "+msg.agent+".", false)
	default:
		m.setStatus(fmt.Sprintf("Sent to %s (session %s). It is working now.", msg.res.Agent, api.ShortSession(msg.res.SessionID)), false)
	}
	cmds := []tea.Cmd{m.loadPreview(true)}
	if !m.fetching {
		m.fetching = true
		cmds = append(cmds, m.fetch())
	}
	return tea.Batch(cmds...)
}

// tailEntry caches one session's conversation.
type tailEntry struct {
	tail    *api.SessionTail
	err     error
	at      time.Time
	loading bool
}

type tailMsg struct {
	id   string
	tail *api.SessionTail
	err  error
	at   time.Time
}

// loadSessionTail reads the selected session's conversation when the cache
// is older than one poll interval (force: always), so it follows live.
func (m *Model) loadSessionTail(r *homeRow, force bool) tea.Cmd {
	tl, ok := m.src.(SessionTailer)
	if !ok {
		return nil
	}
	if m.home.tails == nil {
		m.home.tails = map[string]*tailEntry{}
	}
	id := r.sess.SessionID
	e := m.home.tails[id]
	if e != nil && (e.loading || (!force && m.now().Sub(e.at) < m.interval)) {
		return nil
	}
	if e == nil {
		e = &tailEntry{}
		m.home.tails[id] = e
	}
	e.loading = true
	now := m.now
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		t, err := tl.SessionTail(ctx, id)
		return tailMsg{id: id, tail: t, err: err, at: now()}
	}
}

func (m *Model) onTail(msg tailMsg) {
	if m.home.tails == nil {
		m.home.tails = map[string]*tailEntry{}
	}
	e := m.home.tails[msg.id]
	if e == nil {
		e = &tailEntry{}
		m.home.tails[msg.id] = e
	}
	e.loading, e.at = false, msg.at
	if msg.err != nil {
		e.err = msg.err
		return
	}
	e.tail, e.err = msg.tail, nil
}

func sessionState(sv *api.SessionView) string {
	switch sv.Turn {
	case "waiting":
		return "waits for you"
	case "idle":
		return "idle"
	case "busy":
		return "working"
	}
	return "open"
}

// sessionName is what the human calls a session: its name (claude --name,
// /rename), else its agent, else its short id.
func sessionName(sv *api.SessionView) string {
	switch {
	case sv.Title != "":
		return sv.Title
	case sv.Agent != "":
		return sv.Agent
	case sv.Repo != "":
		return filepath.Base(sv.Repo)
	}
	return "session " + api.ShortSession(sv.SessionID)
}

// idleAgents are the names (one word, no spaces) of the sessions Smif
// can type into now, then their agents.
func (m *Model) idleAgents() []string {
	if m.snap == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range m.snap.Sessions {
		if s.CanPrompt && s.Title != "" && !strings.ContainsAny(s.Title, " \t") && !seen[s.Title] {
			seen[s.Title] = true
			out = append(out, s.Title)
		}
	}
	for _, s := range m.snap.Sessions {
		if s.CanPrompt && s.Agent != "" && !seen[s.Agent] {
			seen[s.Agent] = true
			out = append(out, s.Agent)
		}
	}
	return out
}

// promptAgent is `: prompt <name | agent> <text>`: the session with that
// name, else the agent's one idle session.
func (m *Model) promptAgent(agent, text string) tea.Cmd {
	var idle []*api.SessionView
	if m.snap != nil {
		for i := range m.snap.Sessions {
			if s := &m.snap.Sessions[i]; strings.EqualFold(s.Title, agent) {
				r := sessionRow(s)
				m.home.sel = r.msg.ID
				return m.sendPrompt(&r, text)
			}
		}
		for i := range m.snap.Sessions {
			if s := &m.snap.Sessions[i]; s.Agent == agent && s.CanPrompt {
				idle = append(idle, s)
			}
		}
	}
	switch len(idle) {
	case 0:
		m.setStatus(fmt.Sprintf("%s has no idle session Smif can type into.", agent), true)
		return nil
	case 1:
		r := sessionRow(idle[0])
		m.home.sel = r.msg.ID
		return m.sendPrompt(&r, text)
	}
	m.setStatus(fmt.Sprintf("%s has %d idle sessions; select one under SESSIONS and press s.", agent, len(idle)), true)
	return nil
}
