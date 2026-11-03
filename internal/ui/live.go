package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// What a session does now, for the list, the preview and the chat: the
// state comes from Claude Code's session file, the step from the end of the
// transcript (session.list carries both). The list order never changes with
// the state (sessions stay by folder and name); the state column, the
// top-line counts and tab (the next one that needs you) carry it.

// sessKind is a session's state as the home screen counts it.
type sessKind int

const (
	kQuiet   sessKind = iota // idle, its last answer read
	kNew                     // idle with an answer the human has not seen
	kWorking                 // a prompt is running
	kWaiting                 // stopped on a permission or a question in its tab
)

// silentAfter is how long a working session may write nothing to its
// transcript before the list says so (a long command, or stuck).
const silentAfter = 5 * time.Minute

func (m *Model) sessionKind(sv *api.SessionView) sessKind {
	switch sv.Turn {
	case "waiting":
		return kWaiting
	case "busy":
		return kWorking
	}
	if m.newAnswer(sv) {
		return kNew
	}
	return kQuiet
}

// sessionCounts counts the live sessions per state.
func (m *Model) sessionCounts() (counts [4]int) {
	if m.snap == nil {
		return counts
	}
	for i := range m.snap.Sessions {
		counts[m.sessionKind(&m.snap.Sessions[i])]++
	}
	return counts
}

// elapsed is a running time to the second: 40s, 2m14s, 1h05m.
func elapsed(d time.Duration) string {
	d = max(0, d)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// workingSince is when the running prompt started: the prompt in the
// transcript, else when the session turned busy.
func workingSince(sv *api.SessionView, now time.Time) time.Time {
	if a := sv.Activity; a != nil && !a.PromptAt.IsZero() && now.Sub(a.PromptAt) < 24*time.Hour {
		return a.PromptAt
	}
	return sv.TurnAt
}

// silentFor is how long a working session has written nothing (0 when it
// writes, or is not working).
func silentFor(sv *api.SessionView, now time.Time) time.Duration {
	if sv.Turn != "busy" || sv.Activity == nil || sv.Activity.WroteAt.IsZero() {
		return 0
	}
	if d := now.Sub(sv.Activity.WroteAt); d >= silentAfter {
		return d
	}
	return 0
}

// nowStep is the call without a result: what runs, or what it asks for.
func nowStep(sv *api.SessionView) *api.SessionStep {
	if sv.Activity == nil {
		return nil
	}
	return sv.Activity.Now
}

// stepLabel is a step as "Bash  git push origin main", the tool in st.
// A question or a plan reads as what it asks.
func stepLabel(s *api.SessionStep, st interface{ Render(...string) string }) string {
	switch s.Tool {
	case "AskUserQuestion":
		return st.Render("asks") + "  " + s.Target
	case "ExitPlanMode":
		return st.Render("plan") + "  ready for you to approve"
	}
	if s.Target == "" {
		return st.Render(s.Tool)
	}
	return st.Render(s.Tool) + "  " + s.Target
}

// waitReason says what a waiting session stopped on.
func waitReason(sv *api.SessionView) string {
	s := nowStep(sv)
	switch {
	case s == nil:
		return "it waits for you in its tab"
	case s.Tool == "AskUserQuestion":
		return "it asks you a question in its tab"
	case s.Tool == "ExitPlanMode":
		return "it waits for you to approve its plan in its tab"
	}
	return "it stopped on a permission prompt in its tab"
}

// sessionStateCell is the state column of a session row: "! 4m",
// "▶ 2m14s", "● new", "○ idle". The glyph carries the state without colour.
func (m *Model) sessionStateCell(sv *api.SessionView, now time.Time) string {
	switch m.sessionKind(sv) {
	case kWaiting:
		return stWarn.Render("! " + ago(now.Sub(sv.TurnAt)))
	case kWorking:
		return stLive.Render("▶ " + elapsed(now.Sub(workingSince(sv, now))))
	case kNew:
		return stNew.Render("● new")
	}
	if sv.Turn == "idle" {
		return stDim.Render("○ idle")
	}
	return stDim.Render("· open")
}

// sessionLabel is the preview's state: "! WAITING 4m", "▶ WORKING 2m14s",
// "● NEW ANSWER", "○ IDLE".
func (m *Model) sessionLabel(sv *api.SessionView, now time.Time) string {
	switch m.sessionKind(sv) {
	case kWaiting:
		return stWarn.Render("! WAITING " + ago(now.Sub(sv.TurnAt)))
	case kWorking:
		return stLive.Render("▶ WORKING " + elapsed(now.Sub(workingSince(sv, now))))
	case kNew:
		return stNew.Render("● NEW ANSWER")
	}
	if sv.Turn == "idle" {
		return stOK.Render("○ IDLE")
	}
	return stDim.Render("· OPEN")
}

// liveLine is the one-line state of a working or waiting session, for the
// bottom of the chat ("" when it is idle).
func (m *Model) liveLine(sv *api.SessionView, w int, now time.Time) string {
	var parts []string
	switch m.sessionKind(sv) {
	case kWaiting:
		parts = append(parts, stWarn.Render("! waits for you "+ago(now.Sub(sv.TurnAt))))
		if s := nowStep(sv); s != nil {
			parts = append(parts, stepLabel(s, stWarn))
		}
		parts = append(parts, stDim.Render("ctrl+o its tab"))
	case kWorking:
		parts = append(parts, stLive.Render("▶ working "+elapsed(now.Sub(workingSince(sv, now)))))
		if s := nowStep(sv); s != nil {
			parts = append(parts, stDim.Render("now ")+stepLabel(s, stLive))
		} else {
			parts = append(parts, stDim.Render("thinking"))
		}
		if a := sv.Activity; a != nil && a.Tools > 0 {
			parts = append(parts, stDim.Render(fmt.Sprintf("%d tools", a.Tools)))
		}
		if d := silentFor(sv, now); d > 0 {
			parts = append(parts, stWarn.Render("silent "+ago(d)))
		}
	default:
		return ""
	}
	return ansi.Truncate(strings.Join(parts, stDim.Render(" · ")), w, "…")
}

// attention reports a row that needs the human: a session waiting in its
// tab or with a new answer, a message that asks, a dead letter.
func (m *Model) attention(r homeRow) bool {
	if r.sess != nil {
		k := m.sessionKind(r.sess)
		return k == kWaiting || k == kNew
	}
	return r.group == grpNeeds || r.group == grpDead
}

// homeNextAttention moves to the next row (step -1: the previous) that
// needs the human, round the list. sessionsOnly keeps to sessions (the
// chat). It reports false when nothing needs the human.
func (m *Model) homeNextAttention(step int, sessionsOnly bool) bool {
	rows := m.homeRows()
	if len(rows) == 0 {
		return false
	}
	cur := m.homeIndex(rows)
	for k := 1; k <= len(rows); k++ {
		i := ((cur+step*k)%len(rows) + len(rows)) % len(rows)
		r := rows[i]
		if (sessionsOnly && r.sess == nil) || !m.attention(r) || i == cur {
			continue
		}
		m.home.sel = r.msg.ID
		if r.sess != nil {
			m.markAnswerRead(r.sess)
		}
		return true
	}
	return false
}

// othersNeedYou counts the sessions other than sv that need the human.
func (m *Model) othersNeedYou(sv *api.SessionView) int {
	n := 0
	if m.snap != nil {
		for i := range m.snap.Sessions {
			o := &m.snap.Sessions[i]
			if k := m.sessionKind(o); o.SessionID != sv.SessionID && (k == kWaiting || k == kNew) {
				n++
			}
		}
	}
	return n
}
