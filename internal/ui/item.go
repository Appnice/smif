package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// itemScreen is the thread screen of one message (a needs_human item, a
// dead letter or any message): the question or the reason, the message,
// every message of its thread with bodies, its runs, plus the reply box.
type itemScreen struct {
	id        string
	msg       api.MessageView // from the overview; thread.get replaces it
	thread    *api.ThreadView
	threadErr error
	loading   bool
	scroll    int
	draft     lineInput
	typing    bool // the reply box has the keys
	runSel    int  // selected run when runSet, else the newest run
	runSet    bool
	showRun   bool // scroll so that the selected run is on screen
}

// selectedRun is the run under the RUNS cursor, or nil.
func (it *itemScreen) selectedRun() *api.RunView {
	if it.thread == nil || len(it.thread.Runs) == 0 {
		return nil
	}
	i := len(it.thread.Runs) - 1
	if it.runSet {
		i = max(0, min(it.runSel, i))
	}
	return &it.thread.Runs[i]
}

func (it *itemScreen) moveRun(step int) {
	if it.thread == nil || len(it.thread.Runs) == 0 {
		return
	}
	n := len(it.thread.Runs)
	cur := n - 1
	if it.runSet {
		cur = max(0, min(it.runSel, n-1))
	}
	it.runSel, it.runSet, it.showRun = (cur+step+n)%n, true, true
}

// current is the newest view of the item.
func (it *itemScreen) current() api.MessageView {
	if it.thread != nil {
		for _, x := range it.thread.Messages {
			if x.ID == it.id {
				return x
			}
		}
	}
	return it.msg
}

// confirmation is a yes/no question in the bottom box. Only "y" runs yes.
type confirmation struct {
	what  string // "cancel 0000000001", for the "not done" status
	lines []string
	yes   func() tea.Cmd
}

type threadMsg struct {
	id  string
	tv  *api.ThreadView
	err error
}

// actionMsg is the result of a reply, cancel or retry.
type actionMsg struct {
	verb string // reply | cancel | retry
	id   string
	res  *api.SendResult
	err  error
}

func canReply(state string) bool { return state == "needs_human" }
func canRetry(state string) bool { return state == "dead_letter" }
func canCancel(state string) bool {
	switch state {
	case "accepted", "queued", "retry_wait", "needs_human", "running":
		return true
	}
	return false
}

// ItemOpen reports the id of the open item screen, or "".
func (m *Model) ItemOpen() string {
	if m.item == nil {
		return ""
	}
	return m.item.id
}

// Status returns the last action result line ("" when none).
func (m *Model) Status() string { return m.status }

func (m *Model) setStatus(s string, bad bool) {
	m.status, m.statusBad, m.statusAt = s, bad, m.now()
}

// openItem shows the thread of the message under the cursor of the NEEDS
// YOU, DEAD LETTERS or RECENT ACTIVITY panel.
func (m *Model) openItem() tea.Cmd {
	if m.snap == nil || m.rowCount(m.focus) == 0 {
		return nil
	}
	var x api.MessageView
	switch m.focus {
	case PanelNeedsYou:
		x = m.snap.NeedsYou[m.cursor[m.focus]]
	case PanelDLQ:
		x = m.snap.DLQ[m.cursor[m.focus]]
	case PanelRecent:
		x = m.snap.Recent[m.cursor[m.focus]]
	default:
		return nil
	}
	return m.openMessage(x)
}

func (m *Model) loadThread() tea.Cmd {
	it := m.item
	if m.act == nil || it == nil || it.loading {
		return nil
	}
	it.loading = true
	act, id := m.act, it.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tv, err := act.Thread(ctx, id)
		return threadMsg{id: id, tv: tv, err: err}
	}
}

func (m *Model) onThread(msg threadMsg) {
	it := m.item
	if it == nil || it.id != msg.id {
		return
	}
	it.loading = false
	if msg.err != nil {
		it.threadErr = msg.err
		return
	}
	it.thread, it.threadErr = msg.tv, nil
}

