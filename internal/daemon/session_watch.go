package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/notify"
	"github.com/appnice/smif/internal/store"
)

// Telling the human when a session needs them while they look elsewhere
// (ADR-0014): the status line of every session names the others that wait
// or answered, and an OS notification comes when a session starts to wait
// in its tab or answers after a long turn.

// answerFresh is how long an answer counts as new for the status line
// when the UI has never marked that session seen.
const answerFresh = 15 * time.Minute

// notifyMinWork is how long a turn must have worked before its answer is
// worth a notification (a quick answer is read where it was asked).
const notifyMinWork = 30 * time.Second

// sessionWatchEvery is how often the daemon looks for state changes.
const sessionWatchEvery = 2 * time.Second

// readSeen is the UI's seen file: the last answer the human saw, per
// session (internal/ui/answer.go writes it).
func (d *Daemon) readSeen() map[string]time.Time {
	out := map[string]time.Time{}
	if b, err := os.ReadFile(filepath.Join(d.dataDir, "ui-seen.json")); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// unseenAnswer reports an idle session's answer the human has not seen.
func unseenAnswer(v api.SessionView, seen map[string]time.Time, now time.Time) bool {
	if v.Turn != "idle" || v.LastAnswer == "" || v.LastAnswerAt.IsZero() {
		return false
	}
	if t, ok := seen[v.SessionID]; ok {
		return v.LastAnswerAt.After(t)
	}
	return now.Sub(v.LastAnswerAt) < answerFresh
}

// sessionAttention names the live sessions other than self that wait in
// their tab, and those with an unseen answer, each sorted by name.
func (d *Daemon) sessionAttention(self string) (waiting, answered []string) {
	ps, err := d.fullSessions()
	if err != nil {
		return nil, nil
	}
	cfg, seen, now := d.Config(), d.readSeen(), time.Now()
	for _, p := range ps {
		if p.SessionID == self {
			continue
		}
		v := d.sessionView(cfg, p)
		switch {
		case v.Turn == "waiting":
			waiting = append(waiting, viewName(v))
		case unseenAnswer(v, seen, now):
			answered = append(answered, viewName(v))
		}
	}
	sort.Strings(waiting)
	sort.Strings(answered)
	return waiting, answered
}

// viewName is what the human calls a session: its name, its agent, its
// folder.
func viewName(v api.SessionView) string {
	switch {
	case v.Title != "":
		return v.Title
	case v.Agent != "":
		return v.Agent
	}
	return filepath.Base(v.Repo)
}

// watchedSession is what the watcher saw of a session at its last look.
type watchedSession struct {
	turn      string
	answerAt  time.Time
	since     time.Time // when the running prompt started
	waitSince time.Time // when it started to wait in its tab
	reminded  int       // reminders sent for this wait
	silent    bool      // the silence of this turn was told
	long      bool      // the long turn was told
}

// maxReminders caps the reminders for one wait.
const maxReminders = 3

// sessionWatchLoop notifies on session state changes until the daemon
// stops.
func (d *Daemon) sessionWatchLoop() {
	t := time.NewTicker(sessionWatchEvery)
	defer t.Stop()
	seen := map[string]watchedSession{}
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			seen = d.watchSessions(seen, time.Now())
		}
	}
}

