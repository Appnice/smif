package opencode

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// A session is one row in session_v2, its messages in session_message and
// each message's parts in session_part, in `ordinal` order. The columns come
// from session/sql.ts. The keys inside `data` come from struct definitions
// in session-message.ts, not from a real row.

type message struct {
	id, kind string
	at       time.Time
	data     string
}

type part struct {
	kind, callID, data string
	ordinal            int
}

// partData is the part of a part's data smif uses.
type partData struct {
	Text  string `json:"text"`
	Name  string `json:"name"`
	State struct {
		Status string         `json:"status"`
		Input  map[string]any `json:"input"`
	} `json:"state"`
}

func messages(db *sql.DB, id string) ([]message, error) {
	rows, err := db.Query(`SELECT id, type, time_created, data FROM session_message
		WHERE session_id = ? ORDER BY seq DESC LIMIT ?`, id, maxMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []message
	for rows.Next() {
		var m message
		var ms int64
		if err := rows.Scan(&m.id, &m.kind, &ms, &m.data); err != nil {
			return nil, err
		}
		m.at = time.UnixMilli(ms)
		out = append(out, m)
	}
	return out, rows.Err()
}

func parts(db *sql.DB, id string) (map[string][]part, error) {
	rows, err := db.Query(`SELECT message_id, type, COALESCE(call_id, ''), ordinal, data FROM session_part
		WHERE session_id = ? AND message_id IN
			(SELECT id FROM session_message WHERE session_id = ? ORDER BY seq DESC LIMIT ?)
		ORDER BY ordinal`, id, id, maxMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]part{}
	for rows.Next() {
		var mid string
		var x part
		if err := rows.Scan(&mid, &x.kind, &x.callID, &x.ordinal, &x.data); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], x)
	}
	return out, rows.Err()
}

// step adds a tool part to the log. A part that ended is done.
func step(log *harness.TurnLog, m message, x part, d partData) {
	id := x.callID
	if id == "" {
		id = m.id + "/" + strconv.Itoa(x.ordinal)
	}
	log.Call(harness.Step{ID: id, Tool: d.Name, At: m.at,
		Target: harness.Target(d.State.Input, "command", "filePath", "path", "pattern", "query", "url", "description", "prompt")})
	if d.State.Status == "completed" || d.State.Status == "error" {
		log.Done(id)
	}
}

// readSession reads one session's row and newest messages.
func readSession(db *sql.DB, id string) (load, bool) {
	r, err := scanRow(db.QueryRow(`SELECT `+rowColumns+` FROM session_v2 WHERE id = ?`, id))
	if err != nil {
		return load{}, false
	}
	ms, err := messages(db, id)
	if err != nil {
		return load{}, false
	}
	ps, err := parts(db, id)
	if err != nil {
		return load{}, false
	}
	l := load{row: r, log: &harness.TurnLog{}, wrote: time.UnixMilli(r.updated)}
	for i := len(ms) - 1; i >= 0; i-- { // oldest first
		m := ms[i]
		switch m.kind {
		case "user":
			var d struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(m.data), &d) == nil {
				l.log.Human(d.Text, m.at)
				l.marked = true
			}
		case "assistant":
			l.marked = true
			for _, x := range ps[m.id] {
				var d partData
				if json.Unmarshal([]byte(x.data), &d) != nil {
					continue
				}
				switch x.kind {
				case "text":
					l.log.Answer(d.Text, m.at)
				case "tool":
					step(l.log, m, x, d)
				}
			}
		}
	}
	return l, true
}
