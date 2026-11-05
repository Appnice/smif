// Package droid is the Factory Droid provider (ADR-0019). It is built from a
// reading of Droid's docs, its 1.3.0 bundle and the Entire project's adapter.
// No real session checked it, so it is unverified: smif shows these sessions
// and never types into them, forks them or installs hooks for them.
//
// Droid keeps no live file, so smif matches like the Codex provider: a droid
// process on a terminal owns the transcript in its folder that was written
// since the process started. `droid --resume <id>` names its session. `droid
// exec` is no session, but its transcript has no mark that says so: a TUI in
// the same folder can take one. --cwd moves a process's folder.
package droid

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// Name is the harness name of Droid sessions.
const Name = "droid"

const (
	discoverEvery = 2 * time.Second // ps runs at most this often
	checkFiles    = 3               // how many transcripts smif doctor reads
)

// Provider reads one Droid home folder.
type Provider struct {
	dir   string
	procs func() []harness.Proc

	cache harness.FileCache[transcript]

	mu   sync.Mutex
	at   time.Time
	last []harness.Session
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a home folder ("" reads nothing).
func New(dir string) *Provider { return NewWithProcs(dir, harness.ProcLister(sessionProc)) }

// NewWithProcs takes the process list from procs: tests use it.
func NewWithProcs(dir string, procs func() []harness.Proc) *Provider {
	p := &Provider{dir: dir, procs: procs}
	p.cache.Find, p.cache.Parse = p.transcriptPath, readTranscript
	return p
}

// Info names the provider. Verified stays false until a real session
// checks it and docs/compat.md has a row with that version.
func (p *Provider) Info() harness.Info { return harness.Info{Name: Name, Process: "droid"} }

func (p *Provider) sessionsDir() string { return filepath.Join(p.dir, "sessions") }

// flag gives the value of a --name or --name=value flag.
func flag(argv []string, names ...string) string {
	for i, a := range argv {
		for _, n := range names {
			if a == n && i+1 < len(argv) {
				return argv[i+1]
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v
			}
		}
	}
	return ""
}

// sessionProc reports a process that can be a human's TUI: a process with
// no terminal is a server, and `droid exec` is a headless run.
func sessionProc(pr harness.Proc) bool {
	if pr.TTY == "" || harness.ListedProcess(pr.Argv) != Name {
		return false
	}
	cwd := flag(pr.Argv, "--cwd")
	for _, w := range harness.ArgWords(pr.Argv) {
		if w != cwd {
			return w != "exec"
		}
	}
	return true
}

// resumed is the session `droid --resume <id>` names ("" for any other run).
func resumed(pr harness.Proc) string {
	if id := flag(pr.Argv, "--resume", "-r"); harness.ValidID(id) {
		return id
	}
	return ""
}

// start is the part of a transcript's first line smif uses.
type start struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	LastCwd string `json:"lastCwd"`
	Calling string `json:"callingSessionId"` // a subagent's parent session
}

func (s start) ok() bool { return s.Type == "session_start" && harness.ValidID(s.ID) }

func (s start) dir() string {
	if s.Cwd != "" {
		return s.Cwd
	}
	return s.LastCwd
}

// readStart reads a transcript's first line.
func readStart(path string) start {
	var s start
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return s
	}
	_ = json.Unmarshal(line, &s)
	return s
}

// files are the transcripts, session or subagent.
func (p *Provider) files() []string {
	files, _ := filepath.Glob(filepath.Join(p.sessionsDir(), "*", "*.jsonl"))
	return files
}

// recent are the human sessions written since a time.
func (p *Provider) recent(since time.Time) []harness.Candidate {
	var out []harness.Candidate
	for _, f := range p.files() {
		st, err := os.Stat(f)
		if err != nil || st.ModTime().Before(since) {
			continue
		}
		if s := readStart(f); s.ok() && s.Calling == "" {
			out = append(out, candidate(f, s, st.ModTime()))
		}
	}
	return out
}

func candidate(path string, s start, wrote time.Time) harness.Candidate {
	return harness.Candidate{ID: s.ID, Cwd: s.dir(), Path: path, Name: s.Title, Created: createdAt(path, wrote), Wrote: wrote}
}

