package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/appnice/smif/internal/harness"
)

// History and search (ADR-0021). A transcript can reach 1 GB, and most are
// SDK runs, so the index reads the head of each file, and the tail only of
// an interactive one. It keeps each row in memory until the file's size or
// time changes. Search reads the whole file, newest first, and the caller's
// context stops it.

const (
	histHead = 1 << 20   // the first prompt and the folder are in here
	histTail = 256 << 10 // the title and the last time are in here
	// histMaxLine is the longest record Search looks at. A longer one
	// (pasted images, big tool output) is passed over.
	histMaxLine = 16 << 20
	// histNameRunes caps a row's name.
	histNameRunes = 120
	// Search shows this many runes before and after the words.
	hitBefore, hitAfter = 40, 100
)

// histIDRe is a session file's name: Claude Code names them by UUID. Older
// subagent files (agent-*.jsonl) do not match. The id reaches argv, so a
// file with another name is not a session.
var histIDRe = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// histEntry is what the index keeps of one transcript.
type histEntry struct {
	path  string
	size  int64
	mtime time.Time
	row   harness.HistoryRow
	keep  bool // false: not a session to list (an SDK run, no folder)
}

// histRecord is the part of a transcript record the index and search use.
type histRecord struct {
	Type             string    `json:"type"`
	Entrypoint       string    `json:"entrypoint"`
	Cwd              string    `json:"cwd"`
	CustomTitle      string    `json:"customTitle"`
	AgentName        string    `json:"agentName"`
	Timestamp        time.Time `json:"timestamp"`
	IsSidechain      bool      `json:"isSidechain"`
	IsMeta           bool      `json:"isMeta"`
	IsCompactSummary bool      `json:"isCompactSummary"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// spoken reports a record that a person or the model wrote in the main
// conversation.
func (r *histRecord) spoken() bool {
	return (r.Type == "user" || r.Type == "assistant") && !r.IsSidechain && !r.IsMeta && !r.IsCompactSummary
}

// text is the record's plain text: never a tool call, a tool result or
// thinking.
func (r *histRecord) text() string {
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		return plainText([]string{s})
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return ""
	}
	var text []string
	for _, b := range blocks {
		if b.Type == "text" {
			text = append(text, b.Text)
		}
	}
	return plainText(text)
}

// histScan is what the head and tail of one file say.
type histScan struct {
	entrypoint, dir, title, agent, prompt string
	at                                    time.Time
}

// add reads one record.
func (s *histScan) add(line []byte) {
	var r histRecord
	if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &r) != nil {
		return
	}
	if s.entrypoint == "" {
		s.entrypoint = r.Entrypoint
	}
	if s.dir == "" {
		s.dir = r.Cwd
	}
	switch r.Type {
	case "custom-title":
		s.title = r.CustomTitle
	case "agent-name":
		s.agent = r.AgentName
	}
	if !r.spoken() {
		return
	}
	if !r.Timestamp.IsZero() {
		s.at = r.Timestamp
	}
	if s.prompt == "" && r.Type == "user" {
		if t := r.text(); !strings.HasPrefix(t, "[Request interrupted") {
			s.prompt = t
		}
	}
}

func (s *histScan) sdk() bool { return strings.HasPrefix(s.entrypoint, "sdk-") }

// countingReader counts what the index reads.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

// eachLine calls fn for each line until fn returns false. A line over
// histMaxLine is passed over, so one big record does not end the file.
func eachLine(r io.Reader, fn func(line []byte) bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	over := false
	for {
		part, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			if len(long)+len(part) > histMaxLine {
				long, over = long[:0], true
			} else if !over {
				long = append(long, part...)
			}
			continue
		}
		line := part
		if len(long) > 0 {
			long = append(long, part...)
			line = long
		}
		if !over && len(line) <= histMaxLine && !fn(bytes.TrimRight(line, "\r\n")) {
			return
		}
		long, over = long[:0], false
		if err != nil {
			return
		}
	}
}

// scanFile reads a transcript's head and, for a session to list, its tail.
func (p *Provider) scanFile(path string, size int64, mtime time.Time) histEntry {
	e := histEntry{path: path, size: size, mtime: mtime}
	f, err := os.Open(path)
	if err != nil {
		return e
	}
	defer f.Close()
	var s histScan
	eachLine(countingReader{io.LimitReader(f, histHead), &p.histRead}, func(line []byte) bool {
		s.add(line)
		return !s.sdk() // an SDK run needs nothing more
	})
	if s.sdk() || s.dir == "" {
		return e
	}
	if size > histHead {
		from := max(histHead, size-histTail)
		s.at = time.Time{}
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return e
		}
		first := true
		eachLine(countingReader{f, &p.histRead}, func(line []byte) bool {
			if !first { // the first line starts before the cut
				s.add(line)
			}
			first = false
			return true
		})
	}
	e.keep = true
	e.row = harness.HistoryRow{SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Dir: filepath.Clean(s.dir),
		Name: oneLineText(nonEmptyText(s.title, s.agent, s.prompt), histNameRunes), At: s.at}
	if e.row.At.IsZero() {
		e.row.At = mtime
	}
	return e
}

func nonEmptyText(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// oneLineText puts text on one line with no control characters, at most n
// runes. It comes from a transcript and goes to a terminal.
func oneLineText(s string, n int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// histIndex brings the index up to date and returns the sessions, newest
// first. A file that did not change costs one stat.
func (p *Provider) histIndex() []histEntry {
	if p.dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(p.dir, "projects", "*", "*.jsonl"))
	p.histMu.Lock()
	defer p.histMu.Unlock()
	next := make(map[string]histEntry, len(files))
	best := map[string]histEntry{} // session id → its newest file
	for _, path := range files {
		if !histIDRe.MatchString(strings.TrimSuffix(filepath.Base(path), ".jsonl")) {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		e, ok := p.hist[path]
		if !ok || e.size != st.Size() || !e.mtime.Equal(st.ModTime()) {
			e = p.scanFile(path, st.Size(), st.ModTime())
		}
		next[path] = e
		if !e.keep {
			continue
		}
		if b, ok := best[e.row.SessionID]; !ok || e.mtime.After(b.mtime) {
			best[e.row.SessionID] = e // resumed from another folder
		}
	}
	p.hist = next
	out := make([]histEntry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].row.At.Equal(out[j].row.At) {
			return out[i].row.At.After(out[j].row.At)
		}
		return out[i].row.SessionID < out[j].row.SessionID
	})
	return out
}

// History returns the past interactive sessions, newest first.
func (p *Provider) History() []harness.HistoryRow {
	idx := p.histIndex()
	out := make([]harness.HistoryRow, len(idx))
	for i, e := range idx {
		out[i] = e.row
	}
	return out
}

// Search looks for query, in any case, in the text of user and assistant
// records, newest file first, and returns at most limit hits. The records
// of one file come oldest first. A record with several matches is one hit.
func (p *Provider) Search(ctx context.Context, query string, limit int) []harness.SearchHit {
	needle := []rune(lowerRunes(query))
	if len(needle) == 0 || limit <= 0 {
		return nil
	}
	raw := rawForm(string(needle))
	var hits []harness.SearchHit
	for _, e := range p.histIndex() {
		if len(hits) >= limit || ctx.Err() != nil {
			break
		}
		hits = searchFile(ctx, e, needle, raw, limit, hits)
	}
	return hits
}

// lowerRunes lowers each rune and keeps the rune count, so an index in the
// lowered text is an index in the text.
func lowerRunes(s string) string {
	return strings.Map(unicode.ToLower, strings.TrimSpace(s))
}

// rawForm is the needle as it stands in a JSON line: a line without it
// cannot hold the words, and costs no parse.
func rawForm(needle string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(needle) != nil {
		return nil
	}
	return bytes.Trim(bytes.TrimSpace(b.Bytes()), `"`)
}

