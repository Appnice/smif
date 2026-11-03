// Package store owns Mailroom's SQLite database: messages, runs, presence,
// cursors, budgets, blobs, threads and the audit table (RFC 8.1).
//
// Every message state change goes through Tx.Transition, which checks the
// state machine (RFC 5.2) and appends an audit row in the same transaction.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/appnice/mailroom/internal/envelope"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// SchemaVersion is the highest migration number shipped in this binary.
var SchemaVersion = func() int {
	n := 0
	ents, _ := fs.ReadDir(migrationFS, "migrations")
	for _, e := range ents {
		v, _ := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if v > n {
			n = v
		}
	}
	return n
}()

// State is a message lifecycle state (RFC 5.2).
type State string

// The nine states. Four are terminal: done, rejected, expired, dead_letter.
const (
	Accepted   State = "accepted"
	Queued     State = "queued"
	Running    State = "running"
	RetryWait  State = "retry_wait"
	NeedsHuman State = "needs_human"
	Done       State = "done"
	Rejected   State = "rejected"
	Expired    State = "expired"
	DeadLetter State = "dead_letter"
)

// AllStates lists every state.
var AllStates = []State{Accepted, Queued, Running, RetryWait, NeedsHuman, Done, Rejected, Expired, DeadLetter}

// Terminal reports whether s is one of the four terminal states.
func (s State) Terminal() bool {
	return s == Done || s == Rejected || s == Expired || s == DeadLetter
}

// Open reports whether a message in s still counts as open for dedupe.
func (s State) Open() bool { return !s.Terminal() && s != NeedsHuman }

// transitions is the complete state machine. Nothing else is legal.
var transitions = map[State][]State{
	Accepted:   {Queued, Rejected, NeedsHuman, Done, Expired},
	Queued:     {Running, Rejected, NeedsHuman, Expired},
	Running:    {Done, NeedsHuman, RetryWait, DeadLetter, Rejected},
	RetryWait:  {Queued, DeadLetter, Rejected, Expired},
	NeedsHuman: {Done, Queued, Rejected},
	DeadLetter: {Queued},
}