// createdAt is the first message's time, else the time the file changed:
// session_start has no time.
func createdAt(path string, wrote time.Time) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return wrote
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var l struct {
			Timestamp stamp `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && !l.Timestamp.IsZero() {
			return l.Timestamp.Time
		}
	}
	return wrote
}

// transcriptPath finds a session's transcript. The folder name comes from
// the project's path, so this looks in all of them.
func (p *Provider) transcriptPath(sessionID string) string {
	if p.dir == "" || !harness.ValidID(sessionID) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(p.sessionsDir(), "*", sessionID+".jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	return best
}

// match ties processes to transcripts. See the package comment.
func (p *Provider) match(procs []harness.Proc) []harness.Session {
	if len(procs) == 0 {
		return nil
	}
	oldest := procs[0].Start
	procs = slices.Clone(procs)
	for i, pr := range procs {
		if pr.Start.Before(oldest) {
			oldest = pr.Start
		}
		if v := flag(pr.Argv, "--cwd"); v != "" {
			procs[i].Cwd = v
		}
	}
	found := p.recent(oldest)
	for _, pr := range procs { // a resumed session can be older than any process
		if id := resumed(pr); id != "" {
			if path := p.transcriptPath(id); path != "" {
				if st, err := os.Stat(path); err == nil {
					if s := readStart(path); s.ok() {
						found = append(found, candidate(path, s, st.ModTime()))
					}
				}
			}
		}
	}
	var out []harness.Session
	for _, o := range harness.MatchProcs(procs, found, resumed) {
		p.cache.Remember(o.Session.ID, o.Session.Path)
		out = append(out, harness.Session{PID: o.Proc.PID, SessionID: o.Session.ID, Cwd: o.Session.Cwd, Name: o.Session.Name,
			StartedAt: o.Proc.Start.UnixMilli(), StatusAt: o.Session.Wrote})
	}
	return out
}

// Discover returns the sessions a live droid process owns. With no sessions
// folder it runs no ps, so a machine without Droid pays only a stat.
func (p *Provider) Discover() []harness.Session {
	if p.dir == "" {
		return nil
	}
	if _, err := os.Stat(p.sessionsDir()); err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.at.IsZero() || time.Since(p.at) >= discoverEvery {
		p.last, p.at = p.match(p.procs()), time.Now()
	}
	out := make([]harness.Session, 0, len(p.last))
	for _, s := range p.last {
		if harness.ProcessAlive(s.PID) {
			out = append(out, s)
		}
	}
	return out
}

// State is what the transcript's end says. The file records no approval, so
// a tool call with no result gives "": a running tool looks the same as an
// approval prompt. Discover already checked the process.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	t, _, ok := p.cache.Get(s.SessionID)
	if !ok {
		return "", time.Time{}
	}
	return t.turn, t.at
}

// Read reads a session's transcript.
func (p *Provider) Read(sessionID string, limit int) harness.Reading {
	t, wrote, ok := p.cache.Get(sessionID)
	if !ok {
		return harness.Reading{}
	}
	turns, act := t.log.Result(limit)
	return harness.Reading{Turns: turns, Activity: act, WroteAt: wrote, OK: true}
}

// Stamp is the transcript's path, size and time.
func (p *Provider) Stamp(sessionID string) string { return p.cache.Stamp(sessionID) }

// Check compares the newest transcripts on this machine with the source
// reading. The detail names counts only, never a value from a file.
func (p *Provider) Check() (state, detail string) {
	if p.dir == "" {
		return "none", "no Droid folder"
	}
	files := p.files()
	if len(files) == 0 {
		return "none", "no transcripts in " + p.sessionsDir()
	}
	at := func(f string) int64 {
		st, err := os.Stat(f)
		if err != nil {
			return 0
		}
		return st.ModTime().UnixNano()
	}
	slices.SortFunc(files, func(a, b string) int { return cmp.Compare(at(b), at(a)) })
	if len(files) > checkFiles {
		files = files[:checkFiles]
	}
	marked := 0
	for _, f := range files {
		if !readStart(f).ok() {
			return "changed", filepath.Base(f) + " has no session_start line with an id"
		}
		if readTranscript(f).marked {
			marked++
		}
	}
	if marked == 0 {
		return "changed", fmt.Sprintf("%d transcripts read, no message found in them", len(files))
	}
	return "ok", fmt.Sprintf("%d transcripts read", len(files))
}