// watchSessions compares the sessions with the last look and sends the
// notifications the config asks for: a session starts to wait or answers
// after a long turn (daemon.session_notify, ADR-0014), and the watchdogs
// (ADR-0015): it still waits, it is silent, its turn is long.
func (d *Daemon) watchSessions(prev map[string]watchedSession, now time.Time) map[string]watchedSession {
	cfg := d.Config()
	want := cfg.Daemon.SessionNotify
	remind, _ := config.WatchdogEvery(cfg.Daemon.RemindWaiting)
	silentAfter, _ := config.WatchdogEvery(cfg.Daemon.AlertSilent)
	longAfter, _ := config.WatchdogEvery(cfg.Daemon.AlertLongTurn)
	ps, err := d.fullSessions()
	if err != nil {
		return prev
	}
	next := map[string]watchedSession{}
	var open []openSession
	working := false
	for _, p := range ps {
		v := d.sessionView(cfg, p)
		working = working || sessionWorks(v, now)
		if p.Harness == harness.ClaudeName && p.Level == "" && p.AttachID == "" && p.PID > 0 {
			open = append(open, openSession{SessionID: v.SessionID, PID: p.PID, Dir: nonEmpty(p.Cwd, p.Repo), Name: viewName(v), Busy: v.Turn == "busy"})
		}
		old, known := prev[v.SessionID]
		w := old
		w.turn, w.answerAt = v.Turn, v.LastAnswerAt
		var step string
		if a := v.Activity; a != nil {
			if !a.PromptAt.Equal(old.since) {
				w.since, w.silent, w.long = a.PromptAt, false, false // a new prompt: a new turn
			}
			if a.Now != nil {
				step = harness.StepText(a.Now.Tool, a.Now.Target)
			}
		}
		if v.Turn == "waiting" && old.turn != "waiting" {
			w.waitSince, w.reminded = now, 0
		}
		next[v.SessionID] = w
		if !known {
			continue // a session seen for the first time is not news
		}
		name := viewName(v)
		switch {
		case v.Turn == "waiting" && old.turn != "waiting":
			if strings.Contains(want, "waiting") {
				d.notifySession("session_waiting", name, v, nonEmpty(step, waitText(v)))
			}
		case v.Turn == "waiting" && remind > 0 && w.reminded < maxReminders && now.Sub(w.waitSince) >= remind*time.Duration(w.reminded+1):
			w.reminded++
			next[v.SessionID] = w
			d.notifySession("session_reminder", name, v, "still waits ("+shortDur(now.Sub(w.waitSince))+"): "+nonEmpty(step, waitText(v)))
		case v.Turn == "idle" && old.turn == "busy" && v.LastAnswerAt.After(old.answerAt) &&
			!old.since.IsZero() && v.LastAnswerAt.Sub(old.since) >= notifyMinWork && strings.Contains(want, "answered"):
			d.notifySession("session_answered", name, v, firstLine(v.LastAnswer))
		case v.Turn == "busy":
			quiet := time.Duration(0)
			if a := v.Activity; a != nil && !a.WroteAt.IsZero() {
				quiet = now.Sub(a.WroteAt)
			}
			switch {
			case silentAfter > 0 && quiet >= silentAfter && !w.silent:
				w.silent = true
				next[v.SessionID] = w
				d.notifySession("session_silent", name, v, "nothing written for "+shortDur(quiet)+": "+nonEmpty(step, "thinking"))
			case silentAfter > 0 && quiet < silentAfter:
				w.silent = false // it writes again: a later silence is news
				next[v.SessionID] = w
			}
			if longAfter > 0 && !w.since.IsZero() && now.Sub(w.since) >= longAfter && !w.long {
				w.long = true
				next[v.SessionID] = w
				prompt := ""
				if v.Activity != nil {
					prompt = firstLine(v.Activity.Prompt)
				}
				d.notifySession("session_long", name, v, "works "+shortDur(now.Sub(w.since))+" on: "+nonEmpty(prompt, "your prompt"))
			}
		}
	}
	d.trackOpen(open, now)
	d.awake.set(cfg.Daemon.KeepAwake && working)
	for _, v := range d.remoteRows(now) {
		old, known := prev[v.SessionID]
		next[v.SessionID] = watchedSession{turn: v.Turn}
		if known && v.Turn == "waiting" && old.turn != "waiting" && strings.Contains(want, "waiting") {
			d.notifySession("session_waiting", viewName(v), v, "it waits for you on "+v.HostLabel())
		}
	}
	return next
}

// waitText says where a waiting session waits. A background session has
// no tab.
func waitText(v api.SessionView) string {
	if v.Background {
		return "it waits for you: claude attach opens it"
	}
	return "it waits for you in its tab"
}

// shortDur is a duration for a notification: 40s, 12m, 1h05m.
func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// notifySession raises one notification about a session, on the OS and on
// the phone (ADR-0021). The audit
// keeps only the kind and the session, never the text.
func (d *Daemon) notifySession(kind, name string, v api.SessionView, text string) {
	ev := notify.Event{Kind: kind, Agent: name, Summary: notify.Clip(text, 280), At: time.Now()}
	ping := ev
	ping.Agent = nonEmpty(v.Agent, filepath.Base(v.Repo)) // a title can repeat a prompt
	d.pinger.Send(d.Config().Daemon.NotifyURL, ping)
	if err := d.notifier.Notify(ev); err != nil {
		d.log.Warn("notification failed", "error", err.Error(), "session", v.SessionID)
	}
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("notify."+kind, v.Agent, "", "", "", map[string]any{"session_id": v.SessionID})
	})
}

// firstLine is the first line of an answer with text, without Markdown
// marks at its start.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#-*> ")); l != "" && !strings.HasPrefix(l, "```") {
			return strings.ReplaceAll(l, "**", "")
		}
	}
	return ""
}
