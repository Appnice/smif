package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/presence"
	"github.com/appnice/smif/internal/store"
)

// Session discovery (ADR-0012). Claude Code keeps one file for each running
// session in $CLAUDE_CONFIG_DIR/sessions/<pid>.json (default ~/.claude):
// its session id, cwd, name and status (idle, busy; shell while a shell it started runs). Smif reads these
// files, so every open session shows up at once, also one that had no hook
// since Smif was installed. The files are not a documented interface:
// a missing or changed file only means the hooks alone decide (docs/compat.md).

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

// claudeConfigDir is Claude Code's config folder ($CLAUDE_CONFIG_DIR, else
// ~/.claude).
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// claudeSessionsDir is where Claude Code keeps its session files.
func claudeSessionsDir() string {
	if d := claudeConfigDir(); d != "" {
		return filepath.Join(d, "sessions")
	}
	return ""
}

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// transcriptPath finds a session's transcript:
// <claude config>/projects/<cwd folder>/<session id>.jsonl, the newest one
// when a session was resumed from another folder.
func transcriptPath(sessionID string) string {
	dir := claudeConfigDir()
	if dir == "" || !sessionIDRe.MatchString(sessionID) {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(dir, "projects", "*", sessionID+".jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	return best
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
		if json.Unmarshal(b, &s) != nil || s.Kind != "interactive" || s.SessionID == "" || !processAlive(s.PID) {
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

// procInfo is where a claude process runs. It does not change while the
// process lives, so it is read once for each pid and start time.
type procInfo struct{ tty, term string }

type procCache struct {
	mu sync.Mutex
	m  map[string]procInfo
}

func (c *procCache) get(pid int, started int64) procInfo {
	key := strconv.Itoa(pid) + "/" + strconv.FormatInt(started, 10)
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.m[key]; ok {
		return p
	}
	if c.m == nil || len(c.m) > 256 {
		c.m = map[string]procInfo{}
	}
	p := readProcInfo(pid)
	c.m[key] = p
	return p
}

// readProcInfo reads the process's tty, then walks up its parents to name
// the terminal app: Terminal.app, iTerm2, or a tmux or herdr server (the
// nearest one wins, so tmux inside herdr counts as tmux).
func readProcInfo(pid int) procInfo {
	var p procInfo
	cur := pid
	for i := 0; i < 8 && cur > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=,tty=,comm=", "-p", strconv.Itoa(cur)).Output()
		if err != nil {
			return p
		}
		f := strings.Fields(string(out))
		if len(f) < 3 {
			return p
		}
		comm := filepath.Base(strings.Join(f[2:], " "))
		if i == 0 && f[1] != "??" && f[1] != "?" {
			p.tty = "/dev/" + f[1]
		}
		switch {
		case strings.HasPrefix(comm, "tmux"):
			p.term = "tmux"
			return p
		case comm == "herdr":
			p.term = termHerdr
			return p
		case comm == "Terminal":
			p.term = "Apple_Terminal"
			return p
		case strings.HasPrefix(comm, "iTerm"):
			p.term = "iTerm.app"
			return p
		}
		next, err := strconv.Atoi(f[0])
		if err != nil {
			return p
		}
		cur = next
	}
	return p
}

// mergeSessions joins the hook rows with the discovered sessions. For a
// discovered session the file gives the pid and name, ps the tty and
// terminal, and the newer of the file status and the hook turn wins. The
// hook row keeps the last prompt. Hook rows without a file count as before.
func (d *Daemon) mergeSessions(cfg *config.Config, rows []store.Presence, found []claudeSession) []store.Presence {
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.SessionID] = i
	}
	seen := map[string]bool{}
	var out []store.Presence
	for _, s := range found {
		// A session outside every agent's repo still shows (its folder
		// stands in for the repo): the human can read and prompt it.
		repo := gitRoot(filepath.Clean(s.Cwd))
		if a := d.agentForCwd(s.Cwd); a != nil {
			repo = a.RepoPath()
		} else if s.Cwd == "" || strings.HasPrefix(repo+"/", filepath.Join(d.dataDir, "worktrees")+"/") {
			continue
		}
		p := store.Presence{Repo: repo, SessionID: s.SessionID, State: "active"}
		if i, ok := byID[s.SessionID]; ok {
			p = rows[i]
		}
		seen[s.SessionID] = true
		pi := d.procs.get(s.PID, s.StartedAt)
		p.PID, p.Cwd = s.PID, filepath.Clean(s.Cwd)
		if pi.tty != "" || p.TTY == "" {
			p.TTY = pi.tty
		}
		if pi.term != "" || p.Term == "" {
			p.Term = pi.term
		}
		if s.Name != "" {
			p.Title = s.Name
		}
		at := time.UnixMilli(s.StatusUpdatedAt)
		if t := fileTurn(s.Status); t != "" && (p.TurnAt.IsZero() || !at.Before(p.TurnAt)) {
			p.Turn, p.TurnAt = t, at
		}
		if at.After(p.LastSeen) {
			p.LastSeen = at
		}
		out = append(out, p)
	}
	for _, r := range rows {
		if !seen[r.SessionID] {
			out = append(out, r)
		}
	}
	return out
}

// answerCache keeps each session's last answer and activity, read again
// only when its transcript changes (session.list runs on every UI refresh).
type answerCache struct {
	mu sync.Mutex
	m  map[string]answerEntry
}

type answerEntry struct {
	path      string
	size      int64
	mod       time.Time
	info      transcriptInfo
	checkedAt time.Time
}

// transcriptInfo is what session.list shows from a transcript.
type transcriptInfo struct {
	answer   string
	answerAt time.Time
	act      presence.Activity
	wroteAt  time.Time
	ok       bool // a transcript was read
}

// get returns the final message of the session's newest answer and what
// the newest exchange does now.
func (c *answerCache) get(sessionID string) transcriptInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 512 {
		c.m = map[string]answerEntry{}
	}
	e, ok := c.m[sessionID]
	if !ok || e.path == "" && time.Since(e.checkedAt) > 30*time.Second {
		e = answerEntry{path: transcriptPath(sessionID), checkedAt: time.Now()}
	}
	if e.path == "" {
		c.m[sessionID] = e
		return transcriptInfo{}
	}
	st, err := os.Stat(e.path)
	if err != nil {
		delete(c.m, sessionID)
		return transcriptInfo{}
	}
	if st.Size() != e.size || !st.ModTime().Equal(e.mod) {
		e.size, e.mod = st.Size(), st.ModTime()
		turns, act := presence.Read(e.path, 4)
		text, at := presence.LastAnswer(turns)
		e.info = transcriptInfo{answer: text, answerAt: at, act: act, wroteAt: st.ModTime(), ok: true}
	}
	c.m[sessionID] = e
	return e.info
}

// gitRoot is the nearest folder at or above dir that holds .git (a folder
// or a worktree's file), else dir itself.
func gitRoot(dir string) string {
	home, _ := os.UserHomeDir()
	for d := dir; d != "/" && d != "." && d != home; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	return dir
}

// sessionsCanary checks that Claude Code's session files and transcripts
// still have the shape the session view reads. They are not a documented
// interface: a new Claude Code version can change them, and the view would
// go quiet without this check (smif doctor shows it).
func (d *Daemon) sessionsCanary() (state, detail string) {
	files, _ := filepath.Glob(filepath.Join(d.sessDir, "*.json"))
	if len(files) == 0 {
		return "none", "no session files in " + d.sessDir + ": the session list comes from hooks only"
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
		if s.Kind == "interactive" && processAlive(s.PID) {
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
		p := transcriptPath(id)
		if p == "" {
			continue
		}
		read++
		ts, act := presence.Read(p, 4)
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
