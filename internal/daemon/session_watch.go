package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/notify"
	"github.com/appnice/mailroom/internal/presence"
	"github.com/appnice/mailroom/internal/store"
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
	ps, err := d.liveSessions()
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
	turn     string
	answerAt time.Time
	since    time.Time // when the running prompt started
}

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
			seen = d.watchSessions(seen)
		}
	}
}

// watchSessions compares the sessions with the last look and sends the
// notifications the config asks for (daemon.session_notify).
func (d *Daemon) watchSessions(prev map[string]watchedSession) map[string]watchedSession {
	cfg := d.Config()
	want := cfg.Daemon.SessionNotify
	if want == "none" {
		return prev
	}
	ps, err := d.liveSessions()
	if err != nil {
		return prev
	}
	next := map[string]watchedSession{}
	for _, p := range ps {
		v := d.sessionView(cfg, p)
		w := watchedSession{turn: v.Turn, answerAt: v.LastAnswerAt}
		if a := v.Activity; a != nil {
			w.since = a.PromptAt
		}
		old, known := prev[v.SessionID]
		next[v.SessionID] = w
		if !known {
			continue // a session seen for the first time is not news
		}
		name := viewName(v)
		switch {
		case v.Turn == "waiting" && old.turn != "waiting" && strings.Contains(want, "waiting"):
			what := "it waits for you in its tab"
			if a := v.Activity; a != nil && a.Now != nil {
				what = presence.StepText(a.Now.Tool, a.Now.Target)
			}
			d.notifySession("session_waiting", name, v, what)
		case v.Turn == "idle" && old.turn == "busy" && v.LastAnswerAt.After(old.answerAt) &&
			!old.since.IsZero() && v.LastAnswerAt.Sub(old.since) >= notifyMinWork && strings.Contains(want, "answered"):
			d.notifySession("session_answered", name, v, firstLine(v.LastAnswer))
		}
	}
	return next
}

// notifySession raises one OS notification about a session. The audit
// keeps only the kind and the session, never the text.
func (d *Daemon) notifySession(kind, name string, v api.SessionView, text string) {
	ev := notify.Event{Kind: kind, Agent: name, Summary: notify.Clip(text, 280), At: time.Now()}
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
