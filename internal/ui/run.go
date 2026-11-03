package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// The tabs of the run screen.
const (
	tabRecord = iota
	tabTranscript
	tabDiff
	numTabs
)

var tabNames = [numTabs]string{"RECORD", "TRANSCRIPT", "DIFF"}

// runScreen is the detail screen of one run: the record, the transcript
// tail and the diff of its worker branch.
type runScreen struct {
	id  string
	run *api.RunView // nil until run.get answers
	err error        // last run.get error

	tab    int
	scroll [numTabs]int
	follow bool // the transcript sticks to its end

	tr        *TranscriptTail
	trErr     error
	trLoading bool
	trCache   []string
	trCacheW  int

	diff        *DiffView
	diffErr     error
	diffLoading bool
	diffLoaded  bool

	loading   bool
	applyNote string // result of the last apply on this screen
	applyBad  bool
}

type runMsg struct {
	id  string
	r   *api.RunView
	err error
}

type transcriptMsg struct {
	id  string
	t   *TranscriptTail
	err error
}

type diffMsg struct {
	id  string
	d   *DiffView
	err error
}

type applyMsg struct {
	id  string
	res *ApplyResult
	err error
}

// RunOpen reports the id of the open run screen, or "".
func (m *Model) RunOpen() string {
	if m.run == nil {
		return ""
	}
	return m.run.id
}

// RunTab reports the open tab of the run screen: RECORD, TRANSCRIPT, DIFF.
func (m *Model) RunTab() string {
	if m.run == nil {
		return ""
	}
	return tabNames[m.run.tab]
}

// openRun shows the run screen for id on top of the current screen.
func (m *Model) openRun(id string, known *api.RunView) tea.Cmd {
	if m.insp == nil {
		m.setStatus("This UI has no daemon connection to read runs.", true)
		return nil
	}
	m.run = &runScreen{id: id, run: known, follow: true}
	return m.loadRun()
}

// loadRun reloads the run record. When it answers, onRun loads the
// transcript and the diff as needed.
func (m *Model) loadRun() tea.Cmd {
	rs := m.run
	if m.insp == nil || rs == nil || rs.loading {
		return nil
	}
	rs.loading = true
	insp, id := m.insp, rs.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := insp.Run(ctx, id)
		return runMsg{id: id, r: r, err: err}
	}
}

func (m *Model) onRun(msg runMsg) tea.Cmd {
	rs := m.run
	if rs == nil || rs.id != msg.id {
		return nil
	}
	rs.loading = false
	if msg.err != nil {
		rs.err = msg.err
		return nil
	}
	ended := rs.run != nil && rs.run.EndedAt == nil && msg.r.EndedAt != nil
	rs.run, rs.err = msg.r, nil
	var cmds []tea.Cmd
	// The transcript grows while the run is in progress; read it again.
	if rs.tr == nil || msg.r.EndedAt == nil || ended {
		cmds = append(cmds, m.loadTranscript())
	}
	if !rs.diffLoaded || ended {
		cmds = append(cmds, m.loadDiff())
	}
	return tea.Batch(cmds...)
}

func (m *Model) loadTranscript() tea.Cmd {
	rs := m.run
	if m.insp == nil || rs == nil || rs.run == nil || rs.trLoading {
		return nil
	}
	if rs.run.Transcript == "" {
		rs.trErr = fmt.Errorf("run %s has no transcript (handler %s)", Short(rs.id), rs.run.Handler)
		return nil
	}
	rs.trLoading = true
	insp, id, r := m.insp, rs.id, *rs.run
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		t, err := insp.Transcript(ctx, r)
		return transcriptMsg{id: id, t: t, err: err}
	}
}

func (m *Model) onTranscript(msg transcriptMsg) {
	rs := m.run
	if rs == nil || rs.id != msg.id {
		return
	}
	rs.trLoading = false
	if msg.err != nil {
		rs.trErr = msg.err
		return
	}
	if rs.tr == nil || !bytes.Equal(rs.tr.Data, msg.t.Data) {
		rs.trCache = nil
	}
	rs.tr, rs.trErr = msg.t, nil
}

func (m *Model) loadDiff() tea.Cmd {
	rs := m.run
	if m.insp == nil || rs == nil || rs.run == nil || rs.diffLoading {
		return nil
	}
	if rs.run.Branch == "" {
		rs.diffLoaded = true
		return nil
	}
	rs.diffLoading = true
	insp, id := m.insp, rs.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := insp.Diff(ctx, id)
		return diffMsg{id: id, d: d, err: err}
	}
}

