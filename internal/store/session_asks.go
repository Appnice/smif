package store

import (
	"database/sql"
	"errors"
	"time"
)

// SessionAsk ties a message to the interactive session whose fork answers
// it (ADR-0013): the worker resumes SessionID with --fork-session in Cwd.
type SessionAsk struct {
	MessageID, SessionID, Cwd string
	CreatedAt                 time.Time
}

// InsertSessionAsk records that message id is answered by a fork of session.
func (t *Tx) InsertSessionAsk(messageID, sessionID, cwd string) error {
	_, err := t.tx.Exec(`INSERT INTO session_asks (message_id, session_id, cwd, created_at) VALUES (?,?,?,?)`,
		messageID, sessionID, cwd, t.now.UnixMilli())
	return err
}

// SessionAskTx returns the session ask of a message, or nil.
func (t *Tx) SessionAskTx(messageID string) (*SessionAsk, error) {
	return scanSessionAsk(t.tx.QueryRow(`SELECT message_id, session_id, cwd, created_at FROM session_asks WHERE message_id=?`, messageID))
}

// SessionAsk returns the session ask of a message, or nil.
func (s *Store) SessionAsk(messageID string) (*SessionAsk, error) {
	return scanSessionAsk(s.db.QueryRow(`SELECT message_id, session_id, cwd, created_at FROM session_asks WHERE message_id=?`, messageID))
}

func scanSessionAsk(row *sql.Row) (*SessionAsk, error) {
	var a SessionAsk
	var at int64
	if err := row.Scan(&a.MessageID, &a.SessionID, &a.Cwd, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.CreatedAt = time.UnixMilli(at)
	return &a, nil
}

// SessionAsks returns the newest limit asks answered by forks of a session,
// oldest first.
func (s *Store) SessionAsks(sessionID string, limit int) ([]SessionAsk, error) {
	rows, err := s.db.Query(`SELECT message_id, session_id, cwd, created_at FROM session_asks WHERE session_id=?
ORDER BY created_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionAsk
	for rows.Next() {
		var a SessionAsk
		var at int64
		if err := rows.Scan(&a.MessageID, &a.SessionID, &a.Cwd, &at); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(at)
		out = append([]SessionAsk{a}, out...)
	}
	return out, rows.Err()
}
