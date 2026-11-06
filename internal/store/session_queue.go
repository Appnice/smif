package store

import (
	"fmt"
	"slices"
	"time"
)

// The prompt queue (ADR-0021): prompts the human wrote for a busy or
// waiting session. The daemon's watcher types the oldest one when the
// session is idle. Only the human's RPC call adds a row, so no inbound
// mail reaches it (D4).

// States of a queue item.
const (
	QueueQueued    = "queued"
	QueueSent      = "sent"
	QueueCancelled = "cancelled"
	QueueExpired   = "expired"
	QueueGone      = "gone"
)

// queueMoves are the allowed state changes. sent goes back to queued only
// when no key reached the terminal.
var queueMoves = map[string][]string{
	QueueQueued: {QueueSent, QueueCancelled, QueueExpired, QueueGone},
	QueueSent:   {QueueQueued},
}

// QueueItem is one prompt in the queue.
type QueueItem struct {
	ID                           int64
	SessionID, Agent, Name, Text string
	CreatedAt                    time.Time
	State, Error                 string
}

func (i QueueItem) detail() map[string]any {
	return map[string]any{"session_id": i.SessionID, "item": i.ID, "chars": len([]rune(i.Text))}
}

// AddQueued writes a queued item and its audit row. The audit keeps the
// length, never the text.
func (t *Tx) AddQueued(i QueueItem) (int64, error) {
	res, err := t.tx.Exec(`INSERT INTO session_queue (session_id, agent, name, text, created_at) VALUES (?,?,?,?,?)`,
		i.SessionID, i.Agent, i.Name, i.Text, t.now.UnixMilli())
	if err != nil {
		return 0, err
	}
	i.ID, err = res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return i.ID, t.Audit("session.queue", i.Agent, "", "", "", i.detail())
}

// QueueMove changes the state of an item and writes the audit row in the
// same transaction. It reports false, and changes nothing, when the item
// is no longer in state from. A move that queueMoves does not allow is an
// error.
func (t *Tx) QueueMove(i QueueItem, from, to, why string) (bool, error) {
	if !slices.Contains(queueMoves[from], to) {
		return false, fmt.Errorf("queue item %d: no move from %s to %s", i.ID, from, to)
	}
	res, err := t.tx.Exec(`UPDATE session_queue SET state=?, error=? WHERE id=? AND state=?`, to, why, i.ID, from)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	d := i.detail()
	if why != "" {
		d["why"] = why
	}
	return true, t.Audit("session.queue."+to, i.Agent, "", "", "", d)
}

// QueueRefused keeps why an item did not go. The state stays, so the audit
// row comes only when the reason changes (a refusal can repeat every 30 s
// for a day).
func (t *Tx) QueueRefused(i QueueItem, why string) error {
	if why == i.Error {
		return nil
	}
	if _, err := t.tx.Exec(`UPDATE session_queue SET error=? WHERE id=? AND state=?`, why, i.ID, QueueQueued); err != nil {
		return err
	}
	d := i.detail()
	d["why"] = why
	return t.Audit("session.queue.refused", i.Agent, "", "", "", d)
}

// QueueNote keeps why a sent item started no turn. The sent audit row
// exists already, and deliverPrompt audits session.prompt_unsent.
func (t *Tx) QueueNote(id int64, why string) error {
	_, err := t.tx.Exec(`UPDATE session_queue SET error=? WHERE id=?`, why, id)
	return err
}

const queueColumns = `id, session_id, agent, name, text, created_at, state, error`

// QueuedItems returns the items still queued, oldest first.
func (s *Store) QueuedItems() ([]QueueItem, error) {
	return s.queueItems(`state=? ORDER BY id`, QueueQueued)
}

// QueueItem returns one item, or nil.
func (s *Store) QueueItem(id int64) (*QueueItem, error) {
	items, err := s.queueItems(`id=?`, id)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

// QueueItems returns the items of a session in every state, oldest first.
func (s *Store) QueueItems(sessionID string) ([]QueueItem, error) {
	return s.queueItems(`session_id=? ORDER BY id`, sessionID)
}

func (s *Store) queueItems(where string, args ...any) ([]QueueItem, error) {
	rows, err := s.db.Query(`SELECT `+queueColumns+` FROM session_queue WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var i QueueItem
		var at int64
		if err := rows.Scan(&i.ID, &i.SessionID, &i.Agent, &i.Name, &i.Text, &at, &i.State, &i.Error); err != nil {
			return nil, err
		}
		i.CreatedAt = time.UnixMilli(at)
		out = append(out, i)
	}
	return out, rows.Err()
}
