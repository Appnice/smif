package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

// The command prompt (`:` on the home screen): type what you want instead
// of hunting for a key. Tab completes commands, agents and intents from
// the daemon's config.

// promptCmd is one prompt command.
type promptCmd struct {
	name, args, what string
}

var promptCmds = []promptCmd{
	{"ask", "<agent> <intent> [text]", "ask an agent; the answer shows in the thread"},
	{"tell", "<agent> <intent> [text]", "send a tell (no answer expected)"},
	{"prompt", "<agent> <text>", "type a prompt into the agent's idle session"},
	{"answer", "<text>", "answer the selected needs-you item"},
	{"retry", "", "retry the selected dead letter"},
	{"cancel", "", "cancel the selected item"},
	{"open", "", "open the selected item"},
	{"agent", "[agent]", "show one agent (empty: all)"},
	{"filter", "[text]", "filter the list (empty: clear)"},
	{"read", "", "mark the handled items read"},
	{"inbox", "", "the full inbox"},
	{"digest", "", "the digest"},
	{"dashboard", "", "the six-panel overview"},
	{"help", "", "every key"},
	{"quit", "", "quit"},
}

// Sender sends a message as the human (message.ask or message.send).
type Sender interface {
	Send(ctx context.Context, method string, d envelope.Draft) (*api.SendResult, error)
}

// promptState is the open prompt.
type promptState struct {
	in  lineInput
	sel int // highlighted suggestion
}

type sentMsg struct {
	kind string
	res  *api.SendResult
	err  error
}

// suggestions completes the last word of the prompt.
func (m *Model) suggestions() []string {
	text := m.prompt.in.Value()
	words := strings.Fields(text)
	if strings.HasSuffix(text, " ") || len(words) == 0 {
		words = append(words, "")
	}
	last := words[len(words)-1]
	var pool []string
	switch len(words) {
	case 1:
		for _, c := range promptCmds {
			pool = append(pool, c.name)
		}
	case 2:
		switch words[0] {
		case "ask", "tell", "agent":
			pool = m.homeAgents()
		case "prompt":
			pool = m.idleAgents()
		}
	case 3:
		if words[0] == "ask" || words[0] == "tell" {
			pool = m.intentsOf(words[1], words[0])
		}
	}
	var out []string
	for _, p := range pool {
		if strings.HasPrefix(p, last) && p != last {
			out = append(out, p)
		}
	}
	return out
}

