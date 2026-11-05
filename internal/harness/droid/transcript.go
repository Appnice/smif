package droid

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// A transcript's first line is session_start. Then each `message` line wraps
// a {role, content} message with Anthropic-style blocks: text, tool_use, and
// tool_result in a later user message. The report rebuilt this example from
// the bundle's schema and Entire's comments, nobody captured it.

type transcript struct {
	log    *harness.TurnLog
	turn   string // idle, busy, or "" when the end cannot say
	at     time.Time
	marked bool // a message was seen
}

// stamp is a time as a string or as a number of ms.
type stamp struct{ time.Time }

// UnmarshalJSON takes both forms. Any other value is no time.
func (s *stamp) UnmarshalJSON(b []byte) error {
	if ms, err := strconv.ParseInt(string(b), 10, 64); err == nil {
		s.Time = time.UnixMilli(ms)
		return nil
	}
	if len(b) > 1 && b[0] == '"' {
		s.Time, _ = time.Parse(time.RFC3339Nano, string(bytes.Trim(b, `"`)))
	}
	return nil
}

// hidden marks a message the human does not see.
type hidden struct {
	IsUserVisible       *bool  `json:"isUserVisible"`
	HiddenFromUserViews bool   `json:"hiddenFromUserViews"`
	HookEventName       string `json:"hookEventName"`
}

func (h hidden) hides() bool {
	return h.IsUserVisible != nil && !*h.IsUserVisible || h.HiddenFromUserViews || h.HookEventName != ""
}

type block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
}

type line struct {
	Type      string `json:"type"`
	Timestamp stamp  `json:"timestamp"`
	hidden
	Message struct {
		Role string `json:"role"`
		hidden
		Content []block `json:"content"`
	} `json:"message"`
}

func toolTarget(in map[string]any) string {
	return harness.Target(in, "command", "file_path", "path", "pattern", "query", "url", "description", "prompt")
}

// readTranscript walks a transcript. The last visible message gives the
// state: from the user is busy, from the assistant with text is idle. A tool
// call with no result gives "".
func readTranscript(path string) transcript {
	t := transcript{log: &harness.TurnLog{}}
	last := ""
	_ = harness.ScanTail(path, func(b []byte) {
		var l line
		if json.Unmarshal(b, &l) != nil || l.Type != "message" {
			return
		}
		t.marked = true
		if l.hides() || l.Message.hides() {
			return
		}
		at := l.Timestamp.Time
		for _, c := range l.Message.Content {
			switch {
			case l.Message.Role == "user" && c.Type == "text":
				t.log.Human(c.Text, at)
				last = "user"
			case l.Message.Role == "user" && c.Type == "tool_result":
				t.log.Done(c.ToolUseID)
				last = "user"
			case l.Message.Role == "assistant" && c.Type == "text" && c.Text != "":
				t.log.Answer(c.Text, at)
				last = "text"
			case l.Message.Role == "assistant" && c.Type == "tool_use":
				t.log.Call(harness.Step{ID: c.ID, Tool: c.Name, Target: toolTarget(c.Input), At: at})
				last = "tool"
			}
		}
		if !at.IsZero() {
			t.at = at
		}
	})
	_, act := t.log.Result(1)
	switch {
	case act.Pending != nil:
	case last == "user" || last == "tool":
		t.turn = "busy"
	case last == "text":
		t.turn = "idle"
	}
	return t
}