func searchFile(ctx context.Context, e histEntry, needle []rune, raw []byte, limit int, hits []harness.SearchHit) []harness.SearchHit {
	f, err := os.Open(e.path)
	if err != nil {
		return hits
	}
	defer f.Close()
	eachLine(f, func(line []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		if len(line) == 0 || line[0] != '{' || !bytes.Contains(bytes.ToLower(line), raw) {
			return true
		}
		var r histRecord
		if json.Unmarshal(line, &r) != nil || !r.spoken() {
			return true
		}
		snippet := findSnippet(r.text(), needle)
		if snippet == "" {
			return true
		}
		role := harness.RoleHuman
		if r.Type == "assistant" {
			role = harness.RoleAgent
		}
		row := e.row
		row.At = r.Timestamp
		hits = append(hits, harness.SearchHit{HistoryRow: row, Role: role, Text: snippet})
		return len(hits) < limit
	})
	return hits
}

// findSnippet returns the words with text around them ("" = not there).
func findSnippet(text string, needle []rune) string {
	rs := []rune(text)
	low := []rune(strings.Map(unicode.ToLower, text))
	if len(low) != len(rs) {
		low = rs // an invalid byte: exact case only
	}
	at := -1
	for i := 0; i+len(needle) <= len(low) && at < 0; i++ {
		if low[i] == needle[0] && string(low[i:i+len(needle)]) == string(needle) {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	from, to := max(0, at-hitBefore), min(len(rs), at+len(needle)+hitAfter)
	s := oneLineText(string(rs[from:to]), hitBefore+hitAfter+len(needle)+1)
	if from > 0 {
		s = "…" + s
	}
	if to < len(rs) {
		s += "…"
	}
	return s
}
