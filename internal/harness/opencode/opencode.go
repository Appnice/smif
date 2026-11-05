// Package opencode is the OpenCode V2 provider (ADR-0019). It is built from
// a reading of OpenCode's source at commit 1e4ad075. No real session checked
// it, so it is unverified: smif shows these sessions and never types into
// them, forks them or installs hooks for them. OpenCode V1 has no session_v2
// table and stays at the herdr and Listed levels.
//
// One server serves every TUI, and no file or route links a TUI's pid to a
// session, so smif matches like the Codex provider: an opencode process on
// a terminal owns the session in its folder that was updated since the
// process started. A TUI with no such session stays at the Listed level.
//
// The database is opened with mode=ro, never immutable=1: immutable would
// skip a live -wal file. service.json holds the server's password, so smif
// never opens that file and uses no HTTP route. `ps` tells that the server
// lives. A pending approval lives only in the server's memory, so an open
// tool call gives no state.
package opencode

import (
	"database/sql"
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

// Name is the harness name of OpenCode sessions.
const Name = "opencode"

const (
	discoverEvery = 2 * time.Second // ps runs at most this often
	maxMessages   = 200             // the newest messages of a session to read
	checkSessions = 3               // how many sessions smif doctor reads
)

// Provider reads one OpenCode database.
type Provider struct {
	db    string
	procs func() []harness.Proc

	mu     sync.Mutex
	at     time.Time
	last   []harness.Session
	server bool // the last ps showed the shared server
	loads  map[string]load
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a database.
func New(db string) *Provider {
	return NewWithProcs(db, harness.ProcLister(func(pr harness.Proc) bool {
		return sessionProc(pr) || serverProc(pr)
	}))
}

// NewWithProcs takes the process list from procs: tests use it.
func NewWithProcs(db string, procs func() []harness.Proc) *Provider {
	return &Provider{db: db, procs: procs, loads: map[string]load{}}
}

// Info names the provider. Verified stays false until a real session
// checks it and docs/compat.md has a row with that version.
func (p *Provider) Info() harness.Info { return harness.Info{Name: Name, Process: "opencode"} }

// sessionProc reports a process that can be a human's TUI: a process with
// no terminal is the server, and serve and run are no TUI.
func sessionProc(pr harness.Proc) bool {
	if pr.TTY == "" || harness.ListedProcess(pr.Argv) != Name {
		return false
	}
	w := harness.ArgWords(pr.Argv)
	return len(w) == 0 || w[0] != "serve" && w[0] != "run"
}

// serverProc reports the shared server. OpenCode starts it as `<self> serve
// --service`, on a terminal or not. A plain `serve` is another server.
func serverProc(pr harness.Proc) bool {
	if harness.ListedProcess(pr.Argv) != Name || !slices.Contains(pr.Argv, "--service") {
		return false
	}
	w := harness.ArgWords(pr.Argv)
	return len(w) > 0 && w[0] == "serve"
}

// dsn is the read-only address of a database.
func dsn(path string) string {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(1000)&_pragma=query_only(1)"}
	return u.String()
}

// open opens the database read only. The caller closes it.
func (p *Provider) open() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(p.db))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// row is the part of a session_v2 row smif uses. Times are ms, 0 for NULL.
type row struct {
	id, dir, title    string
	created, updated  int64
	busy, idle, claim int64
}

const rowColumns = `id, directory, COALESCE(title, ''), time_created, time_updated,
	COALESCE(time_busy, 0), COALESCE(time_idle, 0), COALESCE(time_suspended, 0)`

func scanRow(rows interface{ Scan(...any) error }) (r row, err error) {
	err = rows.Scan(&r.id, &r.dir, &r.title, &r.created, &r.updated, &r.busy, &r.idle, &r.claim)
	return r, err
}

// recent are the top-level, unarchived sessions updated since a time.
func recent(db *sql.DB, since time.Time) []harness.Candidate {
	rows, err := db.Query(`SELECT `+rowColumns+` FROM session_v2
		WHERE parent_id IS NULL AND time_archived IS NULL AND time_updated >= ?`, since.UnixMilli())
	if err != nil {
		return nil // no session_v2 table, or a changed one: smif doctor says so
	}
	defer rows.Close()
	var out []harness.Candidate
	for rows.Next() {
		if r, err := scanRow(rows); err == nil && harness.ValidID(r.id) {
			out = append(out, harness.Candidate{ID: r.id, Cwd: r.dir, Name: r.title,
				Created: time.UnixMilli(r.created), Wrote: time.UnixMilli(r.updated)})
		}
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
// no ps, so a machine without OpenCode pays only a stat.
func (p *Provider) Discover() []harness.Session {
	if p.db == "" {
		return nil
	}
	if _, err := os.Stat(p.db); err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.look()
	out := make([]harness.Session, 0, len(p.last))
	for _, s := range p.last {
		if harness.ProcessAlive(s.PID) {
			out = append(out, s)
		}
	}
	return out
}

// look runs ps at most every discoverEvery. The caller holds p.mu.
func (p *Provider) look() {
	if !p.at.IsZero() && time.Since(p.at) < discoverEvery {
		return
	}
	var tuis []harness.Proc
	server := false
	for _, pr := range p.procs() {
		switch {
		case serverProc(pr):
			server = true
		case sessionProc(pr):
			tuis = append(tuis, pr)
		}
	}
	p.last, p.server, p.at = p.match(tuis), server, time.Now()
}

// serverAlive reports whether the shared server lives. A crash can leave a
// claim in the database, so a state counts only with a live server. A
// failed ps shows no server, so it gives no state.
func (p *Provider) serverAlive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.look()
	return p.server
}

// load is what one read of a session gave.
type load struct {
	stamp  string
	row    row
	log    *harness.TurnLog
	wrote  time.Time
	marked bool // a user or assistant message was seen
}

// stateOf reads the session's columns. A running session with an open tool
// call gives "": a running tool looks the same as an approval prompt. So
// does a run with no claim, which the source reading does not explain. The
// pairing of the columns is an inference from the reading.
func stateOf(r row, pending bool) (string, time.Time) {
	switch {
	case r.busy > r.idle && r.claim != 0 && !pending:
		return "busy", time.UnixMilli(r.busy)
	case r.busy > r.idle:
		return "", time.UnixMilli(r.busy)
	case r.idle > 0:
		return "idle", time.UnixMilli(r.idle)
	}
	return "", time.Time{}
}

// State is what the session's row says, behind a live server.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	l, ok := p.read(s.SessionID)
	if !ok || !p.serverAlive() {
		return "", time.Time{}
	}
	_, act := l.log.Result(1)
	return stateOf(l.row, act.Pending != nil)
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

// Check compares the database on this machine with the source reading. The
// detail names counts and table names only, never a value from a row.
func (p *Provider) Check() (state, detail string) {
	if p.db == "" {
		return "none", "no OpenCode database"
	}
	if _, err := os.Stat(p.db); err != nil {
		return "none", "no database at " + p.db
	}
	db, err := p.open()
	if err != nil {
		return "changed", "the database does not open"
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_v2'`).Scan(&n); err != nil {
		return "changed", "the database cannot be read"
	}
	if n == 0 {
		return "none", "no session_v2 table: OpenCode V1, shown at the Listed level"
	}
	for table, q := range map[string]string{
		"session_v2":      `SELECT ` + rowColumns + `, parent_id, time_archived FROM session_v2 LIMIT 0`,
		"session_message": `SELECT id, session_id, type, seq, time_created, data FROM session_message LIMIT 0`,
		"session_part":    `SELECT message_id, session_id, ordinal, type, call_id, data FROM session_part LIMIT 0`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			return "changed", "table " + table + " lacks a column smif reads"
		}
		rows.Close()
	}
	rows, err := db.Query(`SELECT id FROM session_v2 ORDER BY time_updated DESC LIMIT ?`, checkSessions)
	if err != nil {
		return "changed", "session_v2 cannot be read"
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
