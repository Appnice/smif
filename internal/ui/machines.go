package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/machines"
)

// The Machines screen (ADR-0024): this machine, the ones you added and the
// ones smif found. Enter adds one, and the setup runs in the row, so you can
// move on and add another while it runs. Passwords live in the box until
// enter, go to the setup, and are wiped from the box.

// MachineDaemon is what the screen needs from the daemon.
type MachineDaemon interface {
	machines.Daemon
	// Remotes is the daemon's last look at each host in daemon.remotes.
	Remotes() ([]api.ProviderCheck, error)
}

type machineKind int

const (
	mHere machineKind = iota
	mAdded
	mFound  // ssh answers
	mClosed // ssh does not answer
	mRun    // the setup runs
	mFailed
)

type machineCheck struct {
	state  machines.State
	detail string
}

type machineRow struct {
	name, target, source string
	kind                 machineKind
	detail               string // an added host's last look
	bad                  string // why a setup stopped, or why a host is away
	checks               map[machines.Step]machineCheck
	cancel               context.CancelFunc

	closed  bool        // the setup waits for ssh
	ask     *machineAsk // the question it waits on
	askUser string      // the user the login proposes
	problem string
	skipped bool // linger stayed off
}

// machineAsk is a question from the setup. reply gets one answer.
type machineAsk struct {
	sudo  bool
	reply chan machineAnswer
}

type machineAnswer struct {
	user, pass string
	ok         bool
}

type machineField int

const (
	fieldNone machineField = iota
	fieldAddr
	fieldUser
	fieldSecret
)

// machineHit is a place the mouse can click.
type machineHit struct {
	y, x0, x1 int
	do        func() tea.Cmd
}

type machinesScreen struct {
	open      bool
	rows      []*machineRow
	cursor    int
	scanning  bool
	field     machineField
	addr      lineInput
	user      lineInput
	secret    lineInput
	events    chan tea.Msg
	listening bool
	hits      []machineHit
	start     func(context.Context, *machines.Setup) error
	discover  func(context.Context, map[string]bool) []machines.Found
}

type (
	machinesFoundMsg struct{ rows []machines.Found }
	machineHealthMsg struct{ rows []api.ProviderCheck }
	machineEventMsg  struct {
		name string
		e    machines.Event
	}
	machineClosedMsg struct{ name string }
	machineAskMsg    struct {
		name    string
		ask     *machineAsk
		user    string
		problem string
	}
	machineDoneMsg struct {
		name string
		host string
		err  error
	}
)

// uiAsk puts the setup's questions on the screen.
type uiAsk struct {
	name   string
	ctx    context.Context
	events chan tea.Msg
}

func (a *uiAsk) send(msg tea.Msg) {
	select {
	case a.events <- msg:
	case <-a.ctx.Done():
	}
}

func (a *uiAsk) wait(ask *machineAsk) machineAnswer {
	select {
	case r := <-ask.reply:
		return r
	case <-a.ctx.Done():
		return machineAnswer{}
	}
}

func (a *uiAsk) Closed(string) { a.send(machineClosedMsg{a.name}) }

func (a *uiAsk) Login(user, problem string) (string, string, bool) {
	ask := &machineAsk{reply: make(chan machineAnswer, 1)}
	a.send(machineAskMsg{name: a.name, ask: ask, user: user, problem: problem})
	r := a.wait(ask)
	return r.user, r.pass, r.ok
}

func (a *uiAsk) Sudo(problem string) (string, bool) {
	ask := &machineAsk{sudo: true, reply: make(chan machineAnswer, 1)}
	a.send(machineAskMsg{name: a.name, ask: ask, problem: problem})
	r := a.wait(ask)
	return r.pass, r.ok
}

// Install is yes: the row said so before enter.
func (a *uiAsk) Install() bool { return true }

func (m *Model) machinesOpen() bool { return m.mach.open }