// intentsOf lists the concrete intents an agent's rules name for a kind
// (from agent.status), plus the intents seen in the list.
func (m *Model) intentsOf(agent, verb string) []string {
	kind := map[string]string{"ask": "ask", "tell": "tell"}[verb]
	seen := map[string]bool{}
	if m.snap != nil {
		for _, a := range m.snap.Status.Agents {
			if a.ID != agent {
				continue
			}
			for _, it := range a.Intents {
				k, name, ok := strings.Cut(it, ":")
				if !ok {
					name, k = it, ""
				}
				if (k == "" || k == kind || k == "*") && name != "*" && !strings.Contains(name, "*") {
					seen[name] = true
				}
			}
		}
		for _, r := range m.homeRowsAll() {
			if r.msg.To == agent && r.msg.Kind == kind {
				seen[r.msg.Intent] = true
			}
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// promptKey edits the prompt; tab completes, enter runs, esc closes.
func (m *Model) promptKey(msg tea.KeyPressMsg) tea.Cmd {
	sug := m.suggestions()
	switch msg.String() {
	case "esc":
		m.prompt = nil
		return nil
	case "tab":
		// Like a shell: one match completes, several fill their common
		// prefix first, then tab takes the highlighted one.
		switch {
		case len(sug) == 1:
			m.complete(sug[0], true)
		case len(sug) > 1:
			if p := commonPrefix(sug); len(p) > len(m.lastWord()) {
				m.complete(p, false)
			} else {
				m.complete(sug[min(m.prompt.sel, len(sug)-1)], true)
			}
		}
		return nil
	case "up", "ctrl+p":
		m.prompt.sel = max(0, m.prompt.sel-1)
		return nil
	case "down", "ctrl+n":
		m.prompt.sel = min(max(0, len(sug)-1), m.prompt.sel+1)
		return nil
	case "enter":
		line := strings.TrimSpace(m.prompt.in.Value())
		m.prompt = nil
		return m.runPrompt(line)
	}
	if m.prompt.in.edit(msg) {
		m.prompt.sel = 0
	}
	return nil
}

func (m *Model) lastWord() string {
	text := m.prompt.in.Value()
	if strings.HasSuffix(text, " ") {
		return ""
	}
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

func commonPrefix(xs []string) string {
	p := xs[0]
	for _, x := range xs[1:] {
		for !strings.HasPrefix(x, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// complete replaces the last word with s (and a space when done).
func (m *Model) complete(s string, done bool) {
	text := m.prompt.in.Value()
	if i := strings.LastIndex(text, " "); i >= 0 {
		text = text[:i+1]
	} else {
		text = ""
	}
	m.prompt.in = lineInput{}
	if done {
		s += " "
	}
	m.prompt.in.insert([]rune(text + s))
	m.prompt.sel = 0
}

// runPrompt runs one command line.
func (m *Model) runPrompt(line string) tea.Cmd {
	words := strings.Fields(line)
	if len(words) == 0 {
		return nil
	}
	rest := func(n int) string {
		if len(words) <= n {
			return ""
		}
		return strings.Join(words[n:], " ")
	}
	r := m.homeSelected()
	switch words[0] {
	case "ask", "tell":
		if len(words) < 3 {
			m.setStatus("Usage: "+words[0]+" <agent> <intent> [text]", true)
			return nil
		}
		return m.send(words[0], words[1], words[2], rest(3))
	case "prompt":
		if len(words) < 3 {
			m.setStatus("Usage: prompt <session name | agent> <text>", true)
			return nil
		}
		return m.promptAgent(words[1], rest(2))
	case "answer", "reply":
		if r == nil || !canReply(r.msg.State) {
			m.setStatus("Select a needs-you item first.", true)
			return nil
		}
		if rest(1) == "" {
			m.home.typing = true
			return nil
		}
		id, text := r.msg.ID, rest(1)
		return m.act1("reply", id, func(ctx context.Context, a Actions) (*api.SendResult, error) {
			return a.Reply(ctx, id, text)
		})
	case "retry", "cancel", "open":
		key := map[string]string{"retry": "t", "cancel": "c", "open": "enter"}[words[0]]
		return m.homeKey(keyPress(key))
	case "agent":
		m.home.agent, m.home.sel = rest(1), ""
		return m.loadPreview(false)
	case "filter":
		m.home.filter = lineInput{}
		m.home.filter.insert([]rune(rest(1)))
		m.home.sel = ""
		return m.loadPreview(false)
	case "read":
		return m.homeMarkRead()
	case "inbox":
		return m.openInbox(m.home.agent)
	case "digest":
		return m.openDigest()
	case "dashboard":
		m.dashboard = true
	case "help":
		m.openHelp()
	case "quit", "q":
		return tea.Quit
	default:
		m.setStatus(fmt.Sprintf("Unknown command %q. Tab lists the commands.", words[0]), true)
	}
	return nil
}

// keyPress is a key event for a named key or one character.
func keyPress(k string) tea.KeyPressMsg {
	if k == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// send sends an ask or a tell as the human and selects the new message.
func (m *Model) send(verb, agent, intent, text string) tea.Cmd {
	snd, ok := m.src.(Sender)
	if !ok {
		m.setStatus("This UI cannot send messages.", true)
		return nil
	}
	known := false
	for _, a := range m.homeAgents() {
		known = known || a == agent
	}
	if !known {
		m.setStatus(fmt.Sprintf("No agent %q. Tab lists the agents.", agent), true)
		return nil
	}
	subject := text
	if subject == "" {
		subject = intent
	}
	if len([]rune(subject)) > 120 {
		subject = string([]rune(subject)[:117]) + "…"
	}
	d := envelope.Draft{To: agent, Kind: map[string]string{"ask": "ask", "tell": "tell"}[verb], Intent: intent,
		Subject: subject, Body: text}
	method := map[string]string{"ask": "message.ask", "tell": "message.send"}[verb]
	m.setStatus(fmt.Sprintf("Sending %s to %s…", verb, agent), false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := snd.Send(ctx, method, d)
		return sentMsg{kind: verb, res: res, err: err}
	}
}

func (m *Model) onSent(msg sentMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(capital(msg.kind)+" failed: "+oneLine(rpcMessage(msg.err)), true)
		return nil
	}
	m.setStatus(fmt.Sprintf("Sent %s %s (%s).", msg.kind, Short(msg.res.ID), msg.res.State), false)
	m.home.agent, m.home.sel = "", msg.res.ID
	if !m.fetching {
		m.fetching = true
		return m.fetch()
	}
	return nil
}

// promptLines is the prompt box with its suggestions, drawn above the
// footer.
func (m *Model) promptLines(w int) []string {
	sug := m.suggestions()
	var out []string
	help := map[string]promptCmd{}
	for _, c := range promptCmds {
		help[c.name] = c
	}
	for i, s := range sug {
		if i == 5 {
			break
		}
		line := "  " + s
		if c, ok := help[s]; ok && len(strings.Fields(m.prompt.in.Value())) <= 1 {
			line = fmt.Sprintf("  %-10s %-26s %s", s, c.args, stDim.Render(c.what))
		}
		if i == min(m.prompt.sel, len(sug)-1) {
			line = stSelRow.Render("▌" + line[1:])
		}
		out = append(out, ansi.Truncate(line, w, "…"))
	}
	out = append(out, stAccent.Render(": ")+m.prompt.in.view(max(10, w-4)))
	return out
}
