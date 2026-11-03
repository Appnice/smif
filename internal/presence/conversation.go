package presence

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Turn is one step of an interactive session's conversation, read from its
// transcript: the human's prompt ("you") or Claude's answer ("claude"). An
// answer holds only its final message, the text after its last tool call
// ("" while it still works), and counts its tool calls.
type Turn struct {
	Role  string    `json:"role"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	Tools int       `json:"tools,omitempty"`
	// longest is the longest text before a tool call in this answer: the
	// real answer when Claude answers, then saves a note and ends with a
	// short "Saved." (see finalText).
	longest string
}

// shortFinal is a final message so short that an earlier, longer text of
// the same answer is likely the real one.
const shortFinal = 80

// finalText is the answer to show: the final message, with the longest
// earlier text in front when the final one is short and that one is at
// least three times longer.
func (t Turn) finalText() string {
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

// LastAnswer is the final message of the newest answer and when it came
// ("" when the newest answer has no text yet).
func LastAnswer(turns []Turn) (string, time.Time) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "claude" {
			return turns[i].Text, turns[i].At
		}
	}
	return "", time.Time{}
}

// Conversation reads the last limit turns of a session from its
// transcript. Tool results, subagent (sidechain) records, meta records and
// system text (it starts with "<", such as <system-reminder>) are left out.
func Conversation(path string, limit int) []Turn {
	if path == "" || filepath.Ext(path) != ".jsonl" || limit <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > conversationTail {
		if _, err := f.Seek(st.Size()-conversationTail, io.SeekStart); err != nil {
			return nil
		}
	}
	var turns []Turn
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
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(rec.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					text = append(text, b.Text)
				case "tool_use":
					tools++
					if j := strings.TrimSpace(strings.Join(text, "\n\n")); len([]rune(j)) > len([]rune(pre)) && !strings.HasPrefix(j, "<") {
						pre = j
					}
					text, reset = nil, true
				}
			}
		}
		var keep []string
		for _, t := range text {
			if t = strings.TrimSpace(t); t != "" && !strings.HasPrefix(t, "<") {
				keep = append(keep, t)
			}
		}
		body := strings.Join(keep, "\n\n")
		if rec.Type == "user" {
			if body != "" {
				turns = append(turns, Turn{Role: "you", Text: body, At: rec.Timestamp})
			}
			continue
		}
		if body == "" && tools == 0 {
			continue
		}
		// Claude's answer spans several records (text, tool calls, more
		// text): one turn until the human speaks again.
		if n := len(turns); n > 0 && turns[n-1].Role == "claude" {
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
		turns = append(turns, Turn{Role: "claude", Text: body, At: rec.Timestamp, Tools: tools, longest: pre})
	}
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	for i := range turns {
		if turns[i].Role == "claude" {
			turns[i].Text = turns[i].finalText()
		}
		if r := []rune(turns[i].Text); len(r) > maxTurnRunes {
			turns[i].Text = "…" + string(r[len(r)-maxTurnRunes+1:])
		}
	}
	return turns
}