// openMachines shows the screen and looks for machines.
func (m *Model) openMachines() tea.Cmd {
	if m.machDaemon == nil {
		m.setStatus("This UI cannot add machines.", true)
		return nil
	}
	s := &m.mach
	s.open = true
	if s.events == nil {
		s.events = make(chan tea.Msg, 16)
	}
	if s.start == nil {
		s.start = func(ctx context.Context, x *machines.Setup) error { return x.Run(ctx) }
	}
	if s.discover == nil {
		s.discover = machines.Discover
	}
	if len(s.rows) == 0 {
		s.rows = []*machineRow{{name: "This machine", kind: mHere}}
		s.cursor = 0
	}
	return tea.Batch(m.scanMachines(), m.loadRemotes(), m.listenMachines())
}

func (m *Model) closeMachines() {
	m.mach.open = false
	m.mach.field = fieldNone
	m.mach.secret = lineInput{}
}

func (m *Model) listenMachines() tea.Cmd {
	s := &m.mach
	if s.listening || s.events == nil {
		return nil
	}
	s.listening = true
	ch := s.events
	return func() tea.Msg { return <-ch }
}

func (m *Model) scanMachines() tea.Cmd {
	s := &m.mach
	if s.scanning {
		return nil
	}
	s.scanning = true
	skip := map[string]bool{}
	for _, r := range s.rows {
		if r.kind == mAdded || r.kind == mRun {
			skip[r.target] = true
		}
	}
	find := s.discover
	return func() tea.Msg { return machinesFoundMsg{find(context.Background(), skip)} }
}

func (m *Model) loadRemotes() tea.Cmd {
	d := m.machDaemon
	if d == nil {
		return nil
	}
	return func() tea.Msg {
		rows, err := d.Remotes()
		if err != nil {
			return nil
		}
		return machineHealthMsg{rows}
	}
}

// sameMachine says that two ssh targets are one machine, with or without a user.
func sameMachine(a, b string) bool {
	return a != "" && machineName(a) == machineName(b)
}

func machineName(target string) string { return target[strings.Index(target, "@")+1:] }

func (s *machinesScreen) row(name string) *machineRow {
	for _, r := range s.rows {
		if r.name == name {
			return r
		}
	}
	return nil
}

func (s *machinesScreen) selected() *machineRow {
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return nil
	}
	return s.rows[s.cursor]
}

// sortRows keeps the groups in order and the cursor on its row.
func (s *machinesScreen) sortRows() {
	cur := s.selected()
	group := func(k machineKind) int {
		switch k {
		case mHere:
			return 0
		case mAdded:
			return 1
		}
		return 2
	}
	var out []*machineRow
	for g := 0; g < 3; g++ {
		for _, r := range s.rows {
			if group(r.kind) == g {
				out = append(out, r)
			}
		}
	}
	s.rows = out
	for i, r := range s.rows {
		if r == cur {
			s.cursor = i
		}
	}
}

func (m *Model) onMachinesFound(msg machinesFoundMsg) {
	s := &m.mach
	s.scanning = false
	for _, f := range msg.rows {
		r := s.byTarget(f.Target)
		if r == nil {
			r = s.byTarget(f.Name)
		}
		if r == nil {
			r = &machineRow{name: f.Name, target: f.Target, kind: mFound}
			s.rows = append(s.rows, r)
		}
		if r.source == "" || r.source == "daemon.remotes" {
			r.source = f.Source
		}
		if r.kind == mFound || r.kind == mClosed {
			r.kind = mClosed
			if f.Open {
				r.kind = mFound
			}
		}
	}
	s.sortRows()
	if s.cursor == 0 && len(s.rows) > 1 {
		for i, r := range s.rows {
			if r.kind == mFound {
				s.cursor = i
				break
			}
		}
	}
}

