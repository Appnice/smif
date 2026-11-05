// Package claude is the Claude Code provider (ADR-0019): session files,
// transcripts, the file status, the fork route and the process name.
//
// Claude Code keeps one file for each running session in
// $CLAUDE_CONFIG_DIR/sessions/<pid>.json (default ~/.claude): its session
// id, cwd, name and status (idle, busy; shell while a shell it started
// runs). Smif reads these files, so every open session shows up at once,
// also one that had no hook since Smif was installed. The files are not a
// documented interface: a missing or changed file only means the hooks
// alone decide (docs/compat.md, ADR-0012).
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/install"
)

// Name is the harness name of Claude Code sessions. Rows from before the
// harness column carry it too (migration 005).
const Name = "claude"

// Provider reads one Claude Code config folder.
type Provider struct {
	dir string // the config folder

	mu    sync.Mutex
	paths map[string]pathEntry // session id → its transcript, for Stamp
}

var (
	_ harness.Provider = (*Provider)(nil)
	_ harness.Stater   = (*Provider)(nil)
	_ harness.Hooker   = (*Provider)(nil)
	_ harness.Forker   = (*Provider)(nil)
	_ harness.Stamper  = (*Provider)(nil)
	_ harness.Checker  = (*Provider)(nil)
)

// New returns the provider for a config folder ("" reads nothing).
func New(dir string) *Provider { return &Provider{dir: dir, paths: map[string]pathEntry{}} }

// Info names the provider. A real session checks every Claude Code version
// (docs/compat.md).
func (p *Provider) Info() harness.Info {
	return harness.Info{Name: Name, Process: "claude", Verified: true}
}

// sessionsDir is where Claude Code keeps its session files.
func (p *Provider) sessionsDir() string {
	if p.dir == "" {
		return ""
	}
	return filepath.Join(p.dir, "sessions")
}

