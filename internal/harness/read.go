package harness

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What a provider that reads a JSONL transcript shares with the others.

const (
	// TailBytes is how much of a transcript's end a provider reads.
	TailBytes = 2 << 20
	// MaxTurnRunes caps the text of one turn.
	MaxTurnRunes = 6000
	// MaxTargetRunes caps a step's target.
	MaxTargetRunes = 300
)

// ScanTail calls fn for each JSON line in the last TailBytes of a file. A
// line that starts before the cut is left out. The line slice is valid only
// during the call.
func ScanTail(path string, fn func(line []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cut := false
	if st, err := f.Stat(); err == nil && st.Size() > TailBytes {
		if _, err := f.Seek(st.Size()-TailBytes, io.SeekStart); err != nil {
			return err
		}
		cut = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if cut {
			cut = false
			continue
		}
		if l := sc.Bytes(); len(l) > 0 && l[0] == '{' {
			fn(l)
		}
	}
	return sc.Err()
}

// Target is what a tool call works on: the first of keys that holds text,
// on one line.
func Target(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return ClipTarget(v)
		}
	}
	return ""
}

// ClipTarget makes text fit a step's target: its first line, cut.
func ClipTarget(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if r := []rune(s); len(r) > MaxTargetRunes {
		s = string(r[:MaxTargetRunes-1]) + "…"
	}
	return s
}

// TurnLog builds turns and the activity while a provider walks a
// transcript, oldest record first. An answer holds only its final message:
// a tool call drops the text before it.
type TurnLog struct {
	turns []Turn
	act   Activity
	steps []Step
	done  map[string]bool
}

// Exchange starts a new exchange: the steps so far belong to the old one.
func (l *TurnLog) Exchange() {
	l.steps, l.done = nil, nil
}

// Human adds the human's prompt. The same text right after the same prompt
// is one prompt: some agents record it twice.
func (l *TurnLog) Human(text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(l.turns); n > 0 && l.turns[n-1].Role == RoleHuman && l.turns[n-1].Text == text {
		return
	}
	l.turns = append(l.turns, Turn{Role: RoleHuman, Text: text, At: at})
	l.act = Activity{Prompt: text, PromptAt: at}
	l.Exchange()
}

// answer is the turn of the newest answer.
func (l *TurnLog) answer(at time.Time) *Turn {
	if n := len(l.turns); n == 0 || l.turns[n-1].Role != RoleAgent {
		l.turns = append(l.turns, Turn{Role: RoleAgent})
	}
	t := &l.turns[len(l.turns)-1]
	t.At = at
	return t
}

// Answer sets the newest answer's message. A newer message replaces it:
// agents write the same text in more than one record.
func (l *TurnLog) Answer(text string, at time.Time) {
	if text = strings.TrimSpace(text); text != "" {
		l.answer(at).Text = text
	}
}

// Call adds a tool call to the newest answer. s.ID ties it to its result.
func (l *TurnLog) Call(s Step) {
	t := l.answer(s.At)
	t.Text = ""
	t.Tools++
	l.steps = append(l.steps, s)
}

// Done marks a tool call's result.
func (l *TurnLog) Done(id string) {
	if l.done == nil {
		l.done = map[string]bool{}
	}
	l.done[id] = true
}

// Result gives the last limit turns and the newest exchange's activity.
func (l *TurnLog) Result(limit int) ([]Turn, Activity) {
	act := l.act
	act.Tools = len(l.steps)
	steps := make([]Step, len(l.steps))
	copy(steps, l.steps)
	for i := range steps {
		steps[i].Done = l.done[steps[i].ID]
		if !steps[i].Done && act.Pending == nil {
			p := steps[i]
			act.Pending = &p
		}
	}
	if len(steps) > MaxSteps {
		steps = steps[len(steps)-MaxSteps:]
	}
	act.Steps = steps
	if r := []rune(act.Prompt); len(r) > MaxTurnRunes {
		act.Prompt = string(r[:MaxTurnRunes-1]) + "…"
	}
	turns := l.turns
	if limit > 0 && len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	out := make([]Turn, len(turns))
	copy(out, turns)
	for i := range out {
		if r := []rune(out[i].Text); len(r) > MaxTurnRunes {
			out[i].Text = "…" + string(r[len(r)-MaxTurnRunes+1:])
		}
	}
	return out, act
}

// FileCache remembers where each session's transcript is and what one
// parse of it gave, so session.list parses a transcript only when it
// changed. A missing transcript is looked for at most every 30 s.
type FileCache[T any] struct {
	Find  func(sessionID string) string // the transcript, "" for none
	Parse func(path string) T

	mu     sync.Mutex
	paths  map[string]foundPath
	parsed map[string]parsedFile[T]
}

type foundPath struct {
	path string
	at   time.Time
}

type parsedFile[T any] struct {
	stamp string
	value T
}

// Remember sets a session's transcript, when discovery already found it.
func (c *FileCache[T]) Remember(sessionID, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remember(sessionID, path)
}

func (c *FileCache[T]) remember(sessionID, path string) {
	if c.paths == nil || len(c.paths) > 512 {
		c.paths = map[string]foundPath{}
	}
	c.paths[sessionID] = foundPath{path: path, at: time.Now()}
}

// locate gives a session's transcript and its stat.
func (c *FileCache[T]) locate(sessionID string) (string, os.FileInfo) {
	e, ok := c.paths[sessionID]
	if !ok || e.path == "" && time.Since(e.at) > 30*time.Second {
		e.path = c.Find(sessionID)
		c.remember(sessionID, e.path)
	}
	if e.path == "" {
		return "", nil
	}
	st, err := os.Stat(e.path)
	if err != nil {
		delete(c.paths, sessionID)
		return "", nil
	}
	return e.path, st
}

func stampOf(path string, st os.FileInfo) string {
	return path + "/" + strconv.FormatInt(st.Size(), 10) + "/" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// Stamp is the transcript's path, size and time, "" with no transcript.
func (c *FileCache[T]) Stamp(sessionID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	path, st := c.locate(sessionID)
	if path == "" {
		return ""
	}
	return stampOf(path, st)
}

// Get gives the parse of a session's transcript and when the file changed.
// ok is false with no transcript.
func (c *FileCache[T]) Get(sessionID string) (value T, wrote time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path, st := c.locate(sessionID)
	if path == "" {
		return value, wrote, false
	}
	stamp := stampOf(path, st)
	e, hit := c.parsed[path]
	if !hit || e.stamp != stamp {
		if c.parsed == nil || len(c.parsed) > 64 {
			c.parsed = map[string]parsedFile[T]{}
		}
		e = parsedFile[T]{stamp: stamp, value: c.Parse(path)}
		c.parsed[path] = e
	}
	return e.value, st.ModTime(), true
}