// onMachineHealth takes the added hosts from the daemon, which has the truth.
func (m *Model) onMachineHealth(msg machineHealthMsg) {
	s := &m.mach
	seen := map[string]bool{}
	for _, p := range msg.rows {
		seen[p.Name] = true
		r := s.byTarget(p.Name)
		if r == nil {
			r = &machineRow{name: machineName(p.Name), target: p.Name, source: "daemon.remotes", kind: mAdded}
			s.rows = append(s.rows, r)
		}
		if r.kind == mRun {
			continue
		}
		r.target, r.kind, r.detail, r.bad = p.Name, mAdded, p.Detail, ""
		if p.State != "ok" {
			r.bad = p.Detail
		}
	}
	for _, r := range s.rows {
		if r.kind == mAdded && !seen[r.target] && r.source != "daemon.remotes" {
			r.kind, r.detail, r.bad = mFound, "", ""
		}
	}
	kept := s.rows[:0]
	for _, r := range s.rows {
		if r.kind != mAdded || seen[r.target] || r.source != "daemon.remotes" {
			kept = append(kept, r)
		}
	}
	s.rows = kept
	s.cursor = min(s.cursor, len(s.rows)-1)
	s.sortRows()
}

func (s *machinesScreen) byTarget(target string) *machineRow {
	for _, r := range s.rows {
		if sameMachine(r.target, target) || r.name == target {
			return r
		}
	}
	return nil
}

func (m *Model) onMachineEvent(msg machineEventMsg) {
	if r := m.mach.row(msg.name); r != nil {
		r.checks[msg.e.Step] = machineCheck{msg.e.State, msg.e.Detail}
		if msg.e.Step == machines.StepReach && msg.e.State == machines.Done {
			r.closed = false
		}
		if msg.e.Step == machines.StepLinger && msg.e.State == machines.Skipped {
			r.skipped = true
		}
	}
}

func (m *Model) onMachineAsk(msg machineAskMsg) {
	s := &m.mach
	r := s.row(msg.name)
	if r == nil {
		msg.ask.reply <- machineAnswer{}
		return
	}
	r.ask, r.askUser, r.problem = msg.ask, msg.user, msg.problem
	if s.selected() == r {
		m.focusAsk(r)
	}
}

// focusAsk gives the box the keys when the selected row asks.
func (m *Model) focusAsk(r *machineRow) {
	s := &m.mach
	s.secret = lineInput{}
	s.field = fieldSecret
	s.user = lineInput{}
	s.user.insert([]rune(r.askUser))
}