func (m *Model) onDiff(msg diffMsg) {
	rs := m.run
	if rs == nil || rs.id != msg.id {
		return
	}
	rs.diffLoading, rs.diffLoaded = false, true
	if msg.err != nil {
		rs.diffErr = msg.err
		return
	}
	rs.diff, rs.diffErr = msg.d, nil
}

// runKey handles keys on the run screen.
func (m *Model) runKey(k string) tea.Cmd {
	rs := m.run
	sc := &rs.scroll[rs.tab]
	switch k {
	case "q":
		return tea.Quit
	case "esc", "backspace":
		m.run = nil
	case "?":
		m.openHelp()
	case "1", "2", "3":
		rs.tab = int(k[0] - '1')
	case "tab", "right", "l":
		rs.tab = (rs.tab + 1) % numTabs
	case "shift+tab", "left", "h":
		rs.tab = (rs.tab + numTabs - 1) % numTabs
	case "up", "k":
		*sc--
		rs.unfollow()
	case "down", "j":
		*sc++
	case "pgup":
		*sc -= 10
		rs.unfollow()
	case "pgdown", " ":
		*sc += 10
	case "home", "g":
		*sc = 0
		rs.unfollow()
	case "end", "G":
		*sc = 1 << 30
		if rs.tab == tabTranscript {
			rs.follow = true
		}
	case "r":
		var cmds []tea.Cmd
		if !m.fetching {
			m.fetching = true
			cmds = append(cmds, m.fetch())
		}
		rs.trCache = nil
		rs.diffLoaded = false
		cmds = append(cmds, m.loadRun())
		if rs.run != nil {
			cmds = append(cmds, m.loadTranscript(), m.loadDiff())
		}
		return tea.Batch(cmds...)
	case "a":
		return m.askApply()
	case "c":
		if rs.run == nil || rs.run.EndedAt != nil {
			m.setStatus("Cancel is possible only while the run is in progress.", true)
			return nil
		}
		r := rs.run
		m.askCancel(api.MessageView{ID: r.MessageID, To: r.Agent, Kind: "run", Intent: r.Handler, State: "running",
			Subject: "run " + Short(r.ID) + ", attempt " + itoa(r.Attempt)})
	}
	return nil
}

func (rs *runScreen) unfollow() {
	if rs.tab == tabTranscript {
		rs.follow = false
	}
}

// merged reports that the loaded diff of the run's branch is empty: the
// branch is already in the main checkout (or never changed anything).
func (rs *runScreen) merged() bool {
	return rs.diffLoaded && rs.diffErr == nil && rs.diff != nil && strings.TrimSpace(rs.diff.Diff) == ""
}

// askApply asks y/N before run.apply. Only "y" merges.
func (m *Model) askApply() tea.Cmd {
	rs := m.run
	if rs.run == nil {
		m.setStatus("Wait: the run record is still loading.", true)
		return nil
	}
	if rs.run.Branch == "" {
		m.setStatus("Apply is not possible: run "+Short(rs.id)+" has no worker branch (write_scope was not worktree).", true)
		return nil
	}
	if rs.merged() {
		m.setStatus("Nothing to apply: branch "+rs.run.Branch+" has no changes that the main checkout lacks.", false)
		return nil
	}
	repo := "the agent's main checkout"
	if rs.diff != nil && rs.diff.Repo != "" {
		repo = rs.diff.Repo
	}
	id, branch := rs.id, rs.run.Branch
	m.confirm = &confirmation{what: "apply " + Short(id), lines: []string{
		stWarn.Render(fmt.Sprintf("Apply run %s? [y/N] It merges branch %s", Short(id), branch)),
		fmt.Sprintf("into the current branch of %s (git merge --no-ff in your main checkout).", repo),
		"The daemon refuses when the checkout has uncommitted changes.",
	}, yes: func() tea.Cmd { return m.apply(id) }}
	return nil
}

func (m *Model) apply(id string) tea.Cmd {
	if m.busy != "" {
		m.setStatus("Wait: the "+m.busy+" is still in flight.", true)
		return nil
	}
	m.busy = "apply"
	m.setStatus("Sending apply for "+Short(id)+"…", false)
	insp := m.insp
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		defer cancel()
		res, err := insp.Apply(ctx, id)
		return applyMsg{id: id, res: res, err: err}
	}
}

