package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/appnice/smif/internal/harness"
)

// factsTail caps the first read of a transcript: the largest real ones
// pass 1 GB. A newest exchange longer than this undercounts Files and
// Commits.
const factsTail = 16 << 20

// factCall is a tool call that waits for its result: the result says
// whether the edit, the commit or the test happened.
type factCall struct {
	file         string // an edit tool's file
	commit, test bool
	seq          int // the call's place in the exchange
}

// factCursor is what Facts read of one transcript. The fields after offset
// hold from the newest human prompt on, except tokens.
type factCursor struct {
	path   string
	offset int64 // the end of the last whole line read

	files   map[string]bool
	commits int
	calls   map[string]factCall // tool id → the call with no result yet
	seq     int
	editSeq int    // the last edit of a non-doc file, 0 = none
	testSeq int    // the last test command with a result, 0 = none
	tested  string // that test's result
	tokens  int64
}

// prompt starts a new exchange.
func (c *factCursor) prompt() {
	c.files, c.commits, c.calls = nil, 0, nil
	c.seq, c.editSeq, c.testSeq, c.tested = 0, 0, 0, ""
}

func (c *factCursor) facts() harness.Facts {
	f := harness.Facts{Files: len(c.files), Commits: c.commits, ContextTokens: c.tokens}
	switch {
	case c.editSeq == 0:
	case c.testSeq > c.editSeq:
		f.Tests = c.tested
	default:
		f.Tests = harness.TestsNone
	}
	return f
}

// Facts gives the transcript's facts. It parses only the whole lines added
// since the last call. A shorter file, or a transcript at a new path, is
// read again from the start.
func (p *Provider) Facts(sessionID string, isTest func(string) bool) (harness.Facts, bool) {
	path := p.transcriptPath(sessionID)
	if path == "" {
		return harness.Facts{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return harness.Facts{}, false
	}
	if isTest == nil {
		isTest = func(s string) bool { return harness.IsTestCommand(s, nil) }
	}
	p.factsMu.Lock()
	defer p.factsMu.Unlock()
	c := p.cursors[sessionID]
	if c == nil || c.path != path || st.Size() < c.offset {
		if len(p.cursors) > 512 {
			p.cursors = map[string]*factCursor{}
		}
		c = &factCursor{path: path}
		p.cursors[sessionID] = c
	}
	if st.Size() > c.offset {
		p.parsed += c.advance(st.Size(), isTest)
	}
	return c.facts(), true
}

// advance parses the whole lines after the offset and gives how many bytes
// it parsed. A last line with no newline waits for the next call: the
// agent still writes it.
func (c *factCursor) advance(size int64, isTest func(string) bool) int64 {
	f, err := os.Open(c.path)
	if err != nil {
		return 0
	}
	defer f.Close()
	at, cut := c.offset, false
	if at == 0 && size > factsTail {
		at, cut = size-factsTail, true
	}
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return 0
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var parsed int64
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		at += int64(len(line))
		if cut {
			cut = false // a line that starts before the cut
			continue
		}
		parsed += int64(len(line))
		c.record(line, isTest)
	}
	c.offset = at
	return parsed
}

