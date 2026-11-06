package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// helpEntry is one line of the help screen: the keys and what they do.
// An entry with the keys "#" starts a section. An entry without keys is a
// note.
type helpEntry struct {
	keys, what string
}

func section(name string) helpEntry { return helpEntry{"#", name} }

// helpText lists every key of every screen. TestHelpListsEveryKey keeps
// it in step with the key handlers.
var helpText = []helpEntry{
	section("KEYS: HOME (the start screen: what asks you, sessions with their state, then workers; empty groups hide)"),
	{"↑/k, ↓/j", "move through the list; the preview on the right follows"},
	{"g/home, G/end", "first row, last row"},
	{"tab, shift+tab", "the next or previous row that needs you (a waiting session, a new answer, a question, a dead letter); with none, the next group"},
	{"[ ], ←/h →/l", "show only one agent's rows (the top line names it), previous or next"},
	{"0, 1-9", "all agents, or the agent with that number"},
	{"/", "filter by text, the harness name included (enter keeps it, esc clears it)"},
	{":", "command prompt: ask, tell, prompt, answer, retry, cancel, open, agent, filter, read, inbox, digest, dashboard, help, quit; " +
		"tab completes commands, agents and intents, ↑/↓ choose, enter runs, esc closes"},
	{"esc", "clear the agent and the filter"},
	{"z", "hide or show the quiet sessions (idle, answer read)"},
	{"b", "group SESSIONS by harness, with a count per harness, or back by root folder; the choice stays until you quit"},
	{"enter", "open the thread of the message, the run of a running item, or the chat with a session: type and enter sends " +
		"(typed into it when idle, else queued for when it is idle; ctrl+f asks a fork instead); ↑↓ pgup pgdn scroll, tab the next session that needs you, ctrl+o its tab, ctrl+y copy a code block, esc back"},
	{"a", "answer a needs-you item in the box under the preview (enter sends, alt+enter new line)"},
	{"c", "cancel the selected item; asks y/N first"},
	{"t", "retry a dead letter; asks y/N first"},
	{"e", "mark the handled items read (the • dot)"},
	{"s", "on a session: write to it under the preview (idle: typed into its tab; busy or waiting: queued, nothing is typed now)"},
	{"x", "on a session with queued prompts (the row shows ⧖ 2 queued): open the chat with the cursor on them"},
	{"o", "on a session: bring its terminal tab to the front (a waiting session: answer its permission prompt or question there). " +
		"A [background] session has no tab: o opens claude attach in a new tab"},
	{"y", "on a session: copy the newest code block of its answers, whole and unbroken (in the chat ctrl+y); press again for the block before it"},
	{"p", "on a session: the saved prompts ([prompts] in the config); enter or 1-9 sends one the way s would. In a prompt, @name stands for it"},
	{"n", "open a new terminal tab that runs claude in the selected session's folder (asks for its name). " +
		"With another harness on the PATH, ← → pick it; only claude takes a name"},
	{"R", "history: past Claude Code sessions, the closed ones first (the top line then says 12 sessions closed · R reopens)"},
	{"v", "dashboard: the six-panel overview (agents, events); v or esc there comes back"},
	{"i, d, r, ?, q", "inbox, digest, refresh, this help, quit"},

	section("KEYS: HISTORY (R)"),
	{"↑/k, ↓/j, pgup, pgdown", "move through the list; g/home first row, G/end last row"},
	{"enter", "reopen the session: claude --resume in a new tab, in the session's folder. An open session or a missing folder is refused"},
	{"A", "reopen the closed sessions, at most 20, 500 ms apart; A again takes the rest. died working marks a session that was busy when it ended"},
	{"/", "filter by name, folder or id (enter keeps it, esc clears it)"},
	{"ctrl+s", "search the text of your prompts and Claude's answers for the filter's words, newest session first; it stops at 50 hits or 10 s"},
	{"r, esc", "refresh; esc leaves a search, clears the filter, then goes back"},

	section("KEYS: PROMPT BOX OF A SESSION (s, or the chat)"),
	{"paste", "a text with several lines, or a very long one, becomes a part: [1] in your text, its first and last lines under it, its size, " +
		"and whether it matches the clipboard. A dropped file is a part too"},
	{"ctrl+v", "attach the image on the clipboard (a terminal cannot paste one); with no image, its text as a paste"},
	{"ctrl+p", "move to the parts: ↑↓ choose, enter open it whole, x remove, esc back to the text"},
	{"r, o", "in an open image: r takes the clipboard again, o opens it in the system's viewer (o also for a text)"},
	{"ctrl+f", "busy or waiting session: a read-only fork of it answers now, instead of the queue. Parts cannot go to a fork. Idle session: moves the text cursor right"},
	{"ctrl+q", "move to the queued prompts: ↑↓ choose, x remove, esc back to the text. One goes in each time the session is idle; a prompt not sent in 24 h expires"},
	{"enter", "send: typed in when the session is idle, else queued; parts go as files in that line, never to a fork. A possible secret, a very long line, a very big part or a mismatch " +
		"with the clipboard open a check first: enter sends, e goes back, g shows the first warning"},

	section("KEYS: DASHBOARD (v)"),
	{"tab, →, l", "next panel"},
	{"shift+tab, ←, h", "previous panel"},
	{"↑/k, ↓/j", "move the cursor in the panel"},
	{"g/home, G/end", "first row, last row"},
	{"enter", "open the row: the thread of a message (NEEDS YOU, DEAD LETTERS, RECENT ACTIVITY), a run (RUNNING), " +
		"the inbox of an agent (AGENTS), the thread or run of an event (EVENTS)"},
	{"a", "reply to the needs_human item under the cursor (NEEDS YOU)"},
	{"c", "cancel the item under the cursor (NEEDS YOU, RUNNING); asks y/N first"},
	{"t", "retry the dead letter under the cursor (DEAD LETTERS); asks y/N first, a retry starts a new run"},
	{"i", "inbox: the newest 200 messages of every agent"},
	{"d", "digest: the items handled since the read cursor"},
	{"r", "refresh now (the screen also polls the daemon on its own)"},
	{"?", "this help"},
	{"q, ctrl+c", "quit (ctrl+c quits on every screen, also in the reply box)"},

	section("KEYS: INBOX AND DIGEST"),
	{"↑/k, ↓/j", "move the cursor"},
	{"pgup, pgdown/space", "move 10 rows"},
	{"g/home, G/end", "first row, last row"},
	{"enter", "open the thread of the message"},
	{"f/F", "inbox: next or previous agent filter"},
	{"s/S", "inbox: next or previous state filter"},
	{"x", "inbox: clear both filters"},
	{"m", "digest: mark the shown items read (the read cursor moves past them, not past newer items)"},
	{"esc, backspace", "back to the overview"},

	section("KEYS: THREAD (a message, its thread and its runs)"),
	{"↑/k, ↓/j", "scroll one line"},
	{"pgup, pgdown/space", "scroll 10 lines"},
	{"g/home, G/end", "top, bottom"},
	{"tab, shift+tab", "select the next or previous run"},
	{"enter", "open the selected run"},
	{"a", "reply (only on a needs_human item)"},
	{"c", "cancel the item; asks y/N first"},
	{"t", "retry a dead letter; asks y/N first"},
	{"esc, backspace, ←, h", "back one screen"},

	section("KEYS: REPLY BOX"),
	{"enter", "send the reply; the worker continues the thread"},
	{"alt+enter", "new line"},
	{"←, ctrl+b", "move the text cursor left"},
	{"→, ctrl+f", "move the text cursor right"},
	{"home, ctrl+a", "go to the start of the text"},
	{"end, ctrl+e", "go to the end of the text"},
	{"backspace", "delete the character before the text cursor"},
	{"delete, ctrl+d", "delete the character under the text cursor"},
	{"ctrl+u", "delete from the start of the text to the text cursor"},
	{"ctrl+k", "delete from the text cursor to the end of the text"},
	{"ctrl+w", "delete the word before the text cursor"},
	{"esc", "close the box (the text stays for the next a)"},

	section("KEYS: RUN (record, transcript, diff of the worker branch)"),
	{"1, 2, 3", "record, transcript, diff"},
	{"tab, →, l / shift+tab, ←, h", "next, previous tab"},
	{"↑/k, ↓/j, pgup, pgdown/space", "scroll"},
	{"g/home, G/end", "top, bottom; G on the transcript follows new lines"},
	{"a", "apply: merge the worker branch into the main checkout; asks y/N first"},
	{"c", "cancel the run while it is in progress; asks y/N first"},
	{"esc, backspace", "back one screen"},

	section("KEYS: QUESTIONS AND THIS HELP"),
	{"y", "yes to a question; any other key is no"},
	{"↑/k, ↓/j, pgup, pgdown/space, g, G", "scroll this help"},
	{"q", "quit; any other key closes this help"},

	section("PANELS"),
	{"AGENTS", "queued, running, needs you and dead letter counts, spend today, budget left, and whether an " +
		"interactive Claude Code session is open in the repo"},
	{"RUNNING", "handler runs in progress with the elapsed time"},
	{"NEEDS YOU", "items a worker handed to you; enter shows the question and the thread"},
	{"DEAD LETTERS", "items that failed every attempt; enter shows the reason, t requeues"},
	{"RECENT ACTIVITY", "the newest messages of every agent"},
	{"EVENTS", "the live audit feed, newest first: state changes, routes, runs, human actions"},
	{"", ""},
	{"", "The UI reads the daemon with the human token only. It shows bodies to you, never to an agent."},
}

