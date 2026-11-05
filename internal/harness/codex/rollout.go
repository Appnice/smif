package codex

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// A rollout is one JSON line per record: {timestamp, type, payload}. Two
// history modes write it. Legacy threads keep event_msg user_message and
// agent_message. Paginated threads, the default for new ones, drop those and
// keep event_msg item_completed. Both keep task_started, task_complete,
// turn_aborted and response_item, so a reader must take both prompt forms.
// A response_item with role user also carries injected context, so it does
// not count as a prompt.

type transcript struct {
	log    *harness.TurnLog
	turn   string // idle, busy, or "" when the end cannot say
	at     time.Time
	marked bool // a turn record was seen
}

type content struct {
	Text string `json:"text"`
}

func joined(c []content) string {
	s := ""
	for _, p := range c {
		s += p.Text
	}
	return s
}

// event is the part of an event_msg smif uses, decoded by its type.
type event struct {
	Type             string  `json:"type"`
	Message          string  `json:"message"`
	LastAgentMessage *string `json:"last_agent_message"`
	Item             struct {
		Type    string    `json:"type"`
		Content []content `json:"content"`
	} `json:"item"`
}

// item is the part of a response_item smif uses.
type item struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   []content       `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Action    json.RawMessage `json:"action"`
}

// commandText is a command as one line: an argv array loses the shell
// wrapper in front of it.
func commandText(v any) string {
	list, ok := v.([]any)
	if !ok {
		s, _ := v.(string)
		return s
	}
	var words []string
	for _, w := range list {
		if s, ok := w.(string); ok {
			words = append(words, s)
		}
	}
	if len(words) == 3 && (words[1] == "-lc" || words[1] == "-c") {
		return words[2]
	}
	return strings.Join(words, " ")
}

func callTarget(args string) string {
	var in map[string]any
	if json.Unmarshal([]byte(args), &in) != nil {
		return ""
	}
	if s := commandText(in["command"]); s != "" {
		return harness.ClipTarget(s)
	}
	return harness.Target(in, "cmd", "path", "file_path", "pattern", "query", "url", "description", "prompt")
}

// patchTarget is the first file of an apply_patch input, else its first line.
func patchTarget(input string) string {
	if _, rest, ok := strings.Cut(input, " File: "); ok {
		input = rest
	}
	return harness.ClipTarget(input)
}

func shellTarget(action json.RawMessage) string {
	var a struct {
		Command any `json:"command"`
	}
	if json.Unmarshal(action, &a) != nil {
		return ""
	}
	return harness.ClipTarget(commandText(a.Command))
}

// readTranscript walks a rollout. The state comes from its last turn
// marker: task_complete or turn_aborted is idle. An open turn is busy, and
// unknown with a tool call and no result, unless the turn's approval policy
// is never and the call asks the human nothing.
func readTranscript(path string) transcript {
	t := transcript{log: &harness.TurnLog{}}
	open, policy := false, ""
	var started time.Time
	_ = harness.ScanTail(path, func(line []byte) {
		var l struct {
			Timestamp time.Time       `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			return
		}
		at := l.Timestamp
		switch l.Type {
		case "turn_context":
			var c struct {
				Policy json.RawMessage `json:"approval_policy"`
			}
			if json.Unmarshal(l.Payload, &c) == nil {
				policy = strings.Trim(string(c.Policy), `"`)
			}
		case "event_msg":
			var e event
			if json.Unmarshal(l.Payload, &e) != nil {
				return
			}
			switch e.Type {
			case "task_started", "turn_started":
				t.log.Exchange()
				open, started, t.marked = true, at, true
			case "user_message":
				t.log.Human(e.Message, at)
			case "agent_message":
				t.log.Answer(e.Message, at)
			case "item_completed":
				switch e.Item.Type {
				case "UserMessage":
					t.log.Human(joined(e.Item.Content), at)
				case "AgentMessage":
					t.log.Answer(joined(e.Item.Content), at)
				}
			case "task_complete", "turn_complete":
				if e.LastAgentMessage != nil {
					t.log.Answer(*e.LastAgentMessage, at)
				}
				open, t.at, t.marked = false, at, true
			case "turn_aborted":
				open, t.at, t.marked = false, at, true
			}
		case "response_item":
			var it item
			if json.Unmarshal(l.Payload, &it) != nil {
				return
			}
			switch it.Type {
			case "message":
				if it.Role == "assistant" {
					t.log.Answer(joined(it.Content), at)
				}
			case "function_call":
				t.log.Call(harness.Step{ID: it.CallID, Tool: it.Name, Target: callTarget(it.Arguments), At: at})
			case "custom_tool_call":
				t.log.Call(harness.Step{ID: it.CallID, Tool: it.Name, Target: patchTarget(it.Input), At: at})
			case "local_shell_call":
				t.log.Call(harness.Step{ID: it.CallID, Tool: "shell", Target: shellTarget(it.Action), At: at})
			case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
				t.log.Done(it.CallID)
			}
		}
	})
	_, act := t.log.Result(1)
	switch {
	case !t.marked:
	case !open:
		t.turn = "idle"
	case act.Pending == nil || policy == "never" && act.Pending.Tool != "request_user_input":
		t.turn, t.at = "busy", started
	default:
		t.at = started
	}
	return t
}
