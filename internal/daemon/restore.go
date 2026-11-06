package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// History and restart (ADR-0021). session.history and session.search read
// past Claude Code sessions. session.resume opens one in a new tab. The
// restore list holds the sessions that went away with no human choice:
// those in open-sessions.json whose process is gone when the daemon starts,
// and two or more that vanish within 10 s. All of this is human only, and
// the daemon builds argv from the config and a checked id (R-ISO-3).

const (
	openFile     = "open-sessions.json"
	massWindow   = 10 * time.Second // sessions that vanish this close are a crash
	closedKept   = 24 * time.Hour   // how long the list keeps a session
	reopenMax    = 20               // the most sessions one A opens
	reopenGap    = 500 * time.Millisecond
	searchHits   = 50 // session.search stops at this many hits
	searchWithin = 10 * time.Second
	maxQueryLen  = 200
)

// openSession is one live interactive session in open-sessions.json.
type openSession struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Dir       string `json:"dir"`
	Name      string `json:"name,omitempty"`
	Busy      bool   `json:"busy,omitempty"`
}

// openState is the content of open-sessions.json. The restore list is in
// the file too: a second restart must not lose it.
type openState struct {
	Open   []openSession       `json:"open"`
	Closed []api.ClosedSession `json:"closed,omitempty"`
}

// vanishedSession is a session that left in the last massWindow.
type vanishedSession struct {
	openSession
	at     time.Time
	listed bool // it joined the list once: a reopen must not bring it back
}

// restoreState is what the daemon keeps between two looks.
type restoreState struct {
	mu        sync.Mutex
	open      map[string]openSession
	vanished  []vanishedSession
	closed    []api.ClosedSession // newest first
	written   []byte              // the file's last content
	reopening bool
	why       string

	searchWithin time.Duration // a test's limit (0 = searchWithin)
}

// closeEntry puts a session on the list, once. A session with no transcript
// has nothing to resume. The caller holds r.mu.
func (d *Daemon) closeEntry(s openSession, at time.Time) {
	r := &d.restore
	for _, c := range r.closed {
		if c.SessionID == s.SessionID {
			return
		}
	}
	if st, ok := d.harnesses.Get(harness.ClaudeName).(harness.Stamper); ok && st.Stamp(s.SessionID) == "" {
		return
	}
	r.closed = append(r.closed, api.ClosedSession{SessionID: s.SessionID, Dir: s.Dir, Name: s.Name, ClosedAt: at, DiedWorking: s.Busy})
	sort.SliceStable(r.closed, func(i, j int) bool { return r.closed[i].ClosedAt.After(r.closed[j].ClosedAt) })
}

// keepClosed drops the sessions that are live again or old. The caller
// holds r.mu.
func (r *restoreState) keepClosed(live map[string]openSession, now time.Time) {
	kept := r.closed[:0]
	for _, c := range r.closed {
		if _, ok := live[c.SessionID]; !ok && now.Sub(c.ClosedAt) < closedKept {
			kept = append(kept, c)
		}
	}
	r.closed = kept
}

// saveOpen writes open-sessions.json when it changed. The caller holds
// r.mu.
func (d *Daemon) saveOpen() {
	r := &d.restore
	st := openState{Open: make([]openSession, 0, len(r.open)), Closed: r.closed}
	for _, s := range r.open {
		st.Open = append(st.Open, s)
	}
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].SessionID < st.Open[j].SessionID })
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil || string(b) == string(r.written) {
		return
	}
	path := filepath.Join(d.dataDir, openFile)
	tmp := path + ".tmp"
	err = os.WriteFile(tmp, b, 0o600)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		d.log.Warn("cannot write the open sessions", "error", err.Error())
		return
	}
	r.written = b
}