// overviewAction handles enter, a, c and t on the overview.
func (m *Model) overviewAction(k string) tea.Cmd {
	if m.snap == nil || m.rowCount(m.focus) == 0 {
		return nil
	}
	i := m.cursor[m.focus]
	switch {
	case k == "enter" && (m.focus == PanelNeedsYou || m.focus == PanelDLQ || m.focus == PanelRecent):
		return m.openItem()
	case k == "enter" && m.focus == PanelRunning:
		r := m.snap.Status.Running[i]
		return m.openRun(r.ID, &r)
	case k == "enter" && m.focus == PanelAgents:
		return m.openInbox(m.snap.Status.Agents[i].ID)
	case k == "enter" && m.focus == PanelEvents:
		ev := m.eventAt(i)
		switch {
		case ev.MessageID != "":
			return m.openMessage(api.MessageView{ID: ev.MessageID, To: ev.Agent, ThreadID: ev.ThreadID})
		case ev.RunID != "":
			return m.openRun(ev.RunID, nil)
		}
		m.setStatus(fmt.Sprintf("Event #%d (%s) has no message or run to open.", ev.Seq, ev.Event), false)
	case k == "a" && m.focus == PanelNeedsYou:
		cmd := m.openItem()
		m.startReply()
		return cmd
	case k == "c" && m.focus == PanelNeedsYou:
		m.askCancel(m.snap.NeedsYou[i])
	case k == "c" && m.focus == PanelRunning:
		r := m.snap.Status.Running[i]
		m.askCancel(api.MessageView{ID: r.MessageID, To: r.Agent, Kind: "run", Intent: r.Handler, State: "running",
			Subject: "run " + Short(r.ID) + ", attempt " + itoa(r.Attempt)})
	case k == "t" && m.focus == PanelDLQ:
		m.askRetry(m.snap.DLQ[i])
	}
	return nil
}

// itemKey handles keys on the item screen (reply box closed).
func (m *Model) itemKey(k string) tea.Cmd {
	it := m.item
	x := it.current()
	switch k {
	case "q":
		return tea.Quit
	case "esc", "backspace", "left", "h":
		m.item = nil
	case "?":
		m.openHelp()
	case "up", "k":
		it.scroll--
	case "down", "j":
		it.scroll++
	case "pgup":
		it.scroll -= 10
	case "pgdown", " ":
		it.scroll += 10
	case "home", "g":
		it.scroll = 0
	case "end", "G":
		it.scroll = 1 << 30
	case "tab":
		it.moveRun(1)
	case "shift+tab":
		it.moveRun(-1)
	case "enter":
		if r := it.selectedRun(); r != nil {
			return m.openRun(r.ID, r)
		}
		m.setStatus("This thread has no runs yet.", true)
	case "r":
		var cmds []tea.Cmd
		if !m.fetching {
			m.fetching = true
			cmds = append(cmds, m.fetch())
		}
		return tea.Batch(append(cmds, m.loadThread())...)
	case "a":
		if !canReply(x.State) {
			m.setStatus("Reply is possible only on a needs_human item; this one is "+x.State+".", true)
			return nil
		}
		m.startReply()
	case "c":
		if !canCancel(x.State) {
			m.setStatus("Cancel is not possible: the item is "+x.State+".", true)
			return nil
		}
		m.askCancel(x)
	case "t":
		if !canRetry(x.State) {
			m.setStatus("Retry is possible only on a dead letter; this one is "+x.State+".", true)
			return nil
		}
		m.askRetry(x)
	}
	return nil
}

func (m *Model) startReply() {
	if m.item != nil {
		m.item.typing = true
	}
}

// inputKey handles keys while the reply box is open.
func (m *Model) inputKey(k tea.KeyPressMsg) tea.Cmd {
	it := m.item
	if it.draft.edit(k) {
		if m.statusBad {
			m.status = ""
		}
		return nil
	}
	switch k.String() {
	case "esc":
		it.typing = false
	case "enter":
		text := strings.TrimSpace(it.draft.Value())
		if text == "" {
			m.setStatus("The reply is empty; type the answer first.", true)
			return nil
		}
		return m.act1("reply", it.id, func(ctx context.Context, a Actions) (*api.SendResult, error) {
			return a.Reply(ctx, it.id, text)
		})
	}
	return nil
}

