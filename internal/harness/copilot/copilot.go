// Package copilot is the GitHub Copilot CLI provider (ADR-0019). It is built
// from a reading of Copilot's docs, its event schema (1.0.60) and its bundle.
// No real session checked it, so it is unverified: smif shows these sessions
// and never types into them, forks them or installs hooks for them.
//
// A session is a folder $COPILOT_HOME/session-state/<id> with events.jsonl
// and workspace.yaml. A running process holds inuse.<pid>.lock in it. A lock
// can outlive a crash, so the pid must be alive, be copilot, sit on a
// terminal and not have started after the lock.
package copilot

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// Name is the harness name of Copilot sessions.
const Name = "copilot"

const (
	sameProcessFor = 30 * time.Second // how long one check of a process counts
	startSlack     = 5 * time.Second  // the one-second grain of a start time
	checkSessions  = 3                // how many sessions smif doctor reads
)

// Provider reads one Copilot home folder.
type Provider struct {
	dir  string
	look func(pid int) (harness.Proc, bool)

	cache harness.FileCache[transcript]

	mu    sync.Mutex
	alive map[string]sameProcess // pid and lock time → the last check
}

type sameProcess struct {
	same  bool
	start time.Time
	at    time.Time
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a home folder ("" reads nothing).
func New(dir string) *Provider { return NewWithLook(dir, harness.ProcLook) }

// NewWithLook takes the process check from look: tests use it.
func NewWithLook(dir string, look func(pid int) (harness.Proc, bool)) *Provider {
	p := &Provider{dir: dir, look: look, alive: map[string]sameProcess{}}
	p.cache.Find, p.cache.Parse = p.eventsPath, readTranscript
	return p
}

// Info names the provider. Verified stays false until a real session
// checks it and docs/compat.md has a row with that version.
func (p *Provider) Info() harness.Info { return harness.Info{Name: Name, Process: "copilot"} }

func (p *Provider) stateDir() string { return filepath.Join(p.dir, "session-state") }

// eventsPath is a session's event log, "" when it is not there.
func (p *Provider) eventsPath(sessionID string) string {
	if p.dir == "" || !harness.ValidID(sessionID) {
		return ""
	}
	path := filepath.Join(p.stateDir(), sessionID, "events.jsonl")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// lockPID reads the pid in inuse.<pid>.lock.
func lockPID(path string) (int, bool) {
	s, ok := strings.CutPrefix(filepath.Base(path), "inuse.")
	if !ok {
		return 0, false
	}
	s, ok = strings.CutSuffix(s, ".lock")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(s)
	return pid, err == nil && pid > 0
}

// holder is the process behind a lock, when it is a live Copilot that
// could have taken the lock. See the package comment.
func (p *Provider) holder(pid int, locked time.Time) (time.Time, bool) {
	if !harness.ProcessAlive(pid) {
		return time.Time{}, false
	}
	key := fmt.Sprintf("%d/%d", pid, locked.UnixNano())
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.alive[key]; ok && time.Since(e.at) < sameProcessFor {
		return e.start, e.same
	}
	pr, ok := p.look(pid)
	same := ok && pr.TTY != "" && harness.ListedProcess(pr.Argv) == Name && !pr.Start.After(locked.Add(startSlack))
	if len(p.alive) > 256 {
		p.alive = map[string]sameProcess{}
	}
	p.alive[key] = sameProcess{same: same, start: pr.Start, at: time.Now()}
	return pr.Start, same
}

// workspace reads the flat keys smif uses from workspace.yaml.
func workspace(path string) (cwd, name string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	fields := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && !strings.HasPrefix(k, " ") {
			fields[k] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	name = fields["name"]
	if name == "" {
		name = fields["summary"]
	}
	return fields["cwd"], name
}

// Discover returns the sessions a live Copilot process holds. A session
// that two processes hold shows once, with the newest process.
func (p *Provider) Discover() []harness.Session {
	if p.dir == "" {
		return nil
	}
	locks, _ := filepath.Glob(filepath.Join(p.stateDir(), "*", "inuse.*.lock"))
	byID := map[string]harness.Session{}
	for _, l := range locks {
		id := filepath.Base(filepath.Dir(l))
		pid, ok := lockPID(l)
		st, err := os.Stat(l)
		if !ok || err != nil || !harness.ValidID(id) {
			continue
		}
		start, ok := p.holder(pid, st.ModTime())
		if !ok || start.UnixMilli() < byID[id].StartedAt {
			continue
		}
		s := harness.Session{PID: pid, SessionID: id, StartedAt: start.UnixMilli()}
		var yamlCwd string
		yamlCwd, s.Name = workspace(filepath.Join(filepath.Dir(l), "workspace.yaml"))
		s.Cwd = yamlCwd
		if t, wrote, ok := p.cache.Get(id); ok {
			s.StatusAt = wrote
			if t.cwd != "" {
				s.Cwd = t.cwd // events.jsonl follows a change of folder
			}
		}
		byID[id] = s
	}
	out := make([]harness.Session, 0, len(byID))
	for _, s := range byID {
		if s.Cwd != "" {
			out = append(out, s)
		}
	}
	return out
}

// State is what the event log says. Discover already checked the process.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	t, _, ok := p.cache.Get(s.SessionID)
	if !ok {
		return "", time.Time{}
	}
	return t.turn, t.at
}

// Read reads a session's event log. A waiting permission is the pending
// step: a request can come before its tool's start record.
func (p *Provider) Read(sessionID string, limit int) harness.Reading {
	t, wrote, ok := p.cache.Get(sessionID)
	if !ok {
		return harness.Reading{}
	}
	turns, act := t.log.Result(limit)
	if t.asked != nil {
		act.Pending = t.asked
	}
	return harness.Reading{Turns: turns, Activity: act, WroteAt: wrote, OK: true}
}

// Stamp is the event log's path, size and time.
func (p *Provider) Stamp(sessionID string) string { return p.cache.Stamp(sessionID) }

// Check compares the newest event logs on this machine with the source
// reading. The detail names counts only, never a value from a file.
func (p *Provider) Check() (state, detail string) {
	if p.dir == "" {
		return "none", "no Copilot folder"
	}
	files, _ := filepath.Glob(filepath.Join(p.stateDir(), "*", "events.jsonl"))
	if len(files) == 0 {
		return "none", "no event logs in " + p.stateDir()
	}
	at := func(f string) int64 {
		st, err := os.Stat(f)
		if err != nil {
			return 0
		}
		return st.ModTime().UnixNano()
	}
	slices.SortFunc(files, func(a, b string) int { return cmp.Compare(at(b), at(a)) })
	if len(files) > checkSessions {
		files = files[:checkSessions]
	}
	for _, f := range files {
		if readTranscript(f).marked {
			return "ok", fmt.Sprintf("%d event logs read", len(files))
		}
	}
	return "changed", fmt.Sprintf("%d event logs read, no session or turn record found in them", len(files))
}