// loadOpen runs at the daemon's start: the sessions in the file whose
// process is gone join the restore list.
func (d *Daemon) loadOpen(now time.Time) {
	b, err := os.ReadFile(filepath.Join(d.dataDir, openFile))
	if err != nil {
		return
	}
	var st openState
	if err := json.Unmarshal(b, &st); err != nil {
		d.log.Warn("cannot read the open sessions", "error", err.Error())
		return
	}
	live := map[string]openSession{}
	if ps, err := d.fullSessions(); err == nil {
		for _, p := range ps {
			live[p.SessionID] = openSession{}
		}
	}
	r := &d.restore
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open, r.closed, r.written = map[string]openSession{}, st.Closed, b
	for _, s := range st.Open {
		if _, ok := live[s.SessionID]; ok || harness.ProcessAlive(s.PID) {
			r.open[s.SessionID] = s
			continue
		}
		d.closeEntry(s, now)
	}
	r.keepClosed(live, now)
	d.saveOpen()
}

// trackOpen takes the live sessions of one look. A session whose process
// still runs did not vanish: /clear gave it a new id. Two or more that
// vanish within massWindow join the restore list; one alone does not.
func (d *Daemon) trackOpen(live []openSession, now time.Time) {
	next := make(map[string]openSession, len(live))
	for _, s := range live {
		next[s.SessionID] = s
	}
	r := &d.restore
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.open {
		if _, ok := next[id]; !ok && !harness.ProcessAlive(s.PID) {
			r.vanished = append(r.vanished, vanishedSession{openSession: s, at: now})
		}
	}
	kept := r.vanished[:0]
	for _, v := range r.vanished {
		if _, back := next[v.SessionID]; !back && now.Sub(v.at) <= massWindow {
			kept = append(kept, v)
		}
	}
	r.vanished = kept
	if len(r.vanished) >= 2 {
		for i := range r.vanished {
			if v := &r.vanished[i]; !v.listed {
				v.listed = true
				d.closeEntry(v.openSession, v.at)
			}
		}
	}
	r.keepClosed(next, now)
	r.open = next
	d.saveOpen()
}

// closedList is session.closed.
func (d *Daemon) closedList() api.ClosedList {
	r := &d.restore
	r.mu.Lock()
	defer r.mu.Unlock()
	return api.ClosedList{Sessions: append([]api.ClosedSession{}, r.closed...), Reopening: r.reopening, Why: r.why}
}

// forgetClosed takes a session off the restore list.
func (d *Daemon) forgetClosed(id string) {
	r := &d.restore
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.closed {
		if c.SessionID == id {
			r.closed = append(r.closed[:i], r.closed[i+1:]...)
			d.saveOpen()
			return
		}
	}
}

// historian is the provider of past sessions: Claude Code only.
func (d *Daemon) historian() (harness.Historian, error) {
	h, ok := d.harnesses.Get(harness.ClaudeName).(harness.Historian)
	if !ok {
		return nil, api.Errf(api.CodeUnavailable, "no provider keeps a history")
	}
	return h, nil
}

// liveClaude is the Claude Code sessions with a running process.
func (d *Daemon) liveClaude() map[string]bool {
	live := map[string]bool{}
	if p := d.harnesses.Get(harness.ClaudeName); p != nil {
		for _, s := range p.Discover() {
			if s.AttachID == "" {
				live[s.SessionID] = true
			}
		}
	}
	return live
}

func historyView(r harness.HistoryRow, live map[string]bool) api.HistoryRow {
	return api.HistoryRow{SessionID: r.SessionID, Dir: r.Dir, Name: r.Name, At: r.At, Open: live[r.SessionID]}
}

// sessionHistory is session.history: the past sessions, newest first.
func (d *Daemon) sessionHistory() ([]api.HistoryRow, error) {
	h, err := d.historian()
	if err != nil {
		return nil, err
	}
	rows, live := h.History(), d.liveClaude()
	out := make([]api.HistoryRow, len(rows))
	for i, r := range rows {
		out[i] = historyView(r, live)
	}
	return out, nil
}