// helpKeyWidth is the width of the key column of the help screen.
const helpKeyWidth = 22

// helpLines renders helpText for a terminal w cells wide. Long texts wrap
// under their own column.
func helpLines(w int) []string {
	out := []string{""}
	textW := max(20, w-helpKeyWidth-4)
	for _, e := range helpText {
		switch {
		case e.keys == "" && e.what == "":
			out = append(out, "")
		case e.keys == "#":
			if len(out) > 1 {
				out = append(out, "")
			}
			out = append(out, stTitle.Render("  "+e.what))
		case e.keys == "":
			for _, l := range strings.Split(ansi.Wrap(e.what, max(20, w-4), ""), "\n") {
				out = append(out, "  "+l)
			}
		default:
			keys := e.keys
			pad := helpKeyWidth - ansi.StringWidth(keys)
			if pad < 2 {
				// The keys take their own line; the text goes under the column.
				out = append(out, "  "+keys)
				keys, pad = "", helpKeyWidth
			}
			for i, l := range strings.Split(ansi.Wrap(e.what, textW, ""), "\n") {
				if i > 0 {
					keys, pad = "", helpKeyWidth
				}
				out = append(out, "  "+keys+strings.Repeat(" ", pad)+l)
			}
		}
	}
	return out
}

func (m *Model) openHelp() {
	m.help, m.helpScroll = true, 0
}

