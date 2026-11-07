// Package hermes is the Hermes Agent provider. It reads Hermes's SQLite
// database, and a real session checked it on v0.23.0 (docs/compat.md). smif
// types into an idle session, and never forks one or installs hooks for it.
//
// Every TUI talks to one gateway, and no row links a TUI's pid to a session,
// so smif matches like the Codex provider: a TUI owns the session in its
// folder that was written since the process started.
//
// A turn holds a row in session_turn_leases until it ends, and an approval or
// a question waits inside the turn, so a lease reads busy. With no live lease
// only a last assistant message that finished with stop reads idle.
package hermes

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the driver of database/sql

	"github.com/appnice/smif/internal/harness"
)

// Name is the harness name of Hermes sessions.
const Name = "hermes"

const (
	discoverEvery = 2 * time.Second // ps runs at most this often
	maxMessages   = 200             // the newest messages of a session to read
	checkSessions = 3               // how many sessions smif doctor reads
)

// Provider reads one Hermes database.
type Provider struct {
	db    string
	procs func() []harness.Proc

	mu    sync.Mutex
	at    time.Time
	last  []harness.Session
	loads map[string]load
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a database.
func New(db string) *Provider {
	return NewWithProcs(db, harness.ProcLister(sessionProc))
}

// NewWithProcs takes the process list from procs: tests use it.
func NewWithProcs(db string, procs func() []harness.Proc) *Provider {
	return &Provider{db: db, procs: procs, loads: map[string]load{}}
}

// Info names the provider. Hermes runs as python, so that is the name the
// foreground check before typing wants.
func (p *Provider) Info() harness.Info {
	return harness.Info{Name: Name, Process: "python", Verified: true}
}

// sessionProc reports a process that can be a human's TUI.
func sessionProc(pr harness.Proc) bool {
	return pr.TTY != "" && harness.ListedProcess(pr.Argv) == Name && !slices.Contains(pr.Argv, "gateway")
}

// open opens the database read only. The caller closes it.
func (p *Provider) open() (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: p.db, RawQuery: "mode=ro&_pragma=busy_timeout(1000)&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func seconds(f float64) time.Time { return time.UnixMilli(int64(f * 1000)) }

// recent are the open local sessions written since a time.
func recent(db *sql.DB, since time.Time) []harness.Candidate {
	rows, err := db.Query(`SELECT id, COALESCE(cwd, ''), COALESCE(title, ''), started_at,
		COALESCE((SELECT MAX(timestamp) FROM messages m WHERE m.session_id = s.id), started_at)
		FROM sessions s WHERE ended_at IS NULL AND source IN ('local', 'cli')`)
	if err != nil {
		return nil // a changed table: smif doctor says so
	}
	defer rows.Close()
	var out []harness.Candidate
	for rows.Next() {
		var id, cwd, title string
		var started, wrote float64
		if rows.Scan(&id, &cwd, &title, &started, &wrote) != nil || !harness.ValidID(id) || seconds(wrote).Before(since) {
			continue
		}
		out = append(out, harness.Candidate{ID: id, Cwd: cwd, Name: title, Created: seconds(started), Wrote: seconds(wrote)})
	}
	return out
}

// match ties TUIs to sessions. See the package comment.
func (p *Provider) match(procs []harness.Proc) []harness.Session {
	if len(procs) == 0 {
		return nil
	}
	db, err := p.open()
	if err != nil {
		return nil
	}
	defer db.Close()
	oldest := procs[0].Start
	for _, pr := range procs {
		if pr.Start.Before(oldest) {
			oldest = pr.Start
		}
	}
	var out []harness.Session
	for _, o := range harness.MatchProcs(procs, recent(db, oldest), nil) {
		out = append(out, harness.Session{PID: o.Proc.PID, SessionID: o.Session.ID, Cwd: o.Session.Cwd, Name: o.Session.Name,
			StartedAt: o.Proc.Start.UnixMilli(), StatusAt: o.Session.Wrote})
	}
	return out
}

// Discover returns the sessions a live TUI owns. With no database it runs
// no ps, so a machine without Hermes pays only a stat.
func (p *Provider) Discover() []harness.Session {
	if p.db == "" {
		return nil
	}
	if _, err := os.Stat(p.db); err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.at.IsZero() || time.Since(p.at) >= discoverEvery {
		var tuis []harness.Proc
		for _, pr := range p.procs() {
			if sessionProc(pr) {
				tuis = append(tuis, pr)
			}
		}
		p.last, p.at = p.match(tuis), time.Now()
	}
	out := make([]harness.Session, 0, len(p.last))
	for _, s := range p.last {
		if harness.ProcessAlive(s.PID) {
			out = append(out, s)
		}
	}
	return out
}

// stateOf reads a load at a time. Anything else than a live lease or a
// finished answer gives "", and smif does not type then.
func stateOf(l load, now time.Time) (string, time.Time) {
	switch {
	case l.lease.After(now):
		return "busy", l.wrote
	case l.role == "assistant" && l.finish == "stop":
		return "idle", l.wrote
	}
	return "", time.Time{}
}

// State is what the session's rows say.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	l, ok := p.read(s.SessionID)
	if !ok {
		return "", time.Time{}
	}
	return stateOf(l, time.Now())
}

// read gives a session's load, again only when the database changed.
func (p *Provider) read(id string) (load, bool) {
	stamp := p.Stamp(id)
	if stamp == "" {
		return load{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.loads[id]; ok && l.stamp == stamp {
		return l, true
	}
	db, err := p.open()
	if err != nil {
		return load{}, false
	}
	defer db.Close()
	l, ok := readSession(db, id)
	if !ok {
		delete(p.loads, id)
		return load{}, false
	}
	l.stamp = stamp
	if len(p.loads) > 64 {
		p.loads = map[string]load{}
	}
	p.loads[id] = l
	return l, true
}

// Read reads a session's newest messages.
func (p *Provider) Read(sessionID string, limit int) harness.Reading {
	l, ok := p.read(sessionID)
	if !ok {
		return harness.Reading{}
	}
	turns, act := l.log.Result(limit)
	return harness.Reading{Turns: turns, Activity: act, WroteAt: l.wrote, OK: true}
}

// Stamp is the size and time of the database and its -wal file: any write
// to any session changes it. "" means no database or a bad id.
func (p *Provider) Stamp(sessionID string) string {
	if p.db == "" || !harness.ValidID(sessionID) {
		return ""
	}
	st, err := os.Stat(p.db)
	if err != nil {
		return ""
	}
	stamp := strconv.FormatInt(st.Size(), 10) + "/" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	if wal, err := os.Stat(p.db + "-wal"); err == nil {
		stamp += "/" + strconv.FormatInt(wal.Size(), 10) + "/" + strconv.FormatInt(wal.ModTime().UnixNano(), 10)
	}
	return stamp
}

// Check compares the database on this machine with what smif reads. The
// detail names counts and table names only, never a value from a row.
func (p *Provider) Check() (state, detail string) {
	if p.db == "" {
		return "none", "no Hermes database"
	}
	if _, err := os.Stat(p.db); err != nil {
		return "none", "no database at " + p.db
	}
	db, err := p.open()
	if err != nil {
		return "changed", "the database does not open"
	}
	defer db.Close()
	for table, q := range map[string]string{
		"sessions":            `SELECT id, cwd, title, source, started_at, ended_at FROM sessions LIMIT 0`,
		"messages":            `SELECT session_id, role, content, tool_calls, tool_call_id, finish_reason, timestamp, active FROM messages LIMIT 0`,
		"session_turn_leases": `SELECT conversation_id, expires_at FROM session_turn_leases LIMIT 0`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			return "changed", "table " + table + " lacks a column smif reads"
		}
		rows.Close()
	}
	rows, err := db.Query(`SELECT id FROM sessions ORDER BY started_at DESC LIMIT ?`, checkSessions)
	if err != nil {
		return "changed", "sessions cannot be read"
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return "none", "no sessions in " + p.db
	}
	marked := slices.ContainsFunc(ids, func(id string) bool {
		l, ok := readSession(db, id)
		return ok && l.marked
	})
	if !marked {
		return "changed", fmt.Sprintf("%d sessions read, no user or assistant message found in them", len(ids))
	}
	return "ok", fmt.Sprintf("%d sessions read", len(ids))
}

// load is what one read of a session gave.
type load struct {
	stamp        string
	log          *harness.TurnLog
	wrote, lease time.Time
	role, finish string // of the newest message
	marked       bool   // a user or assistant message was seen
}

// call is the part of a message's tool_calls smif uses.
type call struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// readSession reads one session's newest messages and its lease.
func readSession(db *sql.DB, id string) (load, bool) {
	var started float64
	if db.QueryRow(`SELECT started_at FROM sessions WHERE id = ?`, id).Scan(&started) != nil {
		return load{}, false
	}
	rows, err := db.Query(`SELECT role, COALESCE(content, ''), COALESCE(tool_calls, ''), COALESCE(tool_call_id, ''),
		COALESCE(finish_reason, ''), timestamp FROM messages WHERE session_id = ? AND active = 1
		ORDER BY id DESC LIMIT ?`, id, maxMessages)
	if err != nil {
		return load{}, false
	}
	defer rows.Close()
	type message struct {
		role, content, calls, callID, finish string
		at                                   float64
	}
	var ms []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.role, &m.content, &m.calls, &m.callID, &m.finish, &m.at); err != nil {
			return load{}, false
		}
		ms = append(ms, m)
	}
	if rows.Err() != nil {
		return load{}, false
	}
	l := load{log: &harness.TurnLog{}, wrote: seconds(started)}
	if len(ms) > 0 {
		l.role, l.finish, l.wrote = ms[0].role, ms[0].finish, seconds(ms[0].at)
	}
	var expires sql.NullFloat64
	if err := db.QueryRow(`SELECT expires_at FROM session_turn_leases WHERE conversation_id = ?`, id).Scan(&expires); err != nil && err != sql.ErrNoRows {
		return load{}, false
	}
	if expires.Valid {
		l.lease = seconds(expires.Float64)
	}
	for i := len(ms) - 1; i >= 0; i-- { // oldest first
		m := ms[i]
		at := seconds(m.at)
		switch m.role {
		case "user":
			l.log.Human(m.content, at)
			l.marked = true
		case "assistant":
			l.marked = true
			var calls []call
			if m.calls != "" && json.Unmarshal([]byte(m.calls), &calls) == nil {
				for _, c := range calls {
					var args map[string]any
					_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
					l.log.Call(harness.Step{ID: c.ID, Tool: c.Function.Name, At: at,
						Target: harness.Target(args, "command", "path", "file_path", "pattern", "query", "url")})
				}
			}
			if m.content != "" {
				l.log.Answer(m.content, at)
			}
		case "tool":
			l.log.Done(m.callID)
		}
	}
	return l, true
}
