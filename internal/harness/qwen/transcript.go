package qwen

import (
	"encoding/json"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The transcript is a tree: each record names its parent, and a rewind
// leaves dead branches in the file. So the walk starts at the newest record
// and follows the parents. Subagent records (isSidechain) stay out.

type transcript struct {
	log  *harness.TurnLog
	turn string // idle, busy, or "" when the end cannot say
	at   time.Time
}

type part struct {
	Text         string          `json:"text"`
	Thought      bool            `json:"thought"`
	FunctionCall *functionCall   `json:"functionCall"`
	Response     *responseCallID `json:"functionResponse"`
}

type functionCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type responseCallID struct {
	ID string `json:"id"`
}

type chatRecord struct {
	UUID        string    `json:"uuid"`
	Parent      string    `json:"parentUuid"`
	Timestamp   time.Time `json:"timestamp"`
	Type        string    `json:"type"`
	Subtype     string    `json:"subtype"`
	IsSidechain bool      `json:"isSidechain"`
	Message     struct {
		Parts []part `json:"parts"`
	} `json:"message"`
	ToolCallResult struct {
		CallID string `json:"callId"`
	} `json:"toolCallResult"`
	SystemPayload json.RawMessage `json:"systemPayload"`
}

// readChain reads the active branch, oldest record first.
func readChain(path string) []chatRecord {
	byID := map[string]chatRecord{}
	var leaf string
	_ = harness.ScanTail(path, func(line []byte) {
		var r chatRecord
		if json.Unmarshal(line, &r) != nil || r.UUID == "" || r.IsSidechain {
			return
		}
		byID[r.UUID] = r
		if r.Type != "system" || r.Subtype == "turn_result" {
			leaf = r.UUID
		}
	})
	var back []chatRecord
	for id := leaf; id != ""; {
		r, ok := byID[id]
		if !ok {
			break // the parent lies before the part of the file read
		}
		delete(byID, id) // a loop in the file ends the walk
		back = append(back, r)
		id = r.Parent
	}
	out := make([]chatRecord, len(back))
	for i, r := range back {
		out[len(back)-1-i] = r
	}
	return out
}

func prompt(r chatRecord) bool {
	return r.Subtype == "" || r.Subtype == "mid_turn_user_message"
}

func text(parts []part) string {
	s := ""
	for _, p := range parts {
		if !p.Thought {
			s += p.Text
		}
	}
	return s
}

func target(raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	return harness.Target(args, "command", "file_path", "absolute_path", "path", "pattern", "query", "url", "description", "prompt")
}

// readTranscript walks the active branch and reads the state from its end
// (an inference from the source). A turn_result record is decisive, but
// only the ACP path writes one. In the TUI an answer with text and no tool
// call is the end of a turn.
func readTranscript(path string) transcript {
	t := transcript{log: &harness.TurnLog{}}
	last := ""
	for _, r := range readChain(path) {
		t.at = r.Timestamp
		switch r.Type {
		case "user":
			last = "other"
			if prompt(r) {
				if s := text(r.Message.Parts); s != "" {
					t.log.Human(s, r.Timestamp)
					last = "working"
				}
			}
		case "assistant":
			last = "other"
			for _, p := range r.Message.Parts {
				switch {
				case p.FunctionCall != nil:
					t.log.Call(harness.Step{ID: p.FunctionCall.ID, Tool: p.FunctionCall.Name, Target: target(p.FunctionCall.Args), At: r.Timestamp})
					last = "working"
				case !p.Thought && p.Text != "":
					t.log.Answer(p.Text, r.Timestamp)
					last = "answered"
				}
			}
		case "tool_result":
			last = "working"
			if r.ToolCallResult.CallID != "" {
				t.log.Done(r.ToolCallResult.CallID)
			}
			for _, p := range r.Message.Parts {
				if p.Response != nil && p.Response.ID != "" {
					t.log.Done(p.Response.ID)
				}
			}
		case "system":
			if r.Subtype == "turn_result" {
				var end struct {
					State string `json:"state"`
				}
				if json.Unmarshal(r.SystemPayload, &end) == nil && end.State != "" {
					last = "ended"
				}
			}
		}
	}
	_, act := t.log.Result(1)
	switch {
	case last == "ended":
		t.turn = "idle"
	case act.Pending != nil:
		t.turn = ""
	case last == "working":
		t.turn = "busy"
	case last == "answered":
		t.turn = "idle"
	}
	return t
}