func (m *Model) askCancel(x api.MessageView) {
	what := fmt.Sprintf("%s %s", x.Kind, x.Intent)
	lines := []string{
		stWarn.Render(fmt.Sprintf("Cancel %s (to %s, %s, %q)?", Short(x.ID), x.To, what, x.Subject)),
		"The item goes to rejected. An asker gets a \"cancelled\" reply. A running worker is stopped.",
	}
	m.confirm = &confirmation{what: "cancel " + Short(x.ID), lines: lines, yes: func() tea.Cmd {
		return m.act1("cancel", x.ID, func(ctx context.Context, a Actions) (*api.SendResult, error) {
			r, err := a.Cancel(ctx, x.ID)
			if err != nil || r == nil {
				return nil, err
			}
			// actionMsg carries a SendResult; only ID and State matter here.
			return &api.SendResult{ID: r.ID, State: r.State}, nil
		})
	}}
}

func (m *Model) askRetry(x api.MessageView) {
	lines := []string{
		stWarn.Render(fmt.Sprintf("Retry dead letter %s (to %s, %s %s, %q)?", Short(x.ID), x.To, x.Kind, x.Intent, x.Subject)),
		"It goes back to the queue with the attempts reset. A worker run can spend budget.",
	}
	m.confirm = &confirmation{what: "retry " + Short(x.ID), lines: lines, yes: func() tea.Cmd {
		return m.act1("retry", x.ID, func(ctx context.Context, a Actions) (*api.SendResult, error) {
			return nil, a.Retry(ctx, x.ID)
		})
	}}
}

// act1 runs one action unless another one is in flight.
func (m *Model) act1(verb, id string, f func(context.Context, Actions) (*api.SendResult, error)) tea.Cmd {
	if m.act == nil {
		m.setStatus("This UI has no daemon connection for actions.", true)
		return nil
	}
	if m.busy != "" {
		m.setStatus("Wait: the "+m.busy+" is still in flight.", true)
		return nil
	}
	m.busy = verb
	m.setStatus(fmt.Sprintf("Sending %s for %s…", verb, Short(id)), false)
	act := m.act
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := f(ctx, act)
		return actionMsg{verb: verb, id: id, res: res, err: err}
	}
}