// sessionSearch is session.search. It stops at searchHits hits or after
// searchWithin, and says which.
func (d *Daemon) sessionSearch(ctx context.Context, p api.SessionSearchParams) (*api.SessionSearchResult, error) {
	q := strings.TrimSpace(p.Query)
	if q == "" || utf8.RuneCountInString(q) > maxQueryLen {
		return nil, api.Errf(api.CodeParams, "a search is 1 to %d characters", maxQueryLen)
	}
	h, err := d.historian()
	if err != nil {
		return nil, err
	}
	within := searchWithin
	if d.restore.searchWithin > 0 {
		within = d.restore.searchWithin
	}
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	found := h.Search(ctx, q, searchHits)
	res := &api.SessionSearchResult{Hits: make([]api.SearchHit, len(found))}
	live := d.liveClaude()
	for i, f := range found {
		res.Hits[i] = api.SearchHit{HistoryRow: historyView(f.HistoryRow, live), Role: f.Role, Text: f.Text}
	}
	switch {
	case len(found) >= searchHits:
		res.Stopped = "hits"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Stopped = "time"
	}
	return res, nil
}

// resumeSession opens `claude --resume <id>` in the session's folder. The
// id and the folder come from the index, and the program from the config.
// A second `claude --resume` of a running session would write the same
// transcript from two processes, so a live session is refused.
func (d *Daemon) resumeSession(ctx context.Context, id, term string) error {
	h, err := d.historian()
	if err != nil {
		return err
	}
	var row *harness.HistoryRow
	rows := h.History()
	for i := range rows {
		if rows[i].SessionID == id {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return api.Errf(api.CodeNotFound, "no session %s in the history", api.ShortSession(id))
	}
	if d.liveClaude()[id] {
		return api.Errf(api.CodeRejected, "the session is open: o shows its tab")
	}
	if st, err := os.Stat(row.Dir); err != nil || !st.IsDir() {
		return api.Errf(api.CodeRejected, "the session's folder %s is gone", row.Dir)
	}
	argv := []string{d.Config().Defaults.ClaudeCommand, "--resume", row.SessionID}
	if err := d.terminal.Open(ctx, term, row.Dir, api.ShortSession(row.SessionID), argv); err != nil {
		return api.Errf(api.CodeUnavailable, "%v", err)
	}
	d.forgetClosed(row.SessionID)
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("session.resume", agentForRepo(d.Config(), gitRoot(row.Dir)), "", "", "", map[string]any{"session_id": row.SessionID, "term": term})
	})
	return nil
}

// sessionReopen is session.reopen: it takes the first reopenMax sessions of
// the restore list and opens them in the background, reopenGap apart. The
// rest stay on the list for the next call.
func (d *Daemon) sessionReopen(p api.SessionReopenParams) (*api.SessionReopenResult, error) {
	if err := checkOpenTerm(p.Term); err != nil {
		return nil, err
	}
	r := &d.restore
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reopening {
		return nil, api.Errf(api.CodeRejected, "sessions are reopening now")
	}
	if len(r.closed) == 0 {
		return nil, api.Errf(api.CodeRejected, "no closed sessions to reopen")
	}
	var ids []string
	for _, c := range r.closed[:min(reopenMax, len(r.closed))] {
		ids = append(ids, c.SessionID)
	}
	r.reopening, r.why = true, ""
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.reopen(ids, p.Term)
	}()
	return &api.SessionReopenResult{Opening: len(ids), Left: len(r.closed) - len(ids)}, nil
}

// reopen works through ids. A session that cannot come back leaves the
// list. A terminal that fails ends the run, and the rest stay.
func (d *Daemon) reopen(ids []string, term string) {
	r := &d.restore
	defer func() {
		r.mu.Lock()
		r.reopening = false
		r.mu.Unlock()
	}()
	for i, id := range ids {
		if i > 0 {
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(reopenGap):
			}
		}
		err := d.resumeSession(d.ctx, id, term)
		if err == nil {
			continue
		}
		d.log.Warn("session not reopened", "session", api.ShortSession(id), "error", err.Error())
		var e *api.Error
		r.mu.Lock()
		if r.why = err.Error(); errors.As(err, &e) {
			r.why = e.Message
		}
		r.mu.Unlock()
		if api.IsCode(err, api.CodeUnavailable) {
			return
		}
		d.forgetClosed(id)
	}
}