func (m *Model) onApply(msg applyMsg) tea.Cmd {
	m.busy = ""
	var note string
	bad := msg.err != nil
	if bad {
		note = fmt.Sprintf("Apply %s refused: %s", Short(msg.id), oneLine(rpcMessage(msg.err)))
	} else {
		note = fmt.Sprintf("Applied %s to %s: HEAD %s.", msg.res.Branch, msg.res.Repo, shortSHA(msg.res.Head))
	}
	m.setStatus(note, bad)
	var cmds []tea.Cmd
	if rs := m.run; rs != nil && rs.id == msg.id {
		rs.applyNote, rs.applyBad = note, bad
		rs.diffLoaded = false
		cmds = append(cmds, m.loadDiff())
	}
	if !m.fetching {
		m.fetching = true
		cmds = append(cmds, m.fetch())
	}
	return tea.Batch(cmds...)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// runLines renders the run screen into at most avail lines.
func (m *Model) runLines(w, avail int) []string {
	rs := m.run
	now := m.now()
	r := rs.run
	var head []string
	if r == nil {
		head = []string{stFocus.Render(" RUN · " + rs.id + " ")}
		switch {
		case rs.err != nil:
			head = append(head, stBad.Render("  cannot load the run: "+oneLine(rs.err.Error())))
		default:
			head = append(head, stDim.Render("  loading…"))
		}
		return head
	}
	hd := r.Handler
	if r.Profile != "" {
		hd += "/" + r.Profile
	}
	end, took := now, "running for "
	if r.EndedAt != nil {
		end, took = *r.EndedAt, "took "
	}
	head = []string{
		stFocus.Render(" RUN · "+r.ID+" ") + "  " + runStateLabel(r.State),
		fmt.Sprintf("  agent %s · %s · attempt %d · message %s · started %s · %s%s", r.Agent, hd, r.Attempt, Short(r.MessageID),
			r.StartedAt.Local().Format("15:04:05"), took, dur(end.Sub(r.StartedAt))),
	}
	var tabs []string
	for i, n := range tabNames {
		// Brackets mark the open tab also where styles do not show.
		if i == rs.tab {
			tabs = append(tabs, stFocus.Render(fmt.Sprintf("[%d %s]", i+1, n)))
		} else {
			tabs = append(tabs, stTitle.Render(fmt.Sprintf(" %d %s ", i+1, n)))
		}
	}
	head = append(head, "  "+strings.Join(tabs, " "))
	if rs.err != nil {
		head = append(head, stBad.Render("  refresh failed, showing older data: "+oneLine(rs.err.Error())))
	}
	var body []string
	switch rs.tab {
	case tabRecord:
		body = m.recordBody(w)
	case tabTranscript:
		body = m.transcriptBody(w)
	case tabDiff:
		body = m.diffBody(w)
	}
	room := avail - len(head)
	if room < 1 {
		return head[:max(0, avail)]
	}
	sc := &rs.scroll[rs.tab]
	if rs.tab == tabTranscript && rs.follow {
		*sc = 1 << 30
	}
	*sc = max(0, min(*sc, len(body)-room))
	stop := min(len(body), *sc+room)
	if len(body) > room {
		head[0] += stDim.Render(fmt.Sprintf("  lines %d-%d of %d", *sc+1, stop, len(body)))
	}
	return append(head, body[*sc:stop]...)
}

func (m *Model) recordBody(w int) []string {
	rs := m.run
	r := rs.run
	var body []string
	wrap := func(indent, s string) {
		for _, l := range strings.Split(ansi.Wrap(s, max(20, w-len(indent)), ""), "\n") {
			body = append(body, indent+l)
		}
	}
	section := func(name string) { body = append(body, "", "  "+stHeading.Render(name)) }
	body = append(body, fmt.Sprintf("  cost %s · tokens in %d out %d", money4(r.CostUSD), r.InputTokens, r.OutputTokens))
	if r.PID != 0 && r.EndedAt == nil {
		body = append(body, fmt.Sprintf("  pid %d", r.PID))
	}
	if r.SessionID != "" {
		wrap("  ", fmt.Sprintf("session %s (open it with: claude --resume %s, from %s)", r.SessionID, r.SessionID, dash(r.Workspace)))
	} else if r.Workspace != "" {
		wrap("  ", "workspace "+r.Workspace)
	}
	if r.ArchivedSession != "" {
		body = append(body, "  rotated out session "+r.ArchivedSession)
	}
	if rs.applyNote != "" {
		st := stOK
		if rs.applyBad {
			st = stBad
		}
		body = append(body, "")
		wrap("  ", st.Render("LAST APPLY: ")+rs.applyNote)
	}
	section("BRANCH")
	if r.Branch == "" {
		body = append(body, stDim.Render("    no worker branch (write_scope was not worktree)"))
	} else {
		hint := "a applies it (asks y/N), 3 shows the diff"
		if rs.merged() {
			hint = "already in the main checkout, nothing to apply"
		}
		body = append(body, fmt.Sprintf("    %s · head %s · %s", r.Branch, dash(shortSHA(r.HeadSHA)), hint))
		for _, l := range strings.Split(strings.TrimRight(r.Diffstat, "\n"), "\n") {
			if l != "" {
				body = append(body, "    "+l)
			}
		}
	}
	if r.Error != "" {
		section("ERROR")
		wrap("    ", stBad.Render(r.Error))
	}
	section("RESULT")
	if len(r.Result) == 0 && r.EndedAt == nil {
		body = append(body, stDim.Render("    (still running; tab 2 follows the transcript)"))
	} else if len(r.Result) == 0 {
		body = append(body, stDim.Render("    (none)"))
	} else {
		var pretty bytes.Buffer
		if json.Indent(&pretty, r.Result, "", "  ") != nil {
			pretty.Reset()
			pretty.Write(r.Result)
		}
		for _, l := range strings.Split(pretty.String(), "\n") {
			wrap("    ", l)
		}
	}
	if r.Transcript != "" {
		section("TRANSCRIPT")
		wrap("    ", r.Transcript+" · 2 shows the tail")
	}
	if s := strings.TrimSpace(r.Stderr); s != "" {
		section("STDERR")
		ls := strings.Split(s, "\n")
		if len(ls) > 20 {
			body = append(body, stDim.Render(fmt.Sprintf("    … %d earlier lines", len(ls)-20)))
			ls = ls[len(ls)-20:]
		}
		for _, l := range ls {
			wrap("    ", l)
		}
	}
	return body
}

func (m *Model) transcriptBody(w int) []string {
	rs := m.run
	switch {
	case rs.tr == nil && rs.trErr != nil:
		return []string{stBad.Render("  cannot read the transcript: " + oneLine(rs.trErr.Error()))}
	case rs.tr == nil:
		return []string{stDim.Render("  loading…")}
	}
	if rs.trCache == nil || rs.trCacheW != w {
		info := fmt.Sprintf("  %s · %d bytes", rs.tr.Path, rs.tr.Size)
		if rs.tr.Cut {
			info += fmt.Sprintf(" · showing the last %d KB", len(rs.tr.Data)>>10)
		}
		rs.trCache = append([]string{stDim.Render(info), ""}, RenderTranscript(rs.tr.Data, w-2)...)
		rs.trCacheW = w
	}
	if rs.trErr != nil {
		return append([]string{stBad.Render("  refresh failed, showing older data: " + oneLine(rs.trErr.Error()))}, rs.trCache...)
	}
	return rs.trCache
}

func (m *Model) diffBody(w int) []string {
	rs := m.run
	r := rs.run
	var body []string
	if rs.applyNote != "" {
		st := stOK
		if rs.applyBad {
			st = stBad
		}
		body = append(body, "  "+st.Render("LAST APPLY: ")+oneLine(rs.applyNote), "")
	}
	switch {
	case r.Branch == "":
		return append(body, stDim.Render("  This run has no worker branch (write_scope was not worktree), so there is no diff."))
	case rs.diff == nil && rs.diffErr != nil:
		return append(body, stBad.Render("  cannot load the diff: "+oneLine(rs.diffErr.Error())))
	case rs.diff == nil:
		return append(body, stDim.Render("  loading…"))
	}
	if rs.diffErr != nil {
		body = append(body, stBad.Render("  refresh failed, showing older data: "+oneLine(rs.diffErr.Error())))
	}
	body = append(body, stTitle.Render(fmt.Sprintf("  %s against HEAD of %s", rs.diff.Branch, rs.diff.Repo)))
	if strings.TrimSpace(rs.diff.Diff) == "" {
		return append(body, stDim.Render("  (no changes against HEAD: the branch is merged or empty)"))
	}
	return append(body, RenderDiff(rs.diff.Diff, w)...)
}

func runStateLabel(s string) string {
	switch s {
	case "failed", "cancelled", "killed", "timeout":
		return stBad.Render(s)
	case "running":
		return stWarn.Render(s)
	case "done", "succeeded":
		return stOK.Render(s)
	}
	return s
}