// onAction shows the result and refreshes the overview and the item.
func (m *Model) onAction(msg actionMsg) tea.Cmd {
	m.busy = ""
	if msg.err != nil {
		m.setStatus(fmt.Sprintf("%s %s failed: %s", capital(msg.verb), Short(msg.id), oneLine(msg.err.Error())), true)
	} else {
		switch msg.verb {
		case "reply":
			s := "Reply sent to " + Short(msg.id) + "."
			if r := msg.res; r != nil {
				s += fmt.Sprintf(" The thread continues as %s (%s).", Short(r.ID), r.State)
			}
			m.setStatus(s, false)
			if m.item != nil && m.item.id == msg.id {
				m.item.draft = lineInput{}
				m.item.typing = false
			}
			if m.home.typing {
				m.home.draft = lineInput{}
				m.home.typing = false
			}
		case "cancel":
			if r := msg.res; r != nil && r.State == api.CancelRejected {
				// The daemon rejected the item at once: that is the result.
				m.setStatus(cancelOutcome(Short(msg.id), r.State, nil), false)
				break
			}
			// "cancelling" (or no state from an older daemon): a running
			// worker must stop first. The status tells the result after a
			// thread read shows that the item left running.
			m.setStatus("Cancel sent for "+Short(msg.id)+": checking the result…", false)
			if m.cancels == nil {
				m.cancels = map[string]*cancelWait{}
			}
			m.cancels[msg.id] = &cancelWait{until: m.now().Add(cancelWaitMax)}
		case "retry":
			m.setStatus("Retried "+Short(msg.id)+": it is back in the queue.", false)
		}
	}
	var cmds []tea.Cmd
	if !m.fetching {
		m.fetching = true
		cmds = append(cmds, m.fetch())
	}
	if c := m.loadThread(); c != nil {
		cmds = append(cmds, c)
	}
	if c := m.checkCancels(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// cancelWaitMax is how long the UI follows a cancel before it reports
// that the item is still running.
const cancelWaitMax = 30 * time.Second

// cancelWait follows one accepted cancel until its item leaves running.
type cancelWait struct {
	until    time.Time
	checking bool // a thread read is in flight
	told     bool // the "still running" status is shown
}

// cancelCheckMsg is a thread read made to learn the result of a cancel.
type cancelCheckMsg struct {
	id  string
	tv  *api.ThreadView
	err error
}

// checkCancels reads the thread of each followed cancel. The model calls
// it after the cancel and after each poll, so a check runs about once a
// poll interval.
func (m *Model) checkCancels() tea.Cmd {
	if m.act == nil || len(m.cancels) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for id, w := range m.cancels {
		if w.checking {
			continue
		}
		w.checking = true
		act, id := m.act, id
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tv, err := act.Thread(ctx, id)
			return cancelCheckMsg{id: id, tv: tv, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// onCancelCheck shows the real result of a cancel when the item is no
// longer running, or a failure when the wait ends first.
func (m *Model) onCancelCheck(msg cancelCheckMsg) {
	w := m.cancels[msg.id]
	if w == nil {
		return
	}
	w.checking = false
	var x *api.MessageView
	var r *api.RunView
	if msg.err == nil && msg.tv != nil {
		for i := range msg.tv.Messages {
			if msg.tv.Messages[i].ID == msg.id {
				x = &msg.tv.Messages[i]
			}
		}
		for i := range msg.tv.Runs {
			if msg.tv.Runs[i].MessageID == msg.id {
				r = &msg.tv.Runs[i] // runs come in start order: the last one wins
			}
		}
	}
	id := Short(msg.id)
	switch {
	case x != nil && x.State != "running":
		delete(m.cancels, msg.id)
		m.setStatus(cancelOutcome(id, x.State, r), x.State != "rejected")
	case !m.now().Before(w.until):
		delete(m.cancels, msg.id)
		now := "still running"
		if x == nil {
			now = "unknown"
			if msg.err != nil {
				now += " (" + oneLine(msg.err.Error()) + ")"
			}
		}
		m.setStatus(fmt.Sprintf("Cancel of %s sent, but after %s the item is %s.", id, cancelWaitMax, now), true)
	case x != nil && !w.told:
		w.told = true
		s := "Cancelling " + id + ": the worker is stopping; the item is still running."
		if r != nil {
			s = "Cancelling " + id + ": run " + Short(r.ID) + " is stopping; the item is still running."
		}
		m.setStatus(s, false)
	}
}

// cancelOutcome is the status line for a cancelled item that is no longer
// running.
func cancelOutcome(id, state string, r *api.RunView) string {
	stopped := r != nil && r.State == "cancelled"
	switch {
	case state == "rejected" && stopped:
		return fmt.Sprintf("Cancelled %s: run %s stopped (cancelled) and the item is rejected.", id, Short(r.ID))
	case state == "rejected":
		return "Cancelled " + id + ": the item is rejected."
	case r != nil:
		return fmt.Sprintf("Cancel of %s came too late: run %s ended %s and the item is %s.", id, Short(r.ID), r.State, state)
	}
	return fmt.Sprintf("Cancel of %s did not apply: the item is %s.", id, state)
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// bottomBox is the confirmation or the reply box, drawn above the footer.
func (m *Model) bottomBox(w int) []string {
	if m.confirm != nil {
		out := []string{stDim.Render(strings.Repeat("─", w))}
		for _, l := range m.confirm.lines {
			out = append(out, "  "+l)
		}
		return append(out, "  "+stTitle.Render("y")+" yes · any other key: no")
	}
	if it := m.item; it != nil && it.typing && m.run == nil {
		x := it.current()
		return []string{
			stDim.Render(strings.Repeat("─", w)),
			stTitle.Render(fmt.Sprintf("  REPLY to %s on %s", x.To, Short(x.ID))) +
				stDim.Render(" · the text goes back to the agent as a reply and its worker continues the thread"),
			"  > " + it.draft.view(max(10, w-5)),
		}
	}
	return nil
}

// footerKeys is the key hint line for the current screen.
func (m *Model) footerKeys() string {
	if m.homeActive() && m.confirm == nil {
		return m.homeKeys()
	}
	switch {
	case m.confirm != nil:
		return "y yes · any other key no · ctrl+c quit"
	case m.help:
		return "↑/↓ scroll · any other key closes the help · q quit"
	case m.history != nil:
		return m.historyKeys()
	case m.run != nil:
		ks := []string{"1 record · 2 transcript · 3 diff", "↑/↓ scroll"}
		if r := m.run.run; r != nil && r.Branch != "" && !m.run.merged() {
			ks = append(ks, "a apply")
		}
		if r := m.run.run; r != nil && r.EndedAt == nil {
			ks = append(ks, "c cancel")
		}
		return strings.Join(append(ks, "r refresh", "esc back", "? help", "q quit"), " · ")
	case m.item != nil && m.item.typing:
		return "enter send · alt+enter new line · ←/→ move · ctrl+u clear · esc close the box (the text stays) · ctrl+c quit"
	case m.item != nil:
		x := m.item.current()
		var ks []string
		if canReply(x.State) {
			ks = append(ks, "a reply")
		}
		if canCancel(x.State) {
			ks = append(ks, "c cancel")
		}
		if canRetry(x.State) {
			ks = append(ks, "t retry")
		}
		if m.item.selectedRun() != nil {
			ks = append(ks, "tab/enter run")
		}
		return strings.Join(append(ks, "↑/↓ scroll", "r refresh", "esc back", "? help", "q quit"), " · ")
	case m.digest != nil:
		return "↑/↓ move · enter open thread · m mark all shown read · r refresh · esc back · ? help · q quit"
	case m.inbox != nil:
		return "↑/↓ move · enter open thread · f/F agent · s/S state · x clear · r refresh · esc back · ? help · q quit"
	}
	ks := []string{"tab panel", "↑/↓ move"}
	if m.snap != nil && m.rowCount(m.focus) > 0 {
		switch m.focus {
		case PanelNeedsYou:
			ks = append(ks, "enter open", "a reply", "c cancel")
		case PanelDLQ:
			ks = append(ks, "enter open", "t retry")
		case PanelRunning:
			ks = append(ks, "enter run", "c cancel")
		case PanelRecent:
			ks = append(ks, "enter thread")
		case PanelAgents:
			ks = append(ks, "enter agent inbox")
		case PanelEvents:
			ks = append(ks, "enter thread or run")
		}
	}
	return strings.Join(append(ks, "i inbox", "d digest", "r refresh", "? help", "q quit"), " · ")
}

// itemLines renders the item screen into exactly avail lines.
func (m *Model) itemLines(w, avail int) []string {
	it := m.item
	x := it.current()
	now := m.now()
	title := "MESSAGE"
	switch x.State {
	case "needs_human":
		title = "NEEDS YOU"
	case "dead_letter":
		title = "DEAD LETTER"
	}
	head := []string{
		stFocus.Render(" "+title+" · "+x.ID+" ") + "  " + stateLabel(x.State),
		fmt.Sprintf("  %s → %s · %s %s · priority %s · attempt %d · handler %s · updated %s ago",
			x.From.Agent, x.To, x.Kind, x.Intent, dash(x.Priority), x.Attempt, dash(x.Handler), dur(now.Sub(x.UpdatedAt))),
		"  subject: " + dash(oneLine(x.Subject)),
	}
	var body []string
	selLine := -1
	wrap := func(indent, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, max(20, w-len(indent)), ""), "\n") {
			body = append(body, indent+l)
		}
	}
	section := func(name string) {
		body = append(body, "", "  "+stHeading.Render(name))
	}
	if x.HumanQuestion != "" {
		section("QUESTION FROM THE WORKER")
		wrap("    ", x.HumanQuestion)
	}
	if x.Reason != "" {
		section("REASON")
		wrap("    ", x.Reason)
	}
	if x.Summary != "" {
		section("SUMMARY")
		wrap("    ", x.Summary)
	}
	section("MESSAGE BODY")
	if x.Body == "" && it.thread == nil && it.threadErr == nil && m.act != nil {
		body = append(body, stDim.Render("    loading…")) // list rows carry no body; thread.get brings it
	} else if x.Body == "" {
		body = append(body, stDim.Render("    (no body)"))
	} else {
		wrap("    ", x.Body)
	}
	if len(x.Data) > 0 {
		wrap("    ", "data: "+string(x.Data))
	}
	for _, a := range x.Attachments {
		body = append(body, "    "+attachmentLine(a))
	}
	tv := it.thread
	switch {
	case tv == nil && it.threadErr != nil:
		section("THREAD")
		body = append(body, stBad.Render("    cannot load the thread: "+oneLine(it.threadErr.Error())))
	case tv == nil && m.act == nil:
		section("THREAD")
		body = append(body, stDim.Render("    not available (no daemon connection)"))
	case tv == nil:
		section("THREAD")
		body = append(body, stDim.Render("    loading…"))
	default:
		name := fmt.Sprintf("THREAD %s · %d messages · %d runs", tv.ThreadID, len(tv.Messages), len(tv.Runs))
		if tv.Frozen {
			name += " · FROZEN: " + tv.FrozenReason
		}
		section(name)
		if it.threadErr != nil {
			body = append(body, stBad.Render("    refresh failed, showing older data: "+oneLine(it.threadErr.Error())))
		}
		for _, y := range tv.Messages {
			mark := "  "
			if y.ID == x.ID {
				mark = "▸ "
			}
			body = append(body, fmt.Sprintf("  %s%s  %s  %s → %s  %s %s  %s  %s", mark, y.CreatedAt.Local().Format("15:04:05"),
				Short(y.ID), y.From.Agent, y.To, y.Kind, y.Intent, y.State, oneLine(summary(y))))
			if y.ID == x.ID {
				continue // its body is above
			}
			if y.HumanQuestion != "" {
				wrap("        ", "needs you: "+y.HumanQuestion)
			}
			if y.Body != "" {
				for _, l := range strings.Split(ansi.Wrap(y.Body, max(20, w-8), ""), "\n") {
					body = append(body, stDim.Render("        "+l))
				}
			}
			for _, a := range y.Attachments {
				body = append(body, "        "+attachmentLine(a))
			}
		}
		section("RUNS")
		if len(tv.Runs) == 0 {
			body = append(body, stDim.Render("    (none)"))
		}
		sel := it.selectedRun()
		for _, r := range tv.Runs {
			hd := r.Handler
			if r.Profile != "" {
				hd += "/" + r.Profile
			}
			end := now
			if r.EndedAt != nil {
				end = *r.EndedAt
			}
			line := fmt.Sprintf("%s  msg %s  %s  attempt %d  %s  %s  %s  %s", Short(r.ID), Short(r.MessageID), hd, r.Attempt,
				r.State, dur(end.Sub(r.StartedAt)), money(r.CostUSD), oneLine(runOutcome(r)))
			if sel != nil && sel.ID == r.ID {
				selLine = len(body)
				body = append(body, "  > "+stCursor.Render(ansi.Truncate(line, max(10, w-4), "…")))
			} else {
				body = append(body, "    "+line)
			}
			if r.Branch != "" {
				body = append(body, stDim.Render(fmt.Sprintf("      branch %s · head %s", r.Branch, dash(shortSHA(r.HeadSHA)))))
			}
		}
	}
	room := avail - len(head)
	if room < 1 {
		return head[:avail]
	}
	if it.showRun && selLine >= 0 {
		if selLine < it.scroll || selLine >= it.scroll+room {
			it.scroll = selLine - room/2
		}
		it.showRun = false
	}
	it.scroll = max(0, min(it.scroll, len(body)-room))
	end := min(len(body), it.scroll+room)
	shown := body[it.scroll:end]
	if len(body) > room {
		head[0] += stDim.Render(fmt.Sprintf("  lines %d-%d of %d", it.scroll+1, end, len(body)))
	}
	return append(head, shown...)
}

func attachmentLine(a envelope.Attachment) string {
	return fmt.Sprintf("attachment: %s (%d bytes, %s) sha256 %s", a.Name, a.Size, dash(a.MIME), shortSHA(a.SHA256))
}

// runOutcome is the result status and summary of a run, or its error.
func runOutcome(r api.RunView) string {
	if r.Error != "" {
		return "error: " + r.Error
	}
	var res struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if len(r.Result) > 0 && json.Unmarshal(r.Result, &res) == nil {
		return strings.TrimSpace(res.Status + ": " + res.Summary)
	}
	return ""
}

func stateLabel(s string) string {
	switch s {
	case "needs_human":
		return stWarn.Render("needs_human")
	case "dead_letter", "rejected", "expired":
		return stBad.Render(s)
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