// helpKey handles a key on the help screen: scroll keys scroll, q quits,
// every other key closes the help.
func (m *Model) helpKey(k string) tea.Cmd {
	switch k {
	case "q", "ctrl+c":
		return tea.Quit
	case "up", "k":
		m.helpScroll--
	case "down", "j":
		m.helpScroll++
	case "pgup":
		m.helpScroll -= 10
	case "pgdown", " ":
		m.helpScroll += 10
	case "home", "g":
		m.helpScroll = 0
	case "end", "G":
		m.helpScroll = 1 << 30
	default:
		m.help = false
	}
	return nil
}

// helpView is the help screen in avail lines. When the help is taller,
// the first line says which lines show.
func (m *Model) helpView(w, avail int) []string {
	ls := helpLines(w)
	if len(ls) <= avail {
		m.helpScroll = 0
		return ls
	}
	room := max(1, avail-1)
	m.helpScroll = max(0, min(m.helpScroll, len(ls)-room))
	stop := min(len(ls), m.helpScroll+room)
	head := stDim.Render(fmt.Sprintf("  HELP · lines %d-%d of %d · ↑/↓ scroll · q quit · any other key closes", m.helpScroll+1, stop,
		len(ls)))
	return append([]string{head}, ls[m.helpScroll:stop]...)
}
