// Package qwen is the Qwen Code provider (ADR-0019). It is built from a
// reading of Qwen Code's source at 0.26.7 (commit cf6b4270). No real session
// checked it, so it is unverified: smif shows these sessions and never types
// into them, forks them or installs hooks for them.
//
// Each running session keeps a file in $QWEN_HOME/sessions: <pid>.json, or
// <pid>-<8 hex>.json for an --acp process that hosts several sessions. The
// file has no state field and holds a secret, ipcToken, that smif must never
// read into a log or a view. The record struct here has no such field, so
// the value stays out of every value this package keeps.
package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// Name is the harness name of Qwen Code sessions.
const Name = "qwen"

// schemaVersion is the registry format the source reading saw.
const schemaVersion = 1

// sameProcessFor is how long one check of a process's start time counts.
const sameProcessFor = 30 * time.Second

// startSlack allows for the one-second grain of a process's start time.
const startSlack = 5 * time.Second

// Provider reads one Qwen Code home folder.
type Provider struct {
	dir     string // sessions/
	runtime string // projects/

	cache harness.FileCache[transcript]

	mu    sync.Mutex
	alive map[string]sameProcess // pid and record time → the last check
}

type sameProcess struct {
	same bool
	at   time.Time
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a home folder and a runtime folder, which
// the user can move apart ("" reads nothing).
func New(dir, runtime string) *Provider {
	p := &Provider{dir: dir, runtime: runtime, alive: map[string]sameProcess{}}
	p.cache.Find, p.cache.Parse = p.transcriptPath, readTranscript
	return p
}

// Info names the provider. Verified stays false until a real session
// checks it and docs/compat.md has a row with that version. The process is
// `node`, so the name here matches nothing: the foreground check refuses.
func (p *Provider) Info() harness.Info { return harness.Info{Name: Name, Process: "qwen"} }

// record is the part of a registry file smif uses. It leaves ipcToken and
// ipcPath out on purpose: see the package comment.
type record struct {
	SchemaVersion int    `json:"schemaVersion"`
	PID           int    `json:"pid"`
	SessionID     string `json:"sessionId"`
	Cwd           string `json:"cwd"`
	Name          string `json:"name"`
	StartedAt     int64  `json:"startedAt"`
	Kind          string `json:"kind"`
}

func (p *Provider) sessionsDir() string {
	if p.dir == "" {
		return ""
	}
	return filepath.Join(p.dir, "sessions")
}

func readRecord(path string) (record, bool) {
	var r record
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<10 {
		return r, false
	}
	return r, json.Unmarshal(b, &r) == nil
}

// interactive leaves out what no human drives: headless is a standalone
// --acp process, serve a daemon child. Other kinds show, since a read-only
// row is harmless.
func interactive(kind string) bool { return kind != "headless" && kind != "serve" }

// sameProcess reports whether the record's pid is still the process that
// wrote it. A record can outlive a crash, and the pid can come back as
// another process, which then starts after the record was written.
func (p *Provider) sameProcess(r record) bool {
	if !harness.ProcessAlive(r.PID) {
		return false
	}
	key := fmt.Sprintf("%d/%d", r.PID, r.StartedAt)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.alive[key]; ok && time.Since(e.at) < sameProcessFor {
		return e.same
	}
	start := harness.ProcStart(r.PID)
	same := r.StartedAt == 0 || start.IsZero() || !start.After(time.UnixMilli(r.StartedAt).Add(startSlack))
	if len(p.alive) > 256 {
		p.alive = map[string]sameProcess{}
	}
	p.alive[key] = sameProcess{same: same, at: time.Now()}
	return same
}

// Discover returns the interactive sessions whose process lives.
func (p *Provider) Discover() []harness.Session {
	if p.dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(p.sessionsDir(), "*.json"))
	byID := map[string]record{}
	for _, f := range files {
		r, ok := readRecord(f)
		if !ok || !harness.ValidID(r.SessionID) || !interactive(r.Kind) || !p.sameProcess(r) {
			continue
		}
		if old, ok := byID[r.SessionID]; ok && old.StartedAt > r.StartedAt {
			continue
		}
		byID[r.SessionID] = r
	}
	out := make([]harness.Session, 0, len(byID))
	for _, r := range byID {
		s := harness.Session{PID: r.PID, SessionID: r.SessionID, Cwd: r.Cwd, Name: r.Name, StartedAt: r.StartedAt}
		if _, wrote, ok := p.cache.Get(r.SessionID); ok {
			s.StatusAt = wrote
		}
		out = append(out, s)
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

// transcriptPath finds a session's transcript. The folder name comes from
// the project root in a way that cannot be reversed, so this looks in all.
func (p *Provider) transcriptPath(sessionID string) string {
	if p.runtime == "" || !harness.ValidID(sessionID) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(p.runtime, "projects", "*", "chats", sessionID+".jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	return best
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

// Check compares the files on this machine with the source reading. The
// detail names counts and file names only, never a value from a file.
func (p *Provider) Check() (state, detail string) {
	dir := p.sessionsDir()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		return "none", "no session files in " + dir
	}
	parsed, read, turns := 0, 0, 0
	for _, f := range files {
		r, ok := readRecord(f)
		if !ok || r.SchemaVersion != schemaVersion || r.PID == 0 || r.SessionID == "" || r.Cwd == "" {
			continue
		}
		parsed++
		if read < 3 {
			if t, _, ok := p.cache.Get(r.SessionID); ok {
				read++
				got, _ := t.log.Result(4)
				turns += len(got)
			}
		}
	}
	switch {
	case parsed == 0:
		return "changed", fmt.Sprintf("none of %d session files has schemaVersion %d, pid, sessionId and cwd", len(files), schemaVersion)
	case read > 0 && turns == 0:
		return "changed", fmt.Sprintf("%d transcripts read, no prompt or answer found in them", read)
	}
	return "ok", fmt.Sprintf("%d session files, %d transcripts read", parsed, read)
}