// factRecord is the part of a transcript record that Facts reads.
type factRecord struct {
	Type             string `json:"type"`
	IsSidechain      bool   `json:"isSidechain"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	PromptSource     string `json:"promptSource"`
	Origin           struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Attachment    struct {
		Type        string `json:"type"`
		CommandMode string `json:"commandMode"`
		Origin      struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	} `json:"attachment"`
	Message struct {
		Content json.RawMessage `json:"content"`
		Usage   struct {
			Input         int64 `json:"input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// interruptMark starts the user record that Claude Code writes when a
// human stops a turn. The exchange goes on: the facts stay for the review.
const interruptMark = "[Request interrupted"

// fromHuman says that Claude Code marks the record as a prompt. The mark
// also holds for a prompt with no plain text: a paste or an image alone.
// promptSource `system` is a task notification or a peer's message.
func (r *factRecord) fromHuman() bool {
	switch r.PromptSource {
	case "typed", "queued", "suggestion_accepted", "sdk":
		return true
	}
	return r.Origin.Kind == "human"
}

type factBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (c *factCursor) record(line []byte, isTest func(string) bool) {
	var rec factRecord
	if line[0] != '{' || json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.IsSidechain || rec.IsMeta || rec.IsCompactSummary {
		return
	}
	var blocks []factBlock
	var s string
	if json.Unmarshal(rec.Message.Content, &s) == nil {
		blocks = []factBlock{{Type: "text", Text: s}}
	} else {
		_ = json.Unmarshal(rec.Message.Content, &blocks)
	}
	switch rec.Type {
	case "assistant":
		// A record with no usage (an error text) says nothing of the context.
		u := rec.Message.Usage
		if n := u.Input + u.CacheRead + u.CacheCreation; n > 0 {
			c.tokens = n
		}
		for i := range blocks {
			if blocks[i].Type == "tool_use" {
				c.call(&blocks[i], isTest)
			}
		}
	case "user":
		var text []string
		human := rec.fromHuman()
		for i := range blocks {
			switch blocks[i].Type {
			case "text":
				if strings.HasPrefix(strings.TrimSpace(blocks[i].Text), interruptMark) {
					human = false
				} else {
					text = append(text, blocks[i].Text)
				}
			case "tool_result":
				c.result(&blocks[i], rec.ToolUseResult)
			}
		}
		// A record with no mark keeps the plain text rule.
		if human || plainText(text) != "" {
			c.prompt()
		}
	case "attachment":
		// A prompt typed while the session works. It writes no user record.
		a := &rec.Attachment
		if a.Type == "queued_command" && a.CommandMode == "prompt" && (a.Origin.Kind == "human" || a.Origin.Kind == "") {
			c.prompt()
		}
	}
}

// call notes a tool call that can become a fact.
func (c *factCursor) call(b *factBlock, isTest func(string) bool) {
	c.seq++
	fc := factCall{seq: c.seq}
	var in struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	switch b.Name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		_ = json.Unmarshal(b.Input, &in)
		fc.file = in.FilePath
		if fc.file == "" {
			fc.file = in.NotebookPath
		}
	case "Bash":
		_ = json.Unmarshal(b.Input, &in)
		fc.commit, fc.test = harness.IsCommit(in.Command), isTest(in.Command)
	}
	if fc.file == "" && !fc.commit && !fc.test {
		return
	}
	if c.calls == nil {
		c.calls = map[string]factCall{}
	}
	c.calls[b.ID] = fc
}

// result turns a call into facts. A refused edit or commit did not happen.
func (c *factCursor) result(b *factBlock, toolUse json.RawMessage) {
	fc, ok := c.calls[b.ToolUseID]
	if !ok {
		return
	}
	delete(c.calls, b.ToolUseID)
	if fc.file != "" && !b.IsError {
		if c.files == nil {
			c.files = map[string]bool{}
		}
		c.files[fc.file] = true
		if !harness.IsDocFile(fc.file) {
			c.editSeq = max(c.editSeq, fc.seq)
		}
	}
	if fc.commit && !b.IsError {
		c.commits++
	}
	if fc.test && fc.seq > c.testSeq {
		if r := testOutcome(b, toolUse); r != "" {
			c.testSeq, c.tested = fc.seq, r
		}
	}
}

// failRe finds a failure in a test's output with exit status 0: a pipe
// such as `go test ./... | tail` hides the status.
var failRe = regexp.MustCompile(`(?m)^\s*(?:--- )?FAIL\b|\bFAILED\b|^make(?:\[\d+\])?: \*\*\*|\b[1-9]\d* (?:failed|failing)\b`)

// testOutcome reads a test command's result: passed, failed, or "" when the
// test did not finish. A denied or interrupted call has an error text
// other than "Exit code N" (docs/compat.md), and a call sent to the
// background returns before its test ends.
func testOutcome(b *factBlock, toolUse json.RawMessage) string {
	var bg struct {
		ID string `json:"backgroundTaskId"`
	}
	if json.Unmarshal(toolUse, &bg) == nil && bg.ID != "" {
		return ""
	}
	text := resultText(b.Content)
	switch {
	case b.IsError && (strings.HasPrefix(text, "Exit code") || strings.HasPrefix(text, "Command timed out")):
		return harness.TestsFailed
	case b.IsError:
		return ""
	case failRe.MatchString(text):
		return harness.TestsFailed
	}
	return harness.TestsPassed
}

// resultText is a tool result's text: a string or a list of text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var out []string
	for _, b := range blocks {
		out = append(out, b.Text)
	}
	return strings.Join(out, "\n")
}
