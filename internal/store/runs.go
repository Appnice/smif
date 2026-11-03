package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Run states.
const (
	RunRunning    = "running"
	RunSucceeded  = "succeeded"
	RunNeedsHuman = "needs_human"
	RunFailed     = "failed"
	RunTimeout    = "timeout"
	RunKilled     = "killed"
	RunOrphaned   = "orphaned"
	RunCancelled  = "cancelled"
)

// Run is one handler execution.
type Run struct {
	ID, MessageID, Agent, Handler, Profile string
	Attempt                                int
	State                                  string
	PID, PGID                              int
	SessionID, ArchivedSession             string
	Workspace, Branch, BaseSHA, HeadSHA    string
	Diffstat                               string
	ResultJSON, UsageJSON                  string
	CostUSD                                float64
	InputTokens, OutputTokens              int64
	Error, Transcript, Stderr              string
	StartedAt                              time.Time
	EndedAt                                time.Time
}

const runCols = `id,message_id,agent,handler,profile,attempt,state,pid,pgid,session_id,archived_session,workspace,branch,base_sha,head_sha,
diffstat,result_json,usage_json,cost_usd,input_tokens,output_tokens,error,transcript,stderr,started_at,ended_at`

func scanRun(r scanner) (*Run, error) {
	var x Run
	var profile, session, archived, ws, branch, base, head, diffstat, result, usage, errs, transcript, stderr sql.NullString
	var pid, pgid, in, out, ended sql.NullInt64
	var cost sql.NullFloat64
	var started int64
	if err := r.Scan(&x.ID, &x.MessageID, &x.Agent, &x.Handler, &profile, &x.Attempt, &x.State, &pid, &pgid, &session, &archived,
		&ws, &branch, &base, &head, &diffstat, &result, &usage, &cost, &in, &out, &errs, &transcript, &stderr, &started, &ended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	x.Profile, x.SessionID, x.ArchivedSession = profile.String, session.String, archived.String
	x.Workspace, x.Branch, x.BaseSHA, x.HeadSHA, x.Diffstat = ws.String, branch.String, base.String, head.String, diffstat.String
	x.ResultJSON, x.UsageJSON = result.String, usage.String
	x.CostUSD = cost.Float64
	x.InputTokens, x.OutputTokens = in.Int64, out.Int64
	x.PID, x.PGID = int(pid.Int64), int(pgid.Int64)
	x.Error, x.Transcript, x.Stderr = errs.String, transcript.String, stderr.String
	x.StartedAt = time.UnixMilli(started)
	if ended.Valid {
		x.EndedAt = time.UnixMilli(ended.Int64)
	}
	return &x, nil
}

// InsertRun creates a run in state running.
func (t *Tx) InsertRun(r *Run) error {
	r.State = RunRunning
	r.StartedAt = t.now
	_, err := t.tx.Exec(`INSERT INTO runs (id,message_id,agent,handler,profile,attempt,state,workspace,branch,base_sha,session_id,started_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.MessageID, r.Agent, r.Handler, nz(r.Profile), r.Attempt, r.State,
		nz(r.Workspace), nz(r.Branch), nz(r.BaseSHA), nz(r.SessionID), t.now.UnixMilli())
	if err != nil {
		return err
	}
	return t.Audit("run.started", r.Agent, r.MessageID, r.ID, "", map[string]any{"handler": r.Handler, "profile": r.Profile, "attempt": r.Attempt})
}

// SetRunProcess records the pid and pgid once the process started, and the
// transcript path (empty for none) so a running run's transcript can be tailed.
func (s *Store) SetRunProcess(id string, pid, pgid int, transcript string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE runs SET pid=?, pgid=?, transcript=COALESCE(?, transcript) WHERE id=?`, pid, pgid, nz(transcript), id)
	return err
}

// SetRunSession records the claude session id as soon as it is known.
func (s *Store) SetRunSession(id, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE runs SET session_id=? WHERE id=?`, session, id)
	return err
}

// RunEnd carries the final columns of a run.
type RunEnd struct {
	State                     string
	SessionID                 string
	ArchivedSession           string
	HeadSHA, Diffstat, Branch string
	ResultJSON, UsageJSON     string
	CostUSD                   float64
	InputTokens, OutputTokens int64
	Error, Transcript, Stderr string
}

// FinishRun closes a run and audits it.
func (t *Tx) FinishRun(id string, e RunEnd) error {
	r, err := t.GetRun(id)
	if err != nil {
		return err
	}
	if e.SessionID == "" {
		e.SessionID = r.SessionID
	}
	if e.Branch == "" {
		e.Branch = r.Branch
	}
	_, err = t.tx.Exec(`UPDATE runs SET state=?, session_id=?, archived_session=?, head_sha=?, diffstat=?, branch=?, result_json=?, usage_json=?,
cost_usd=?, input_tokens=?, output_tokens=?, error=?, transcript=?, stderr=?, ended_at=? WHERE id=?`,
		e.State, nz(e.SessionID), nz(e.ArchivedSession), nz(e.HeadSHA), nz(e.Diffstat), nz(e.Branch), nz(e.ResultJSON), nz(e.UsageJSON),
		e.CostUSD, e.InputTokens, e.OutputTokens, nz(e.Error), nz(e.Transcript), nz(truncate(e.Stderr, 64<<10)), t.now.UnixMilli(), id)
	if err != nil {
		return err
	}
	return t.Audit("run.finished", r.Agent, r.MessageID, id, "", map[string]any{"state": e.State, "error": e.Error, "cost_usd": e.CostUSD})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// GetRun reads a run in the transaction.
func (t *Tx) GetRun(id string) (*Run, error) {
	return scanRun(t.tx.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

// GetRun reads a run.
func (s *Store) GetRun(id string) (*Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
	if err == nil {
		return r, nil
	}
	rs, err2 := s.Runs(`id LIKE ? LIMIT 2`, "%"+strings.ToUpper(id))
	if err2 != nil || len(rs) != 1 {
		return nil, err
	}
	return rs[0], nil
}

// Runs queries runs.
func (s *Store) Runs(where string, args ...any) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs`
	if where != "" {
		q += ` WHERE ` + where
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Runs in a transaction.
func (t *Tx) Runs(where string, args ...any) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs`
	if where != "" {
		q += ` WHERE ` + where
	}
	rows, err := t.tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- presence ----

// Presence is one interactive session seen in a repo. Turn, TTY, PID and
// Term come from the hooks (ADR-0012); they are empty for a session first
// seen by the status line.
type Presence struct {
	Repo, SessionID, State string
	LastSeen               time.Time
	Turn                   string // "busy" (a prompt is running), "idle" (waits for input) or ""
	TurnAt                 time.Time
	TTY                    string
	PID                    int
	Term                   string
	Title                  string // claude --name or /rename
	LastPrompt             string // the human's last prompt, shortened
	Cwd                    string // the session's folder (from discovery; not stored)
}

// PresenceUpdate is one hook or status-line report. Empty fields keep the
// stored value; Turn "" keeps the turn.
type PresenceUpdate struct {
	Turn, TTY, Term   string
	PID               int
	Title, LastPrompt string
}

// UpsertPresence records a hook or status-line heartbeat.
func (t *Tx) UpsertPresence(repo, session, state string, u PresenceUpdate) error {
	if state == "ended" {
		_, err := t.tx.Exec(`DELETE FROM presence WHERE repo=? AND session_id=?`, repo, session)
		return err
	}
	now := t.now.UnixMilli()
	turnAt := int64(0)
	if u.Turn != "" {
		turnAt = now
	}
	_, err := t.tx.Exec(`INSERT INTO presence (repo,session_id,last_seen,state,turn,turn_at,tty,pid,term,title,last_prompt) VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo,session_id) DO UPDATE SET last_seen=excluded.last_seen, state=excluded.state,
  turn=CASE WHEN excluded.turn<>'' THEN excluded.turn ELSE turn END,
  turn_at=CASE WHEN excluded.turn<>'' THEN excluded.turn_at ELSE turn_at END,
  tty=CASE WHEN excluded.tty<>'' THEN excluded.tty ELSE tty END,
  pid=CASE WHEN excluded.pid<>0 THEN excluded.pid ELSE pid END,
  term=CASE WHEN excluded.term<>'' THEN excluded.term ELSE term END,
  title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE title END,
  last_prompt=CASE WHEN excluded.last_prompt<>'' THEN excluded.last_prompt ELSE last_prompt END`,
		repo, session, now, state, u.Turn, turnAt, u.TTY, u.PID, u.Term, u.Title, u.LastPrompt)
	return err
}

// SetPresenceTurn sets one session's turn (after Mailroom typed a prompt).
func (t *Tx) SetPresenceTurn(session, turn string) error {
	_, err := t.tx.Exec(`UPDATE presence SET turn=?, turn_at=? WHERE session_id=?`, turn, t.now.UnixMilli(), session)
	return err
}

const presenceCols = `repo,session_id,state,last_seen,turn,turn_at,tty,pid,term,title,last_prompt`

func scanPresence(rows *sql.Rows) ([]Presence, error) {
	defer rows.Close()
	var out []Presence
	for rows.Next() {
		var p Presence
		var ls, ta int64
		if err := rows.Scan(&p.Repo, &p.SessionID, &p.State, &ls, &p.Turn, &ta, &p.TTY, &p.PID, &p.Term, &p.Title, &p.LastPrompt); err != nil {
			return nil, err
		}
		p.LastSeen = time.UnixMilli(ls)
		if ta > 0 {
			p.TurnAt = time.UnixMilli(ta)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivePresence returns live sessions for repo newer than ttl.
func (s *Store) ActivePresence(repo string, ttl time.Duration) ([]Presence, error) {
	rows, err := s.db.Query(`SELECT `+presenceCols+` FROM presence WHERE repo=? AND last_seen>=?`, repo, s.now().Add(-ttl).UnixMilli())
	if err != nil {
		return nil, err
	}
	return scanPresence(rows)
}

// AllPresence returns every stored session, newest first.
func (s *Store) AllPresence() ([]Presence, error) {
	rows, err := s.db.Query(`SELECT ` + presenceCols + ` FROM presence ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	return scanPresence(rows)
}

// ---- cursors ----

// DigestCursor returns an agent's digest cursor.
func (s *Store) DigestCursor(agent string) int64 {
	var c int64
	_ = s.db.QueryRow(`SELECT digest_cursor FROM cursors WHERE agent=?`, agent).Scan(&c)
	return c
}

// SetDigestCursor advances the cursor.
func (t *Tx) SetDigestCursor(agent string, c int64) error {
	_, err := t.tx.Exec(`INSERT INTO cursors (agent,digest_cursor) VALUES (?,?) ON CONFLICT(agent) DO UPDATE SET digest_cursor=MAX(digest_cursor,excluded.digest_cursor)`, agent, c)
	return err
}

// ---- budgets ----

// AddUsage accumulates a run's usage into the agent's day.
func (t *Tx) AddUsage(agent, day string, cost float64, tokens int64) error {
	_, err := t.tx.Exec(`INSERT INTO budgets (agent,day,cost_usd,tokens) VALUES (?,?,?,?)
ON CONFLICT(agent,day) DO UPDATE SET cost_usd=cost_usd+excluded.cost_usd, tokens=tokens+excluded.tokens`, agent, day, cost, tokens)
	return err
}

// Usage returns an agent's spend for a day.
func (s *Store) Usage(agent, day string) (cost float64, tokens int64) {
	_ = s.db.QueryRow(`SELECT cost_usd,tokens FROM budgets WHERE agent=? AND day=?`, agent, day).Scan(&cost, &tokens)
	return
}

// ---- blobs ----

// PutBlob records a blob row (refcount 0 until a message references it).
func (t *Tx) PutBlob(sha string, size int64, mime string) error {
	_, err := t.tx.Exec(`INSERT INTO blobs (sha256,size,mime,refcount,zero_since) VALUES (?,?,?,0,?) ON CONFLICT(sha256) DO NOTHING`,
		sha, size, mime, t.now.UnixMilli())
	return err
}

// BlobInfo returns size and mime for a blob.
func (s *Store) BlobInfo(sha string) (size int64, mime string, err error) {
	err = s.db.QueryRow(`SELECT size,mime FROM blobs WHERE sha256=?`, sha).Scan(&size, &mime)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// ExpiredBlobs returns blobs unreferenced since before cutoff.
func (s *Store) ExpiredBlobs(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT sha256 FROM blobs WHERE refcount<=0 AND zero_since IS NOT NULL AND zero_since<?`, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteBlob removes a blob row.
func (t *Tx) DeleteBlob(sha string) error {
	_, err := t.tx.Exec(`DELETE FROM blobs WHERE sha256=? AND refcount<=0`, sha)
	return err
}
