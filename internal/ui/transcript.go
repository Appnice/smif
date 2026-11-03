package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// streamEvent is the part of a `claude -p --output-format stream-json`
// line that the transcript view reads.
type streamEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	Model     string  `json:"model"`
	Version   string  `json:"claude_code_version"`
	Tools     []any   `json:"tools"`
	IsError   bool    `json:"is_error"`
	Result    string  `json:"result"`
	NumTurns  int     `json:"num_turns"`
	CostUSD   float64 `json:"total_cost_usd"`
	Duration  int64   `json:"duration_ms"`
	Usage     struct {
		In  int64 `json:"input_tokens"`
		Out int64 `json:"output_tokens"`
	} `json:"usage"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	ToolUseID string          `json:"tool_use_id"`
	ID        string          `json:"id"`
}

// toolStep is one tool call of a worker, for the live card on the home
// screen: done once its result arrived.
type toolStep struct {
	Name, Arg string
	Done, Err bool
}

// toolTrail lists the last n tool calls in a stream-JSON transcript.
func toolTrail(data []byte, n int) []toolStep {
	var steps []toolStep
	at := map[string]int{}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		var ev streamEvent
		if json.Unmarshal(bytes.TrimSpace(raw), &ev) != nil {
			continue
		}
		if ev.Type != "assistant" && ev.Type != "user" {
			continue
		}
		for _, b := range blocks(ev.Message.Content) {
			switch b.Type {
			case "tool_use":
				at[b.ID] = len(steps)
				steps = append(steps, toolStep{Name: b.Name, Arg: oneLine(toolArg(b.Input))})
			case "tool_result":
				if i, ok := at[b.ToolUseID]; ok {
					steps[i].Done, steps[i].Err = true, b.IsError
				}
			}
		}
	}
	if len(steps) > n {
		steps = steps[len(steps)-n:]
	}
	return steps
}

// RenderTranscript turns stream-JSON lines into readable lines of at most
// w cells: the init event, assistant text, tool uses with their main
// argument, tool results (first line), and the final result with turns,
// cost and tokens. A line that is not JSON shows as it is, dimmed.
func RenderTranscript(data []byte, w int) []string {
	w = max(30, w)
	var out []string
	add := func(s string) { out = append(out, s) }
	wrap := func(indent string, st *lipgloss.Style, s string) {
		s = strings.TrimRight(s, "\n")
		for _, l := range strings.Split(ansi.Wrap(s, w-len(indent), ""), "\n") {
			if st != nil {
				l = st.Render(l)
			}
			add(indent + l)
		}
	}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var ev streamEvent
		if json.Unmarshal(raw, &ev) != nil || ev.Type == "" {
			wrap("  ", &stDim, string(raw))
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				add(stDim.Render(fmt.Sprintf("  init · session %s · model %s · claude %s · %d tools",
					dash(ev.SessionID), dash(ev.Model), dash(ev.Version), len(ev.Tools))))
			} else {
				add(stDim.Render("  system " + dash(ev.Subtype)))
			}
		case "assistant", "user":
			for _, b := range blocks(ev.Message.Content) {
				switch b.Type {
				case "text":
					if ev.Type == "assistant" {
						add("  " + stAssistant.Render("assistant"))
					} else {
						add("  " + stTitle.Render("user"))
					}
					wrap("      ", nil, b.Text)
				case "thinking":
					add("  " + stDim.Render(fmt.Sprintf("thinking (%d chars)", len(b.Thinking))))
				case "tool_use":
					wrap("  ", nil, stTool.Render("tool "+b.Name)+"  "+toolArg(b.Input))
				case "tool_result":
					text, n := resultText(b.Content)
					mark := "→ "
					st := &stDim
					if b.IsError {
						mark, st = "→ ERROR ", &stBad
					}
					if n > 1 {
						text += fmt.Sprintf("  (%d lines)", n)
					}
					wrap("      ", st, mark+text)
				default:
					add(stDim.Render("  " + ev.Type + " " + b.Type))
				}
			}
		case "result":
			head := fmt.Sprintf("result %s · %d turns · %s · in %d / out %d tokens · %dms",
				dash(ev.Subtype), ev.NumTurns, money4(ev.CostUSD), ev.Usage.In, ev.Usage.Out, ev.Duration)
			if ev.IsError {
				add("  " + stBad.Render(head+" · ERROR"))
			} else {
				add("  " + stOK.Render(head))
			}
			if ev.Result != "" {
				wrap("      ", nil, ev.Result)
			}
		default:
			add(stDim.Render("  " + ev.Type + " event"))
		}
	}
	if len(out) == 0 {
		out = append(out, stDim.Render("  (the transcript is empty)"))
	}
	return out
}

func blocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}
	var bs []contentBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

// toolArg is the main argument of a tool call: a command, a path, a
// pattern, or else the input JSON cut to 120 characters.
func toolArg(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || len(in) == 0 {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if v, ok := in[k].(string); ok && v != "" {
			return cut(oneLine(v), 120)
		}
	}
	b, _ := json.Marshal(in) // map keys come out sorted
	return cut(string(b), 120)
}

// resultText is the first non-empty line of a tool result and its line count.
func resultText(raw json.RawMessage) (string, int) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var parts []contentBlock
		_ = json.Unmarshal(raw, &parts)
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
		s = b.String()
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "(no output)", 0
	}
	return cut(lines[0], 160), len(lines)
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func money4(f float64) string { return fmt.Sprintf("$%.4f", f) }

// RenderDiff colours a unified diff: file headers bold, + green, - red,
// hunk headers cyan. Lines are cut, not wrapped, at w cells.
func RenderDiff(diff string, w int) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		l = ansi.Truncate(l, max(10, w-2), "…")
		switch {
		case strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "), strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"),
			strings.HasPrefix(l, "new file"), strings.HasPrefix(l, "deleted file"):
			l = stTitle.Render(l)
		case strings.HasPrefix(l, "@@"):
			l = stHunk.Render(l)
		case strings.HasPrefix(l, "+"):
			l = stAdd.Render(l)
		case strings.HasPrefix(l, "-"):
			l = stDel.Render(l)
		}
		out = append(out, "  "+l)
	}
	return out
}
