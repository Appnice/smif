// Package codex is the Codex CLI provider (ADR-0019). It is built from a
// reading of Codex's source at commit c9035417 (tag rust-v0.168.0-alpha.3). No
// real session checked it, so it is unverified: smif shows these sessions and
// never types into them, forks them or installs hooks for them.
//
// Codex keeps no live file for a session, so no file links a pid to a
// session. Smif matches by ps and folder: a codex process on a terminal
// owns the rollout with its folder that was written since the process
// started. `codex resume <id>` names its session in argv. Two processes in
// one folder get the rollouts newest to newest, which can swap them when
// the newer process resumed an older session. A process with no such
// rollout (Codex writes the file at the first prompt) shows at the Listed
// level instead. Rollouts compressed to .jsonl.zst are skipped: they are
// cold, and reading them needs a new dependency.
package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// Name is the harness name of Codex sessions.
const Name = "codex"

const (
	discoverEvery = 2 * time.Second // ps runs at most this often
	maxDays       = 30              // the newest day folders of rollouts to look in
	checkRollouts = 3               // how many rollouts smif doctor reads
)

// Proc is a process that may be a Codex session.
type Proc = harness.Proc

// Provider reads one Codex home folder.
type Provider struct {
	dir   string
	procs func() []Proc

	cache harness.FileCache[transcript]

	mu    sync.Mutex
	at    time.Time
	last  []harness.Session
	metas map[string]meta // rollout path → its first line
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a home folder ("" reads nothing).
func New(dir string) *Provider {
	return NewWithProcs(dir, harness.ProcLister(sessionProc))
}

// NewWithProcs takes the process list from procs: tests use it.
func NewWithProcs(dir string, procs func() []Proc) *Provider {
	p := &Provider{dir: dir, procs: procs, metas: map[string]meta{}}
	p.cache.Find, p.cache.Parse = p.rolloutPath, readTranscript
	return p
}

// Info names the provider. Verified stays false until a real session
// checks it and docs/compat.md has a row with that version.
func (p *Provider) Info() harness.Info { return harness.Info{Name: Name, Process: "codex"} }

func (p *Provider) sessionsDir() string { return filepath.Join(p.dir, "sessions") }

// sessionProc reports a process that can be a human's Codex session: a
// process with no terminal is a server, and the same binary has other roles.
func sessionProc(p Proc) bool {
	return p.TTY != "" && harness.ListedProcess(p.Argv) == Name && interactive(p.Argv)
}

// otherRoles are subcommands that are no interactive session. The first
// word that is no flag decides, so a flag's value can hide a subcommand.
var otherRoles = []string{"exec", "e", "review", "app-server", "agents", "queue", "mcp", "mcp-server",
	"login", "logout", "completion", "sandbox", "debug", "apply", "cloud", "features"}

func interactive(argv []string) bool {
	w := harness.ArgWords(argv)
	return len(w) == 0 || !slices.Contains(otherRoles, w[0])
}

// resumeID is the session `codex resume <id>` names ("" for any other run).
func resumeID(argv []string) string {
	if w := harness.ArgWords(argv); len(w) >= 2 && w[0] == "resume" && harness.ValidID(w[1]) {
		return w[1]
	}
	return ""
}

// meta is a rollout's first line, which never changes.
type meta struct {
	id, cwd string
	at      time.Time
	human   bool // a session a human started, no subagent or exec run
	ok      bool
}

// readMeta reads a rollout's session_meta line. source is a word for most
// sessions and an object for a subagent.
func readMeta(path string) meta {
	f, err := os.Open(path)
	if err != nil {
		return meta{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	if !sc.Scan() {
		return meta{}
	}
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string          `json:"id"`
			Cwd       string          `json:"cwd"`
			Timestamp time.Time       `json:"timestamp"`
			Source    json.RawMessage `json:"source"`
			Parent    string          `json:"parent_thread_id"`
			Role      string          `json:"agent_role"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "session_meta" || !harness.ValidID(l.Payload.ID) || l.Payload.Cwd == "" {
		return meta{}
	}
	var source string
	word := len(l.Payload.Source) == 0 || json.Unmarshal(l.Payload.Source, &source) == nil
	return meta{id: l.Payload.ID, cwd: l.Payload.Cwd, at: l.Payload.Timestamp, ok: true,
		human: word && source != "exec" && l.Payload.Parent == "" && l.Payload.Role == ""}
}

// metaOf caches readMeta. The caller holds p.mu.
func (p *Provider) metaOf(path string) meta {
	if m, ok := p.metas[path]; ok {
		return m
	}
	if len(p.metas) > 2048 {
		p.metas = map[string]meta{}
	}
	m := readMeta(path)
	p.metas[path] = m
	return m
}

// days are the day folders of rollouts, the newest first.
func (p *Provider) days() []string {
	days, _ := filepath.Glob(filepath.Join(p.sessionsDir(), "*", "*", "*"))
	slices.Sort(days)
	slices.Reverse(days)
	if len(days) > maxDays {
		days = days[:maxDays]
	}
	return days
}

func candidate(path string, m meta, wrote time.Time) harness.Candidate {
	return harness.Candidate{ID: m.id, Cwd: m.cwd, Path: path, Created: m.at, Wrote: wrote}
}

// recent are the rollouts of human sessions written since a time.
func (p *Provider) recent(since time.Time) []harness.Candidate {
	var out []harness.Candidate
	for _, d := range p.days() {
		files, _ := filepath.Glob(filepath.Join(d, "rollout-*.jsonl"))
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil || st.ModTime().Before(since) {
				continue
			}
			if m := p.metaOf(f); m.ok && m.human {
				out = append(out, candidate(f, m, st.ModTime()))
			}
		}
	}
	return out
}

// rolloutPath finds a session's newest rollout. A revert writes a second
// file for a thread: rollout-<time>-<id>_<rollout id>.jsonl.
func (p *Provider) rolloutPath(sessionID string) string {
	if p.dir == "" || !harness.ValidID(sessionID) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(p.sessionsDir(), "*", "*", "*", "rollout-*"+sessionID+"*.jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	return best
}

// match ties processes to rollouts. See the package comment.
func (p *Provider) match(procs []Proc) []harness.Session {
	if len(procs) == 0 {
		return nil
	}
	oldest := procs[0].Start
	for _, pr := range procs {
		if pr.Start.Before(oldest) {
			oldest = pr.Start
		}
	}
	files := p.recent(oldest)
	for _, pr := range procs { // a resumed session can be older than any process
		if id := resumeID(pr.Argv); id != "" {
			if path := p.rolloutPath(id); path != "" {
				if st, err := os.Stat(path); err == nil {
					if m := p.metaOf(path); m.ok {
						files = append(files, candidate(path, m, st.ModTime()))
					}
				}
			}
		}
	}
	names := p.names()
	var out []harness.Session
	for _, o := range harness.MatchProcs(procs, files, func(pr Proc) string { return resumeID(pr.Argv) }) {
		p.cache.Remember(o.Session.ID, o.Session.Path)
		out = append(out, harness.Session{PID: o.Proc.PID, SessionID: o.Session.ID, Cwd: o.Session.Cwd, Name: names[o.Session.ID],
			StartedAt: o.Proc.Start.UnixMilli(), StatusAt: o.Session.Wrote})
	}
	return out
}

// names reads session_index.jsonl: the newest line of a thread wins.
func (p *Provider) names() map[string]string {
	names := map[string]string{}
	_ = harness.ScanTail(filepath.Join(p.dir, "session_index.jsonl"), func(line []byte) {
		var e struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID != "" {
			names[e.ID] = e.Name
		}
	})
	return names
}

// Discover returns the sessions a live codex process owns. With no rollout
// folder it runs no ps, so a machine without Codex pays only a stat.
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

// State is what the rollout's end says. A rollout records no approval, so
// an open turn with a tool call and no result gives "": a running tool
// looks the same as an approval prompt. Discover already checked the
// process, so a turn left open by a crash does not show as busy.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	t, _, ok := p.cache.Get(s.SessionID)
	if !ok {
		return "", time.Time{}
	}
	return t.turn, t.at
}

// Read reads a session's rollout.
func (p *Provider) Read(sessionID string, limit int) harness.Reading {
	t, wrote, ok := p.cache.Get(sessionID)
	if !ok {
		return harness.Reading{}
	}
	turns, act := t.log.Result(limit)
	return harness.Reading{Turns: turns, Activity: act, WroteAt: wrote, OK: true}
}

// Stamp is the rollout's path, size and time.
func (p *Provider) Stamp(sessionID string) string { return p.cache.Stamp(sessionID) }

// Check compares the newest rollouts on this machine with the source
// reading. The detail names counts only, never a value from a file.
func (p *Provider) Check() (state, detail string) {
	if p.dir == "" {
		return "none", "no Codex folder"
	}
	var files []string
	for _, d := range p.days() {
		found, _ := filepath.Glob(filepath.Join(d, "rollout-*.jsonl"))
		slices.Sort(found)
		slices.Reverse(found)
		files = append(files, found...)
		if len(files) >= checkRollouts {
			files = files[:checkRollouts]
			break
		}
	}
	if len(files) == 0 {
		return "none", "no rollouts in " + p.sessionsDir()
	}
	marked := 0
	for _, f := range files {
		if !readMeta(f).ok {
			return "changed", filepath.Base(f) + " has no session_meta line with id and cwd"
		}
		if readTranscript(f).marked {
			marked++
		}
	}
	if marked == 0 {
		return "changed", fmt.Sprintf("%d rollouts read, no turn record found in them", len(files))
	}
	return "ok", fmt.Sprintf("%d rollouts read", len(files))
}
