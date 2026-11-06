package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// answerTurn is a turn while the transcript is read. longest is the longest
// text before a tool call in this answer: the real answer when Claude
// answers, then saves a note and ends with a short "Saved." (see finalText).
type answerTurn struct {
	harness.Turn
	longest string
}

// shortFinal is a final message so short that an earlier, longer text of
// the same answer is likely the real one.
const shortFinal = 80

// finalText is the answer to show: the final message, with the longest
// earlier text in front when the final one is short and that one is at
// least three times longer.
func (t answerTurn) finalText() string {
	f := []rune(t.Text)
	if len(f) < shortFinal && len([]rune(t.longest)) >= 3*max(len(f), 20) {
		if t.Text == "" {
			return t.longest
		}
		return t.longest + "\n\n" + t.Text
	}
	return t.Text
}

// conversationTail is how much of the transcript end Conversation reads.
const conversationTail = 2 << 20

// maxTurnRunes caps the text of one turn.
const maxTurnRunes = 6000

// readTranscript reads the last limit turns of a session from its
// transcript, and the newest exchange's activity. Tool results, subagent
// (sidechain) records, meta records and system text (it starts with "<",
// such as <system-reminder>) are left out.
func readTranscript(path string, limit int) ([]harness.Turn, harness.Activity) {
	var act harness.Activity
	if path == "" || filepath.Ext(path) != ".jsonl" || limit <= 0 {
		return nil, act
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, act
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > conversationTail {
		if _, err := f.Seek(st.Size()-conversationTail, io.SeekStart); err != nil {
			return nil, act
		}
	}
	var turns []answerTurn
	var steps []harness.Step
	done := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type             string    `json:"type"`
			Timestamp        time.Time `json:"timestamp"`
			IsSidechain      bool      `json:"isSidechain"`
			IsMeta           bool      `json:"isMeta"`
			IsCompactSummary bool      `json:"isCompactSummary"`
			Message          struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.IsSidechain || rec.IsMeta || rec.IsCompactSummary || (rec.Type != "user" && rec.Type != "assistant") {
			continue
		}
		// Blocks in order. For an answer only the text after the last tool
		// call counts: that is the final message; notes between tool calls
		// ("Checking…") are steps, not the answer.
		var text []string
		pre := "" // the longest text before a tool call in this record
		tools, reset := 0, false
		var s string
		if json.Unmarshal(rec.Message.Content, &s) == nil {
			text = append(text, s)
		} else {
			var blocks []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
			}
			if json.Unmarshal(rec.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					text = append(text, b.Text)
				case "tool_result":
					done[b.ToolUseID] = true
				case "tool_use":
					if rec.Type == "assistant" {
						steps = append(steps, harness.Step{ID: b.ID, Tool: toolName(b.Name), Target: stepTarget(b.Name, b.Input), At: rec.Timestamp})
					}
					tools++
					if j := strings.TrimSpace(strings.Join(text, "\n\n")); len([]rune(j)) > len([]rune(pre)) && !strings.HasPrefix(j, "<") {
						pre = j
					}
					text, reset = nil, true
				}
			}
		}
		body := plainText(text)
		if rec.Type == "user" {
			if body != "" {
				turns = append(turns, answerTurn{Turn: harness.Turn{Role: harness.RoleHuman, Text: body, At: rec.Timestamp}})
				act = harness.Activity{Prompt: body, PromptAt: rec.Timestamp}
				steps, done = nil, map[string]bool{}
			}
			continue
		}
		if body == "" && tools == 0 {
			continue
		}
		// Claude's answer spans several records (text, tool calls, more
		// text): one turn until the human speaks again.
		if n := len(turns); n > 0 && turns[n-1].Role == harness.RoleAgent {
			t := &turns[n-1]
			if reset {
				if len([]rune(t.Text)) > len([]rune(t.longest)) {
					t.longest = t.Text
				}
				t.Text = body
			} else if body != "" {
				t.Text = strings.TrimSpace(t.Text + "\n\n" + body)
			}
			if len([]rune(pre)) > len([]rune(t.longest)) {
				t.longest = pre
			}
			t.Tools += tools
			t.At = rec.Timestamp
			continue
		}
		turns = append(turns, answerTurn{Turn: harness.Turn{Role: harness.RoleAgent, Text: body, At: rec.Timestamp, Tools: tools}, longest: pre})
	}
	act.Tools = len(steps)
	for i := range steps {
		steps[i].Done = done[steps[i].ID]
		if !steps[i].Done && act.Pending == nil {
			p := steps[i]
			act.Pending = &p
		}
	}
	if len(steps) > harness.MaxSteps {
		steps = steps[len(steps)-harness.MaxSteps:]
	}
	act.Steps = steps
	if r := []rune(act.Prompt); len(r) > maxTurnRunes {
		act.Prompt = string(r[:maxTurnRunes-1]) + "…"
	}
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	out := make([]harness.Turn, len(turns))
	for i, t := range turns {
		out[i] = t.Turn
		if t.Role == harness.RoleAgent {
			out[i].Text = t.finalText()
		}
		if r := []rune(out[i].Text); len(r) > maxTurnRunes {
			out[i].Text = "…" + string(r[len(r)-maxTurnRunes+1:])
		}
	}
	return out, act
}

// plainText joins a record's text blocks, without empty ones and system
// text (it starts with "<"). In a user record, text means a human prompt.
func plainText(blocks []string) string {
	var keep []string
	for _, t := range blocks {
		if t = strings.TrimSpace(t); t != "" && !strings.HasPrefix(t, "<") {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "\n\n")
}

// toolName is a tool's short name: an MCP tool (mcp__server__tool) loses
// its server prefix.
func toolName(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		if i := strings.LastIndex(name, "__"); i > 0 {
			return name[i+2:]
		}
	}
	return name
}

// maxTargetRunes caps a step's target.
const maxTargetRunes = 300

// stepTarget is what a tool call works on, one line: the command, the
// file, the pattern, the question.
func stepTarget(name string, input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	pick := ""
	switch name {
	case "AskUserQuestion":
		if qs, ok := in["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				pick, _ = q["question"].(string)
			}
		}
	case "ExitPlanMode":
		pick = "a plan to approve"
	case "TodoWrite":
		pick = "the to-do list"
	}
	for _, k := range []string{"command", "file_path", "notebook_path", "path", "pattern", "url", "query", "description", "skill", "prompt"} {
		if pick != "" {
			break
		}
		if v, ok := in[k].(string); ok {
			pick = strings.TrimSpace(v)
		}
	}
	if i := strings.IndexByte(pick, '\n'); i >= 0 {
		pick = strings.TrimSpace(pick[:i]) + " …"
	}
	if r := []rune(pick); len(r) > maxTargetRunes {
		pick = string(r[:maxTargetRunes-1]) + "…"
	}
	return pick
}