// Allowed reports whether from -> to is a legal transition.
func Allowed(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ErrIllegal is returned for a transition the state machine forbids.
var ErrIllegal = errors.New("illegal state transition")

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// AuditRow is one audit record.
type AuditRow struct {
	Seq       int64          `json:"seq"`
	TS        time.Time      `json:"ts"`
	Event     string         `json:"event"`
	Agent     string         `json:"agent,omitempty"`
	MessageID string         `json:"message_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	ThreadID  string         `json:"thread_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Store wraps the database.
type Store struct {
	db   *sql.DB
	path string
	mu   sync.Mutex // serialises write transactions
	// OnAudit is called after commit with the audit rows the transaction wrote.
	OnAudit func([]AuditRow)
	now     func() time.Time
}

// Open opens (and migrates) the database at dir/mailroom.db.
func Open(dir string) (*Store, error) {
	p := filepath.Join(dir, "mailroom.db")
	dsn := "file:" + p + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: p, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// SetClock overrides the clock (tests).
func (s *Store) SetClock(f func() time.Time) { s.now = f }

// Now returns the store clock's time.
func (s *Store) Now() time.Time { return s.now() }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for read-only queries in other packages.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	ents, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad name", e.Name())
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, v).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, _ := migrationFS.ReadFile("migrations/" + e.Name())
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(b)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations VALUES (?,?)`, v, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Version returns the applied schema version.
func (s *Store) Version() int {
	var v int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v)
	return v
}

// Tx is a write transaction.
type Tx struct {
	tx    *sql.Tx
	s     *Store
	audit []AuditRow
	now   time.Time
}

// Update runs f in a serialised write transaction.
func (s *Store) Update(f func(*Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sqltx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	tx := &Tx{tx: sqltx, s: s, now: s.now()}
	if err := f(tx); err != nil {
		sqltx.Rollback()
		return err
	}
	if err := sqltx.Commit(); err != nil {
		return err
	}
	if s.OnAudit != nil && len(tx.audit) > 0 {
		s.OnAudit(tx.audit)
	}
	return nil
}

// Now is the transaction timestamp.
func (t *Tx) Now() time.Time { return t.now }

// Exec runs a statement inside the transaction.
func (t *Tx) Exec(q string, args ...any) (sql.Result, error) { return t.tx.Exec(q, args...) }

// QueryRow runs a query inside the transaction.
func (t *Tx) QueryRow(q string, args ...any) *sql.Row { return t.tx.QueryRow(q, args...) }

// Audit appends an audit row inside the transaction.
func (t *Tx) Audit(event, agent, msgID, runID, threadID string, detail map[string]any) error {
	var d []byte
	if detail != nil {
		d, _ = json.Marshal(detail)
	}
	res, err := t.tx.Exec(`INSERT INTO audit (ts,event,agent,message_id,run_id,thread_id,detail) VALUES (?,?,?,?,?,?,?)`,
		t.now.UnixMilli(), event, nz(agent), nz(msgID), nz(runID), nz(threadID), nzb(d))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	t.audit = append(t.audit, AuditRow{Seq: seq, TS: t.now, Event: event, Agent: agent, MessageID: msgID, RunID: runID, ThreadID: threadID, Detail: detail})
	return nil
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nzb(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// Message is a stored message with its routing and lifecycle columns.
type Message struct {
	Env           envelope.Envelope
	State         State
	RuleID        string
	Handler       string
	Profile       string
	Attempt       int
	ExtraAttempts int
	NotBefore     time.Time
	HoldReason    string
	Reason        string
	Summary       string
	HumanQuestion string
	Coalesced     int
	Recurrence    bool
	AnsweredBy    string
	Seen          bool
	HandledSeq    int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const msgCols = `id,thread_id,parent_id,from_agent,from_origin,from_run,to_agent,kind,intent,priority,state,hop,dedupe_key,
coalesced_count,deadline,rule_id,handler,profile,attempt,extra_attempts,not_before,hold_reason,reason,summary,human_question,
recurrence,answered_by,seen,handled_seq,envelope_json,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanMessage(r scanner) (*Message, error) {
	var (
		m                                                                   Message
		id, thread, fromAgent, fromOrigin, to, kind, intent, state, envJSON string
		parent, fromRun, dedupe, rule, handler, profile, hold, reason       sql.NullString
		summary, hq, answered                                               sql.NullString
		prio, hop, coalesced, attempt, extra, recurrence, seen              int
		deadline, handled                                                   sql.NullInt64
		notBefore, created, updated                                         int64
	)
	if err := r.Scan(&id, &thread, &parent, &fromAgent, &fromOrigin, &fromRun, &to, &kind, &intent, &prio, &state, &hop, &dedupe,
		&coalesced, &deadline, &rule, &handler, &profile, &attempt, &extra, &notBefore, &hold, &reason, &summary, &hq,
		&recurrence, &answered, &seen, &handled, &envJSON, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(envJSON), &m.Env); err != nil {
		return nil, fmt.Errorf("message %s: bad envelope: %w", id, err)
	}
	m.State = State(state)
	m.RuleID, m.Handler, m.Profile = rule.String, handler.String, profile.String
	m.Attempt, m.ExtraAttempts = attempt, extra
	m.NotBefore = time.UnixMilli(notBefore)
	m.HoldReason, m.Reason, m.Summary, m.HumanQuestion = hold.String, reason.String, summary.String, hq.String
	m.Coalesced = coalesced
	m.Recurrence = recurrence == 1
	m.AnsweredBy = answered.String
	m.Seen = seen == 1
	m.HandledSeq = handled.Int64
	m.CreatedAt, m.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return &m, nil
}

// InsertMessage stores a new message in state accepted and audits it.
func (t *Tx) InsertMessage(e *envelope.Envelope, recurrence bool) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var deadline any
	if e.Deadline != nil {
		deadline = e.Deadline.UnixMilli()
	}
	var parent any
	if e.ParentID != nil {
		parent = *e.ParentID
	}
	rec := 0
	if recurrence {
		rec = 1
	}
	_, err = t.tx.Exec(`INSERT INTO messages (id,thread_id,parent_id,from_agent,from_origin,from_run,to_agent,kind,intent,priority,
state,hop,dedupe_key,deadline,recurrence,envelope_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ThreadID, parent, e.From.Agent, e.From.Origin, nz(e.From.RunID), e.To, e.Kind, e.Intent,
		priorityLevel(e.Priority), string(Accepted), e.Hop, nz(e.DedupeKey), deadline, rec, string(b),
		e.CreatedAt.UnixMilli(), t.now.UnixMilli())
	if err != nil {
		return err
	}
	if _, err := t.tx.Exec(`INSERT OR IGNORE INTO threads (thread_id) VALUES (?)`, e.ThreadID); err != nil {
		return err
	}
	for _, a := range e.Attachments {
		if _, err := t.tx.Exec(`INSERT OR IGNORE INTO message_blobs (message_id,sha256,name) VALUES (?,?,?)`, e.ID, a.SHA256, a.Name); err != nil {
			return err
		}
		if _, err := t.tx.Exec(`UPDATE blobs SET refcount=refcount+1, zero_since=NULL WHERE sha256=?`, a.SHA256); err != nil {
			return err
		}
	}
	return t.Audit("message.accepted", e.To, e.ID, e.From.RunID, e.ThreadID, map[string]any{
		"from": e.From.Agent, "origin": e.From.Origin, "kind": e.Kind, "intent": e.Intent, "hop": e.Hop, "state": string(Accepted)})
}

func priorityLevel(p string) int {
	switch p {
	case "low":
		return 0
	case "high":
		return 2
	case "urgent":
		return 3
	default:
		return 1
	}
}

// Fields are optional column updates applied with a transition.
type Fields struct {
	RuleID, Handler, Profile *string
	Attempt, ExtraAttempts   *int
	NotBefore                *time.Time
	HoldReason, Reason       *string
	Summary, HumanQuestion   *string
	AnsweredBy               *string
}

// Str and friends make Fields literals short.
func Str(s string) *string        { return &s }
func Int(i int) *int              { return &i }
func Time(t time.Time) *time.Time { return &t }

// Transition moves a message from its current state to `to`, applying
// fields, and appends an audit row. It fails with ErrIllegal if the state
// machine forbids the move, and with a mismatch error if the current state
// is not one of `from` (when given).
func (t *Tx) Transition(id string, to State, f Fields, detail map[string]any, from ...State) (*Message, error) {
	m, err := t.GetMessage(id)
	if err != nil {
		return nil, err
	}
	if len(from) > 0 {
		ok := false
		for _, s := range from {
			if m.State == s {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("message %s is %s, expected %v", id, m.State, from)
		}
	}
	if !Allowed(m.State, to) {
		return nil, fmt.Errorf("%w: %s -> %s (message %s)", ErrIllegal, m.State, to, id)
	}
	sets := []string{"state=?", "updated_at=?"}
	args := []any{string(to), t.now.UnixMilli()}
	add := func(col string, v any) { sets = append(sets, col+"=?"); args = append(args, v) }
	if f.RuleID != nil {
		add("rule_id", *f.RuleID)
	}
	if f.Handler != nil {
		add("handler", *f.Handler)
	}
	if f.Profile != nil {
		add("profile", nz(*f.Profile))
	}
	if f.Attempt != nil {
		add("attempt", *f.Attempt)
	}
	if f.ExtraAttempts != nil {
		add("extra_attempts", *f.ExtraAttempts)
	}
	if f.NotBefore != nil {
		add("not_before", f.NotBefore.UnixMilli())
	}
	if f.HoldReason != nil {
		add("hold_reason", nz(*f.HoldReason))
	}
	if f.Reason != nil {
		add("reason", nz(*f.Reason))
	}
	if f.Summary != nil {
		add("summary", nz(*f.Summary))
	}
	if f.HumanQuestion != nil {
		add("human_question", nz(*f.HumanQuestion))
	}
	if f.AnsweredBy != nil {
		add("answered_by", nz(*f.AnsweredBy))
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["from_state"] = string(m.State)
	detail["to_state"] = string(to)
	if f.Reason != nil && *f.Reason != "" {
		detail["reason"] = *f.Reason
	}
	if err := t.Audit("message.transition", m.Env.To, id, "", m.Env.ThreadID, detail); err != nil {
		return nil, err
	}
	if to.Terminal() || to == NeedsHuman {
		add("handled_seq", t.audit[len(t.audit)-1].Seq)
	}
	args = append(args, id)
	if _, err := t.tx.Exec(`UPDATE messages SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return nil, err
	}
	if to.Terminal() {
		if _, err := t.tx.Exec(`UPDATE blobs SET refcount=MAX(refcount-1,0), zero_since=CASE WHEN refcount-1<=0 THEN ? ELSE zero_since END
WHERE sha256 IN (SELECT sha256 FROM message_blobs WHERE message_id=?) AND ? NOT IN ('dead_letter')`, t.now.UnixMilli(), id, string(to)); err != nil {
			return nil, err
		}
	}
	return t.GetMessage(id)
}

// SetFields updates columns without a state change (holds, not_before).
func (t *Tx) SetFields(id string, f Fields) error {
	sets := []string{"updated_at=?"}
	args := []any{t.now.UnixMilli()}
	add := func(col string, v any) { sets = append(sets, col+"=?"); args = append(args, v) }
	if f.NotBefore != nil {
		add("not_before", f.NotBefore.UnixMilli())
	}
	if f.HoldReason != nil {
		add("hold_reason", nz(*f.HoldReason))
	}
	if f.Reason != nil {
		add("reason", nz(*f.Reason))
	}
	if f.AnsweredBy != nil {
		add("answered_by", nz(*f.AnsweredBy))
	}
	if f.Summary != nil {
		add("summary", nz(*f.Summary))
	}
	if f.Attempt != nil {
		add("attempt", *f.Attempt)
	}
	if f.ExtraAttempts != nil {
		add("extra_attempts", *f.ExtraAttempts)
	}
	args = append(args, id)
	_, err := t.tx.Exec(`UPDATE messages SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

// MarkSeen marks a reply as fetched by its asker.
func (t *Tx) MarkSeen(id string) error {
	_, err := t.tx.Exec(`UPDATE messages SET seen=1 WHERE id=?`, id)
	return err
}

// Coalesce bumps coalesced_count on an open message and audits it.
func (t *Tx) Coalesce(m *Message, dup *envelope.Envelope) error {
	if _, err := t.tx.Exec(`UPDATE messages SET coalesced_count=coalesced_count+1, updated_at=? WHERE id=?`, t.now.UnixMilli(), m.Env.ID); err != nil {
		return err
	}
	return t.Audit("message.coalesced", m.Env.To, m.Env.ID, dup.From.RunID, m.Env.ThreadID, map[string]any{
		"dedupe_key": dup.DedupeKey, "from": dup.From.Agent, "subject": dup.Subject})
}

// ResetCoalesced zeroes the counter (used after a recurrence is created).
func (t *Tx) ResetCoalesced(id string) error {
	_, err := t.tx.Exec(`UPDATE messages SET coalesced_count=0 WHERE id=?`, id)
	return err
}

// GetMessage reads a message inside the transaction.
func (t *Tx) GetMessage(id string) (*Message, error) {
	return scanMessage(t.tx.QueryRow(`SELECT `+msgCols+` FROM messages WHERE id=?`, id))
}

// GetMessage reads a message.
func (s *Store) GetMessage(id string) (*Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+msgCols+` FROM messages WHERE id=?`, id))
}

// FindMessage resolves an id or a unique suffix of one.
func (s *Store) FindMessage(ref string) (*Message, error) {
	if m, err := s.GetMessage(ref); err == nil {
		return m, nil
	}
	rows, err := s.db.Query(`SELECT id FROM messages WHERE id LIKE ? LIMIT 2`, "%"+strings.ToUpper(ref))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 {
		return nil, ErrNotFound
	}
	return s.GetMessage(ids[0])
}

// Messages runs a query returning message rows. where is appended after WHERE.
func (s *Store) Messages(where string, args ...any) ([]*Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages`
	if where != "" {
		q += ` WHERE ` + where
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Messages inside a transaction.
func (t *Tx) Messages(where string, args ...any) ([]*Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages`
	if where != "" {
		q += ` WHERE ` + where
	}
	rows, err := t.tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenByDedupe returns a non-terminal (and not needs_human) message to `to`
// with the dedupe key.
func (t *Tx) OpenByDedupe(to, key string) (*Message, error) {
	// needs_human is not terminal (RFC 6.5): a repeat while the human has not
	// answered joins that item instead of raising a second one.
	ms, err := t.Messages(`to_agent=? AND dedupe_key=? AND state IN ('accepted','queued','running','retry_wait','needs_human') ORDER BY created_at LIMIT 1`, to, key)
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return ms[0], nil
}

// LastDoneByDedupe returns the newest done message to `to` with the key that
// finished after `since`.
func (t *Tx) LastDoneByDedupe(to, key string, since time.Time) (*Message, error) {
	ms, err := t.Messages(`to_agent=? AND dedupe_key=? AND state IN ('done','needs_human') AND updated_at>=? ORDER BY updated_at DESC LIMIT 1`,
		to, key, since.UnixMilli())
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return ms[0], nil
}

// Thread is a thread's guard state.
type Thread struct {
	ID           string
	Frozen       bool
	FrozenReason string
	Branch       string
}

// GetThread returns the thread row.
func (t *Tx) GetThread(id string) (*Thread, error) {
	var th Thread
	var frozen int
	var reason, branch sql.NullString
	err := t.tx.QueryRow(`SELECT thread_id,frozen,frozen_reason,branch FROM threads WHERE thread_id=?`, id).Scan(&th.ID, &frozen, &reason, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return &Thread{ID: id}, nil
	}
	if err != nil {
		return nil, err
	}
	th.Frozen, th.FrozenReason, th.Branch = frozen == 1, reason.String, branch.String
	return &th, nil
}

// FreezeThread marks a thread frozen.
func (t *Tx) FreezeThread(id, reason string) error {
	if _, err := t.tx.Exec(`INSERT INTO threads (thread_id,frozen,frozen_reason) VALUES (?,1,?)
ON CONFLICT(thread_id) DO UPDATE SET frozen=1, frozen_reason=excluded.frozen_reason`, id, reason); err != nil {
		return err
	}
	return t.Audit("thread.frozen", "", "", "", id, map[string]any{"reason": reason})
}

// SetThreadBranch records the worktree branch used by a thread.
func (t *Tx) SetThreadBranch(id, agent, branch string) error {
	_, err := t.tx.Exec(`INSERT INTO threads (thread_id,branch) VALUES (?,?) ON CONFLICT(thread_id) DO UPDATE SET branch=excluded.branch`, id+"|"+agent, branch)
	return err
}

// ThreadBranch returns the branch recorded for (thread, agent).
func (s *Store) ThreadBranch(thread, agent string) string {
	var b sql.NullString
	_ = s.db.QueryRow(`SELECT branch FROM threads WHERE thread_id=?`, thread+"|"+agent).Scan(&b)
	return b.String
}

// CountThread returns how many messages a thread has.
func (t *Tx) CountThread(id string) (int, error) {
	var n int
	err := t.tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id=?`, id).Scan(&n)
	return n, err
}

// CountPair counts messages in a thread between two agents (either
// direction) created at or after since.
func (t *Tx) CountPair(thread, a, b string, since time.Time) (int, error) {
	var n int
	err := t.tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id=? AND created_at>=? AND
((from_agent=? AND to_agent=?) OR (from_agent=? AND to_agent=?))`, thread, since.UnixMilli(), a, b, b, a).Scan(&n)
	return n, err
}

// AuditSince returns audit rows after seq (for tail).
func (s *Store) AuditSince(seq int64, limit int) ([]AuditRow, error) {
	rows, err := s.db.Query(`SELECT seq,ts,event,agent,message_id,run_id,thread_id,detail FROM audit WHERE seq>? ORDER BY seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var ts int64
		var agent, msg, run, thread, detail sql.NullString
		if err := rows.Scan(&r.Seq, &ts, &r.Event, &agent, &msg, &run, &thread, &detail); err != nil {
			return nil, err
		}
		r.TS = time.UnixMilli(ts)
		r.Agent, r.MessageID, r.RunID, r.ThreadID = agent.String, msg.String, run.String, thread.String
		if detail.Valid {
			_ = json.Unmarshal([]byte(detail.String), &r.Detail)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditTail returns the newest n audit rows, oldest first.
func (s *Store) AuditTail(n int) ([]AuditRow, error) {
	if n <= 0 {
		return nil, nil
	}
	return s.AuditWhere(`seq IN (SELECT seq FROM audit ORDER BY seq DESC LIMIT ?)`, n)
}

// AuditHead returns the newest audit seq, or 0 for an empty table.
func (s *Store) AuditHead() (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM audit`).Scan(&n)
	return n.Int64, err
}

// AuditFor returns audit rows for a message.
func (s *Store) AuditFor(msgID string) ([]AuditRow, error) {
	rows, err := s.AuditWhere(`message_id=?`, msgID)
	return rows, err
}

// AuditWhere returns audit rows matching a condition.
func (s *Store) AuditWhere(where string, args ...any) ([]AuditRow, error) {
	rows, err := s.db.Query(`SELECT seq,ts,event,agent,message_id,run_id,thread_id,detail FROM audit WHERE `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var ts int64
		var agent, msg, run, thread, detail sql.NullString
		if err := rows.Scan(&r.Seq, &ts, &r.Event, &agent, &msg, &run, &thread, &detail); err != nil {
			return nil, err
		}
		r.TS = time.UnixMilli(ts)
		r.Agent, r.MessageID, r.RunID, r.ThreadID = agent.String, msg.String, run.String, thread.String
		if detail.Valid {
			_ = json.Unmarshal([]byte(detail.String), &r.Detail)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// KV helpers for small daemon state.
func (t *Tx) SetKV(k, v string) error {
	_, err := t.tx.Exec(`INSERT INTO kv (k,v) VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// GetKV reads a kv entry.
func (s *Store) GetKV(k string) string {
	var v string
	_ = s.db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	return v
}
