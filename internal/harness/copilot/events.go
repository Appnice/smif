package copilot

import (
	"encoding/json"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// events.jsonl is one JSON record per line: {type, data, id, timestamp,
// parentId}. The fields come from Copilot's event schema at 1.0.60, which
// its docs do not otherwise describe. Records marked ephemeral never reach
// the file, among them session.idle and the requests that ask the human a
// question, so a question looks the same as a running tool. Copilot's turn
// is one model call: a turn whose message asks for tools ends before those
// tools' next turn starts, so only a turn with no tool request can end the
// human's exchange.

type transcript struct {
	log    *harness.TurnLog
	turn   string // idle, busy, waiting, or "" when the end cannot say
	at     time.Time
	cwd    string        // the session's newest folder
	asked  *harness.Step // the permission that waits
	marked bool          // a session or turn record was seen
}

// event is the part of a record smif uses. data is one struct: each type
// fills its own fields.
type event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Ephemeral bool      `json:"ephemeral"`
	Data      struct {
		Content      string `json:"content"`
		ToolRequests []any  `json:"toolRequests"`
		Cwd          string `json:"cwd"`
		Context      struct {
			Cwd string `json:"cwd"`
		} `json:"context"`
		ToolCallID       string         `json:"toolCallId"`
		ParentToolCallID string         `json:"parentToolCallId"`
		ToolName         string         `json:"toolName"`
		Arguments        map[string]any `json:"arguments"`
		RequestID        string         `json:"requestId"`
		Permission       map[string]any `json:"permissionRequest"`
	} `json:"data"`
}

func argTarget(args map[string]any) string {
	return harness.Target(args, "command", "fullCommandText", "path", "fileName", "pattern", "query", "url", "description", "prompt")
}

// walk is one pass over an event log.
type walk struct {
	t      *transcript
	tools  map[string]harness.Step // calls that run
	asks   map[string]harness.Step // permissions that wait, by request id
	open   bool                    // the human's exchange runs
	more   bool                    // the newest message asked for tools
	ended  bool
	lastAt time.Time
}

// clear forgets what a process left open: it ended, or a new one took over.
func (w *walk) clear() {
	w.tools, w.asks = map[string]harness.Step{}, map[string]harness.Step{}
	w.open, w.more = false, false
	w.t.log.Exchange()
}

func (w *walk) event(e event) {
	d := &e.Data
	switch e.Type {
	case "session.start", "session.resume":
		w.clear()
		w.ended = false
		w.t.marked = true
		if d.Context.Cwd != "" {
			w.t.cwd = d.Context.Cwd
		}
	case "session.context_changed":
		if d.Cwd != "" {
			w.t.cwd = d.Cwd
		}
	case "user.message":
		w.t.log.Human(d.Content, e.Timestamp)
		w.open, w.more = true, false
	case "assistant.turn_start":
		w.open, w.more = true, false
		w.t.marked = true
	case "assistant.turn_end":
		w.open = w.more
		w.t.marked = true
	case "assistant.message":
		if d.ParentToolCallID != "" {
			return
		}
		w.more = len(d.ToolRequests) > 0
		if !w.more {
			w.t.log.Answer(d.Content, e.Timestamp)
		}
	case "tool.execution_start":
		s := harness.Step{ID: d.ToolCallID, Tool: d.ToolName, Target: argTarget(d.Arguments), At: e.Timestamp}
		w.tools[s.ID] = s
		if d.ParentToolCallID == "" {
			w.t.log.Call(s)
		}
	case "tool.execution_complete":
		delete(w.tools, d.ToolCallID)
		w.t.log.Done(d.ToolCallID)
	case "permission.requested":
		s, ok := w.tools[d.ToolCallID]
		if !ok {
			kind, _ := d.Permission["kind"].(string)
			s = harness.Step{ID: d.ToolCallID, Tool: kind, Target: argTarget(d.Permission), At: e.Timestamp}
			if s.Tool == "" {
				s.Tool = "permission"
			}
		}
		w.asks[d.RequestID] = s
	case "permission.completed":
		delete(w.asks, d.RequestID)
	case "abort":
		w.clear()
	case "session.shutdown":
		w.clear()
		w.ended = true
	}
}

// finish gives the state of the log's end. Only a closed exchange with
// nothing open is idle, and an open call is unknown: it can be a question.
func (w *walk) finish() {
	t := w.t
	t.at = w.lastAt
	for _, s := range w.asks {
		if t.asked == nil || s.At.Before(t.asked.At) {
			c := s
			t.asked = &c
		}
	}
	switch {
	case !t.marked || w.ended:
	case t.asked != nil:
		t.turn = "waiting"
	case len(w.tools) > 0:
	case w.open:
		t.turn = "busy"
	case w.lastTurnClosed():
		t.turn = "idle"
	}
}

// lastTurnClosed says an exchange ran and came to its end, so that a new
// session with no prompt yet gives no state.
func (w *walk) lastTurnClosed() bool {
	turns, _ := w.t.log.Result(1)
	return len(turns) > 0
}

// readTranscript walks an event log.
func readTranscript(path string) transcript {
	t := transcript{log: &harness.TurnLog{}}
	w := walk{t: &t}
	w.clear()
	_ = harness.ScanTail(path, func(line []byte) {
		var e event
		if json.Unmarshal(line, &e) != nil || e.Ephemeral {
			return
		}
		w.lastAt = e.Timestamp
		w.event(e)
	})
	w.finish()
	return t
}