func (m *Model) onMachineDone(msg machineDoneMsg) tea.Cmd {
	s := &m.mach
	r := s.row(msg.name)
	if r == nil {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.ask, r.closed = nil, false
	if s.selected() == r {
		s.field, s.secret = fieldNone, lineInput{}
	}
	switch {
	case errors.Is(msg.err, machines.ErrStopped):
		r.kind, r.checks = mFound, nil
		m.setStatus("Stopped "+r.name+". Nothing was added.", false)
	case msg.err != nil:
		r.kind, r.bad = mFailed, msg.err.Error()
	default:
		r.target, r.kind, r.bad = msg.host, mAdded, ""
		r.detail = "connected"
		m.setStatus(r.name+" is in your list.", false)
	}
	s.sortRows()
	return m.loadRemotes()
}

// begin starts the setup of a row.
func (m *Model) beginMachine(r *machineRow) tea.Cmd {
	s := &m.mach
	ctx, cancel := context.WithCancel(context.Background())
	r.kind, r.bad, r.closed, r.ask, r.problem, r.skipped = mRun, "", false, nil, "", false
	r.cancel, r.checks = cancel, map[machines.Step]machineCheck{}
	name, events, start, daemon := r.name, s.events, s.start, m.machDaemon
	setup := &machines.Setup{Host: r.target, Ask: &uiAsk{name, ctx, events}, Daemon: daemon, Report: func(e machines.Event) {
		select {
		case events <- machineEventMsg{name, e}:
		case <-ctx.Done():
		}
	}}
	go func() {
		err := start(ctx, setup)
		events <- machineDoneMsg{name, setup.Host, err}
	}()
	s.sortRows()
	return nil
}

func (m *Model) addAddress() tea.Cmd {
	s := &m.mach
	host := strings.TrimSpace(s.addr.Value())
	if host == "" {
		return nil
	}
	if !config.ValidRemote(host) {
		m.setStatus("That is not an ssh address. Try me@192.168.1.20.", true)
		return nil
	}
	s.addr = lineInput{}
	s.field = fieldNone
	r := s.byTarget(host)
	if r == nil {
		r = &machineRow{name: host[strings.Index(host, "@")+1:], target: host, source: "typed in", kind: mFound}
		s.rows = append(s.rows, r)
	}
	s.sortRows()
	for i, x := range s.rows {
		if x == r {
			s.cursor = i
		}
	}
	return m.beginMachine(r)
}

func (m *Model) stopMachine(r *machineRow) {
	if r.cancel != nil {
		r.cancel()
	}
}

func (m *Model) answerAsk(r *machineRow, a machineAnswer) {
	s := &m.mach
	if r.ask == nil {
		return
	}
	r.ask.reply <- a
	r.ask, r.problem = nil, ""
	s.secret = lineInput{}
	s.field = fieldNone
}

func (m *Model) removeMachine(r *machineRow) {
	target := r.target
	m.confirm = &confirmation{what: "remove " + r.name, lines: []string{"Remove " + r.name + " from your list?"}, yes: func() tea.Cmd {
		removed, err := config.RemoveRemote("", target)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		if removed {
			if err := m.machDaemon.Reload(); err != nil {
				m.setStatus(err.Error(), true)
				return nil
			}
		}
		m.setStatus(target+" left your list. Its key stays there, and can only list sessions.", false)
		return m.loadRemotes()
	}}
}

func (m *Model) moveMachine(d int) {
	s := &m.mach
	if s.field == fieldSecret || s.field == fieldUser {
		s.field = fieldNone
		s.secret = lineInput{}
	}
	s.cursor = min(max(s.cursor+d, 0), len(s.rows)-1)
	if r := s.selected(); r != nil && r.ask != nil {
		m.focusAsk(r)
	}
}

func (m *Model) enterMachine() tea.Cmd {
	s := &m.mach
	r := s.selected()
	if r == nil {
		return nil
	}
	switch {
	case r.ask != nil:
		pass := s.secret.Value()
		if pass == "" {
			m.setStatus("Type the password first.", true)
			return nil
		}
		m.answerAsk(r, machineAnswer{user: s.user.Value(), pass: pass, ok: true})
	case r.kind == mFound || r.kind == mClosed || r.kind == mFailed:
		return m.beginMachine(r)
	}
	return nil
}

func (m *Model) escMachine() tea.Cmd {
	s := &m.mach
	r := s.selected()
	switch {
	case s.field == fieldAddr:
		s.field = fieldNone
	case r != nil && r.ask != nil && r.ask.sudo:
		m.answerAsk(r, machineAnswer{}) // skip
	case r != nil && r.ask != nil:
		m.stopMachine(r)
	default:
		m.closeMachines()
	}
	return nil
}

func (m *Model) machinesKey(msg tea.KeyPressMsg) tea.Cmd {
	s := &m.mach
	k := msg.String()
	var in *lineInput
	switch s.field {
	case fieldAddr:
		in = &s.addr
	case fieldUser:
		in = &s.user
	case fieldSecret:
		in = &s.secret
	}
	if in != nil {
		if k == "tab" {
			if r := s.selected(); r != nil && r.ask != nil && !r.ask.sudo {
				s.field = map[machineField]machineField{fieldUser: fieldSecret, fieldSecret: fieldUser}[s.field]
			}
			return nil
		}
		if in.edit(msg) {
			return nil
		}
		switch k {
		case "enter":
			if s.field == fieldAddr {
				return m.addAddress()
			}
			return m.enterMachine()
		case "esc":
			return m.escMachine()
		}
		return nil
	}
	r := s.selected()
	switch k {
	case "up", "k":
		m.moveMachine(-1)
	case "down", "j":
		m.moveMachine(1)
	case "enter":
		return m.enterMachine()
	case "esc", "q":
		return m.escMachine()
	case "a":
		s.field = fieldAddr
	case "r":
		return tea.Batch(m.scanMachines(), m.loadRemotes())
	case "x":
		if r != nil && r.kind == mAdded {
			m.removeMachine(r)
		} else if r != nil && r.kind == mRun {
			m.stopMachine(r)
		}
	case "y":
		if r != nil && r.target != "" {
			return m.copyText("ssh "+r.target, "Copied: ssh "+r.target)
		}
	case "1":
		if r != nil && (r.kind == mClosed || r.closed) {
			return m.copyText(machines.CommandLinux, "Copied the Linux command.")
		}
	case "2":
		if r != nil && (r.kind == mClosed || r.closed) {
			return m.copyText(machines.CommandMac, "Copied the macOS command.")
		}
	case "?":
		m.help = true
	}
	return nil
}

func (m *Model) copyText(text, status string) tea.Cmd {
	clip := m.clip
	return func() tea.Msg { return copiedMsg{status: status, err: clip(text)} }
}

// machinesClick acts on the place under the mouse.
func (m *Model) machinesClick(x, y int) tea.Cmd {
	for _, h := range m.mach.hits {
		if y == h.y && x >= h.x0 && x < h.x1 {
			return h.do()
		}
	}
	return nil
}

func (m *Model) machinesPaste(text string) {
	s := &m.mach
	clean := []rune(strings.ReplaceAll(text, "\n", " "))
	switch s.field {
	case fieldAddr:
		s.addr.insert(clean)
	case fieldUser:
		s.user.insert(clean)
	case fieldSecret:
		s.secret.insert(clean)
	}
}

func (m *Model) machinesKeys() string {
	s := &m.mach
	r := s.selected()
	switch {
	case s.field == fieldAddr:
		return "type an address · enter add · esc back"
	case r != nil && r.ask != nil && r.ask.sudo:
		return "enter turn on · esc skip"
	case r != nil && r.ask != nil:
		return "tab next field · enter log in · esc stop"
	}
	ks := []string{"↑↓ machines"}
	if r != nil {
		switch r.kind {
		case mFound, mClosed:
			ks = append(ks, "enter add")
		case mFailed:
			ks = append(ks, "enter try again")
		case mAdded:
			ks = append(ks, "x remove", "y copy ssh")
		case mRun:
			ks = append(ks, "x stop")
		}
		if r.kind == mClosed || r.closed {
			ks = append(ks, "1 copy Linux", "2 copy macOS")
		}
	}
	return strings.Join(append(ks, "a address", "r look again", "esc back"), " · ")
}

func (m *Model) machinesTop(w int) string {
	left := stBadge.Render(" smif ") + "  " + stDim.Render("Sessions") + "  " + stTabOn.Render("Machines")
	if m.mach.scanning {
		left += "  " + stDim.Render("looking…")
	}
	return ansi.Truncate(left, w, "…")
}

func (m *Model) machineSessions(r *machineRow) []api.SessionView {
	if m.snap == nil {
		return nil
	}
	var out []api.SessionView
	for _, v := range m.snap.Sessions {
		if r.kind == mHere && v.Host == "" || r.kind != mHere && v.Host == r.target {
			out = append(out, v)
		}
	}
	return out
}

// mark is a row's state. The word stays in NO_COLOR.
func (m *Model) machineMark(r *machineRow) (string, string, func(string) string) {
	plain := func(st func(...string) string) func(string) string { return func(s string) string { return st(s) } }
	n := len(m.machineSessions(r))
	sessions := fmt.Sprintf("%d sessions", n)
	switch r.kind {
	case mHere:
		return "●", sessions, plain(stOK.Render)
	case mAdded:
		if r.bad != "" {
			return "!", "away", plain(stWarn.Render)
		}
		return "●", sessions, plain(stOK.Render)
	case mFound:
		return "○", "ssh open", plain(stDim.Render)
	case mClosed:
		return "○", "ssh closed", plain(stWarn.Render)
	case mFailed:
		return "✗", "failed", plain(stBad.Render)
	}
	if r.ask != nil {
		return "!", "needs you", plain(stWarn.Render)
	}
	if r.closed {
		return "◐", "waits for ssh", plain(stLive.Render)
	}
	done := 0
	for _, c := range r.checks {
		if c.state == machines.Done || c.state == machines.Skipped {
			done++
		}
	}
	return "▶", fmt.Sprintf("%d/%d", done, len(machines.Steps)), plain(stLive.Render)
}

// machinesLines draws the two panes, or one under the other in a narrow
// terminal, and notes where the mouse can click. top is the first screen line.
func (m *Model) machinesLines(w, avail, top int) []string {
	s := &m.mach
	s.hits = s.hits[:0]
	listLines, listHits := m.machineList()
	if w < 90 {
		listH := min(len(listLines)+2, max(avail/2, 6))
		detailH := max(avail-listH, 3)
		s.addHits(listHits, 2, top+1, w-4)
		out := box("MACHINES", listLines, w, listH, s.field == fieldNone)
		detail, detailHits := m.machineDetail(w - 4)
		s.addHits(detailHits, 2, top+listH+1, w-4)
		return append(out, box(m.machineTitle(), detail, w, detailH, s.field != fieldNone)...)
	}
	listW := max(40, w*2/5)
	detailW := w - listW
	s.addHits(listHits, 2, top+1, listW-4)
	left := box("MACHINES", listLines, listW, avail, s.field == fieldNone)
	detail, detailHits := m.machineDetail(detailW - 4)
	s.addHits(detailHits, listW+2, top+1, detailW-4)
	right := box(m.machineTitle(), detail, detailW, avail, s.field != fieldNone)
	out := make([]string, len(left))
	for i := range left {
		out[i] = left[i] + right[i]
	}
	return out
}

// lineHit is a click target inside a pane: its line, and columns from the pane's left edge.
type lineHit struct {
	line, x0, x1 int
	do           func() tea.Cmd
}

func (s *machinesScreen) addHits(hits []lineHit, left, top, width int) {
	for _, h := range hits {
		x1 := h.x1
		if x1 < 0 {
			x1 = width
		}
		s.hits = append(s.hits, machineHit{y: top + h.line, x0: left + h.x0, x1: left + x1, do: h.do})
	}
}

func (m *Model) machineTitle() string {
	if r := m.mach.selected(); r != nil {
		return strings.ToUpper(r.name)
	}
	return ""
}

func (m *Model) machineList() ([]string, []lineHit) {
	s := &m.mach
	var out []string
	var hits []lineHit
	last := ""
	for i, r := range s.rows {
		head := "FOUND"
		switch r.kind {
		case mHere:
			head = "THIS MACHINE"
		case mAdded:
			head = "ADDED"
		}
		if head != last {
			if last != "" {
				out = append(out, "")
			}
			out = append(out, stDim.Render(head))
			last = head
		}
		glyph, note, st := m.machineMark(r)
		bar := "  "
		name := r.name
		if i == s.cursor {
			bar, name = stAccent.Render("▌")+" ", stSelRow.Render(name)
		}
		left := bar + st(glyph) + " " + name
		out = append(out, left+"  "+st(note))
		hits = append(hits, lineHit{len(out) - 1, 0, -1, m.pickMachine(i)})
	}
	out = append(out, "", stDim.Render("SOMEWHERE ELSE"))
	label := stDim.Render("a  type an address: me@192.168.1.20")
	if s.field == fieldAddr {
		label = "> " + s.addr.view(38)
	}
	out = append(out, label)
	hits = append(hits, lineHit{len(out) - 1, 0, -1, func() tea.Cmd { s.field = fieldAddr; return nil }})
	return out, hits
}

func (m *Model) pickMachine(i int) func() tea.Cmd {
	return func() tea.Cmd {
		s := &m.mach
		if s.field == fieldSecret || s.field == fieldUser || s.field == fieldAddr {
			s.field = fieldNone
			s.secret = lineInput{}
		}
		s.cursor = i
		if r := s.selected(); r != nil && r.ask != nil {
			m.focusAsk(r)
		}
		return nil
	}
}

// machineDetail is the right pane.
func (m *Model) machineDetail(w int) ([]string, []lineHit) {
	s := &m.mach
	r := s.selected()
	if r == nil {
		return nil, nil
	}
	var out []string
	var hits []lineHit
	text := func(st func(...string) string, line string) {
		for _, l := range strings.Split(ansi.Wrap(line, max(10, w), " "), "\n") {
			out = append(out, st(l))
		}
	}
	dim := stDim.Render
	button := func(label string, do func() tea.Cmd) {
		out = append(out, "", stAccent.Render("[ "+label+" ]"))
		hits = append(hits, lineHit{len(out) - 1, 0, ansi.StringWidth(label) + 4, do})
	}
	commands := func() {
		text(stWarn.Render, "ssh does not answer, and smif cannot turn it on from here. Run one of these once on that machine:")
		out = append(out, "")
		for _, c := range []struct {
			key, name, cmd string
			copy           func() tea.Cmd
		}{
			{"1", "Linux", machines.CommandLinux, func() tea.Cmd { return m.copyText(machines.CommandLinux, "Copied the Linux command.") }},
			{"2", "macOS", machines.CommandMac, func() tea.Cmd { return m.copyText(machines.CommandMac, "Copied the macOS command.") }},
		} {
			out = append(out, dim(c.name)+"  "+stAccent.Render("[ "+c.key+" copy ]"))
			hits = append(hits, lineHit{len(out) - 1, 7, 7 + 10, c.copy})
			text(stOK.Render, c.cmd)
		}
		text(dim, "On a Mac this can need Full Disk Access. Or use System Settings > General > Sharing > Remote Login.")
		out = append(out, "")
	}
	switch r.kind {
	case mHere:
		text(dim, "It runs the daemon that collects the others.")
		out = append(out, "", fmt.Sprintf("%d sessions", len(m.machineSessions(r))))
	case mFound, mClosed:
		if r.source != "" {
			text(dim, "From "+r.source+".")
			out = append(out, "")
		}
		if r.kind == mClosed {
			commands()
			button("Add it when ssh opens", m.beginRow(r))
			break
		}
		text(dim, "smif will do this, and asks only when it has to:")
		out = append(out, "")
		for i, l := range []string{"Ask for the password once, if your ssh login does not work", "Detect the system", "Install smif there, in ~/.local/bin, if it is missing",
			"Add a key that can only list sessions", "On Linux, keep smif running after logout", "Add it to your list"} {
			out = append(out, dim(fmt.Sprintf("%d  ", i+1))+l)
		}
		button("Add this machine", m.beginRow(r))
	case mRun:
		if r.closed {
			commands()
			out = append(out, stLive.Render("◐ Watching. This goes on by itself when ssh answers."))
			button("Stop", func() tea.Cmd { m.stopMachine(r); return nil })
			break
		}
		for _, st := range machines.Steps {
			out = append(out, m.machineCheckLine(r, st.ID, st.Label, w))
			if r.ask == nil {
				continue
			}
			if st.ID == machines.StepLogin && !r.ask.sudo && r.checks[st.ID].state == machines.Running {
				a, h := m.loginBox(r, w, len(out))
				out, hits = append(out, a...), append(hits, h...)
			}
			if st.ID == machines.StepLinger && r.ask.sudo {
				a, h := m.sudoBox(r, w, len(out))
				out, hits = append(out, a...), append(hits, h...)
			}
		}
		if r.ask == nil {
			out = append(out, "", dim("Move around while this runs. x stops it."))
		}
	case mFailed:
		for _, st := range machines.Steps {
			if c, ok := r.checks[st.ID]; ok {
				out = append(out, m.machineCheckLine(r, st.ID, st.Label, w))
				if c.state == machines.Failed {
					break
				}
			}
		}
		out = append(out, "")
		text(stBad.Render, r.bad)
		button("Try again", m.beginRow(r))
	case mAdded:
		if r.bad != "" {
			text(stWarn.Render, "! Away: "+r.bad)
		} else {
			text(stOK.Render, "✓ Connected.")
		}
		if r.skipped {
			text(stWarn.Render, "! Linger is off, so it can drop out when nobody is logged in there.")
		}
		out = append(out, "")
		for _, v := range m.machineSessions(r) {
			out = append(out, machineSessionLine(v))
		}
		out = append(out, "", dim("Read only. To answer, ssh there: y copies the command."))
		button("Remove", func() tea.Cmd { m.removeMachine(r); return nil })
	}
	return out, hits
}

func (m *Model) beginRow(r *machineRow) func() tea.Cmd {
	return func() tea.Cmd { return m.beginMachine(r) }
}

func (m *Model) machineCheckLine(r *machineRow, id machines.Step, label string, w int) string {
	c, ok := r.checks[id]
	glyph, st := "○", stDim.Render
	if ok {
		switch c.state {
		case machines.Running:
			glyph, st = "▶", stNew.Render
		case machines.Done:
			glyph, st = "✓", stOK.Render
		case machines.Skipped:
			glyph, st = "!", stWarn.Render
		case machines.Failed:
			glyph, st = "✗", stBad.Render
		}
	}
	line := st(glyph) + " " + label
	if ok && c.detail != "" && c.state != machines.Failed {
		line += stDim.Render("  " + c.detail)
	}
	return ansi.Truncate(line, w, "…")
}

func (m *Model) loginBox(r *machineRow, w, at int) ([]string, []lineHit) {
	s := &m.mach
	out := []string{"", stDim.Render("Used once for this setup. smif does not store it.")}
	if r.problem != "" {
		out = append(out, stBad.Render("✗ "+r.problem))
	}
	user := s.user.Value()
	if s.field == fieldUser {
		user = s.user.view(max(10, w-12))
	}
	out = append(out, machineField2("User", user, s.field == fieldUser))
	userAt := at + len(out) - 1
	out = append(out, machineField2("Password", maskField(&s.secret, s.field == fieldSecret), s.field == fieldSecret))
	passAt := at + len(out) - 1
	out = append(out, "", stAccent.Render("[ Log in ]"))
	loginAt := at + len(out) - 1
	hits := []lineHit{
		{userAt, 0, -1, func() tea.Cmd { s.field = fieldUser; return nil }},
		{passAt, 0, -1, func() tea.Cmd { s.field = fieldSecret; return nil }},
		{loginAt, 0, 10, m.enterMachine},
	}
	return append(out, ""), hits
}

func (m *Model) sudoBox(r *machineRow, w, at int) ([]string, []lineHit) {
	s := &m.mach
	out := []string{"", "Linux stops smif when nobody is logged in. This keeps it running."}
	if r.problem != "" {
		out = append(out, stBad.Render("✗ "+r.problem))
	}
	out = append(out, machineField2("sudo", maskField(&s.secret, true), true))
	out = append(out, "", stAccent.Render("[ Turn on ]")+"  "+stDim.Render("[ Skip ]"))
	y := at + len(out) - 1
	hits := []lineHit{
		{y, 0, 11, m.enterMachine},
		{y, 13, 21, func() tea.Cmd { m.answerAsk(r, machineAnswer{}); return nil }},
	}
	return append(out, ""), hits
}

func machineField2(label, value string, on bool) string {
	mark := "  "
	if on {
		mark = stAccent.Render("> ")
	}
	return mark + stDim.Render(fmt.Sprintf("%-9s", label)) + value
}

// maskField draws a secret: one dot per character, and the cursor.
func maskField(in *lineInput, on bool) string {
	dots := strings.Repeat("•", len(in.r))
	if on {
		return dots + stCursor.Render(" ")
	}
	return dots
}

func machineSessionLine(v api.SessionView) string {
	glyph, st := "○", stDim.Render
	switch v.Turn {
	case "waiting":
		glyph, st = "!", stWarn.Render
	case "busy", "working":
		glyph, st = "▶", stLive.Render
	}
	name := v.Title
	if name == "" {
		name = v.Repo
	}
	return st(glyph) + " " + name
}