// claudeSession is the part of one session file that Smif uses.
type claudeSession struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	StartedAt       int64  `json:"startedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// Discover returns the interactive sessions whose process lives.
func (p *Provider) Discover() []harness.Session {
	found := readClaudeSessions(p.sessionsDir())
	out := make([]harness.Session, 0, len(found))
	for _, s := range found {
		out = append(out, harness.Session{PID: s.PID, SessionID: s.SessionID, Cwd: s.Cwd, Name: s.Name,
			StartedAt: s.StartedAt, Status: s.Status, StatusAt: time.UnixMilli(s.StatusUpdatedAt)})
	}
	return out
}

// readClaudeSessions returns the interactive sessions whose process lives.
// When two processes report one session id (a suspended claude and its
// resumed copy), the newer process wins.
func readClaudeSessions(dir string) []claudeSession {
	if dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	byID := map[string]claudeSession{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 64<<10 {
			continue
		}
		var s claudeSession
		if json.Unmarshal(b, &s) != nil || s.Kind != "interactive" || s.SessionID == "" || !harness.ProcessAlive(s.PID) {
			continue
		}
		if old, ok := byID[s.SessionID]; ok && old.StartedAt > s.StartedAt {
			continue
		}
		byID[s.SessionID] = s
	}
	out := make([]claudeSession, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	return out
}

// State maps a session file's status to a turn.
func (p *Provider) State(s harness.Session) (string, time.Time) {
	return fileTurn(s.Status), s.StatusAt
}

// fileTurn maps a session file's status to a turn ("" = the file says
// nothing). Since Claude Code 2.1.316 the status is "shell" while a shell
// the session started runs, also a background one after the turn ended, so
// it tells neither busy nor idle: the hooks decide then.
func fileTurn(status string) string {
	if status == "shell" {
		return ""
	}
	return status
}

// HookEvents are the Claude Code hooks `smif install` adds.
func (p *Provider) HookEvents() []string { return install.HookEvents }

// ForkDir is where a fork resumes: claude finds a session only from its
// own folder (ADR-0013). internal/runner/claude builds the command
// (`claude -p --resume <id> --fork-session`).
func (p *Provider) ForkDir(cwd, repo string) string {
	if cwd != "" {
		return cwd
	}
	return repo
}

// transcriptPath finds a session's transcript:
// <claude config>/projects/<cwd folder>/<session id>.jsonl, the newest one
// when a session was resumed from another folder.
func (p *Provider) transcriptPath(sessionID string) string {
	if p.dir == "" || !harness.ValidID(sessionID) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(p.dir, "projects", "*", sessionID+".jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	return best
}

// Read reads a session's transcript. WroteAt is when the file last changed.
func (p *Provider) Read(sessionID string, limit int) harness.Reading {
	path := p.transcriptPath(sessionID)
	if path == "" {
		return harness.Reading{}
	}
	st, err := os.Stat(path)
	if err != nil {
		return harness.Reading{}
	}
	turns, act := readTranscript(path, limit)
	return harness.Reading{Turns: turns, Activity: act, WroteAt: st.ModTime(), OK: true}
}

// pathEntry is a transcript found for a session ("" = none at checkedAt).
type pathEntry struct {
	path      string
	checkedAt time.Time
}

// Stamp is the transcript's path, size and time. It looks for a missing
// transcript at most every 30 s: session.list runs on every UI refresh.
func (p *Provider) Stamp(sessionID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.paths) > 512 {
		p.paths = map[string]pathEntry{}
	}
	e, ok := p.paths[sessionID]
	if !ok || e.path == "" && time.Since(e.checkedAt) > 30*time.Second {
		e = pathEntry{path: p.transcriptPath(sessionID), checkedAt: time.Now()}
	}
	if e.path == "" {
		p.paths[sessionID] = e
		return ""
	}
	st, err := os.Stat(e.path)
	if err != nil {
		delete(p.paths, sessionID)
		return ""
	}
	p.paths[sessionID] = e
	return e.path + "/" + strconv.FormatInt(st.Size(), 10) + "/" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// Check checks that Claude Code's session files and transcripts still have
// the shape the session view reads. They are not a documented interface: a
// new Claude Code version can change them, and the view would go quiet
// without this check (smif doctor shows it).
func (p *Provider) Check() (state, detail string) {
	dir := p.sessionsDir()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		return "none", "no session files in " + dir + ": the session list comes from hooks only"
	}
	parsed, live := 0, 0
	var ids []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 64<<10 {
			continue
		}
		var s claudeSession
		if json.Unmarshal(b, &s) != nil || s.SessionID == "" || s.PID == 0 || s.Kind == "" {
			continue
		}
		parsed++
		if s.Kind == "interactive" && harness.ProcessAlive(s.PID) {
			live++
			if s.Status == "" {
				return "changed", "a live session file has no status: " + filepath.Base(f)
			}
			ids = append(ids, s.SessionID)
		}
	}
	if parsed == 0 {
		return "changed", fmt.Sprintf("none of %d session files has pid, sessionId and kind", len(files))
	}
	read, turns, steps, bad := 0, 0, 0, 0
	for _, id := range ids {
		path := p.transcriptPath(id)
		if path == "" {
			continue
		}
		read++
		ts, act := readTranscript(path, 4)
		turns += len(ts)
		// Tool calls (ADR-0014): each needs its id and name, or the step
		// that runs and its result can no longer be matched.
		for _, s := range act.Steps {
			steps++
			if s.ID == "" || s.Tool == "" {
				bad++
			}
		}
		if read == 3 {
			break
		}
	}
	switch {
	case read > 0 && turns == 0:
		return "changed", fmt.Sprintf("%d transcripts read, no prompt or answer found in them", read)
	case bad > 0:
		return "changed", fmt.Sprintf("%d of %d tool calls in the transcripts have no id or name: the live step cannot be read", bad, steps)
	}
	return "ok", fmt.Sprintf("%d live sessions, %d transcripts read, %d tool calls", live, read, steps)
}
