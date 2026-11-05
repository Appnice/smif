package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// procInfo is where a session's process runs. It does not change while the
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

// sessionRepo is the repo a session in cwd counts under. A session outside
// every agent's repo still shows (its folder stands in for the repo): the
// human can read and prompt it. Smif's own worktrees do not show.
func (d *Daemon) sessionRepo(cwd string) (string, bool) {
	if a := d.agentForCwd(cwd); a != nil {
		return a.RepoPath(), true
	}
	repo := gitRoot(filepath.Clean(cwd))
	if cwd == "" || strings.HasPrefix(repo+"/", filepath.Join(d.dataDir, "worktrees")+"/") {
		return "", false
	}
	return repo, true
}

// mergeSessions joins the hook rows with the discovered sessions. For a
// discovered session the provider gives the pid and name, ps the tty and
// terminal, and the newer of the provider's state and the hook turn wins.
// The hook row keeps the last prompt. Hook rows without a discovered session
// count as before.
func (d *Daemon) mergeSessions(cfg *config.Config, rows []store.Presence, found []harness.Session) []store.Presence {
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.SessionID] = i
	}
	seen := map[string]bool{}
	var out []store.Presence
	for _, s := range found {
		repo, ok := d.sessionRepo(s.Cwd)
		if !ok {
			continue
		}
		p := store.Presence{Repo: repo, SessionID: s.SessionID, State: "active"}
		if i, ok := byID[s.SessionID]; ok {
			p = rows[i]
		}
		seen[s.SessionID] = true
		pi := d.procs.get(s.PID, s.StartedAt)
		p.Harness, p.PID, p.Cwd = s.Harness, s.PID, filepath.Clean(s.Cwd)
		if pi.tty != "" || p.TTY == "" {
			p.TTY = pi.tty
		}
		if pi.term != "" || p.Term == "" {
			p.Term = pi.term
		}
		if s.Name != "" {
			p.Title = s.Name
		}
		if st, ok := d.harnesses.Get(s.Harness).(harness.Stater); ok {
			if t, at := st.State(s); t != "" && (p.TurnAt.IsZero() || !at.Before(p.TurnAt)) {
				p.Turn, p.TurnAt = t, at
			}
		}
		if s.StatusAt.After(p.LastSeen) {
			p.LastSeen = s.StatusAt
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
// only when the provider's stamp changes (session.list runs on every UI
// refresh).
type answerCache struct {
	mu sync.Mutex
	m  map[string]answerEntry
}

type answerEntry struct {
	stamp string
	info  transcriptInfo
}

// transcriptInfo is what session.list shows from a session's source.
type transcriptInfo struct {
	answer   string
	answerAt time.Time
	act      harness.Activity
	wroteAt  time.Time
	ok       bool // a source was read
}

// answerTurns is how many turns session.list reads.
const answerTurns = 4

func readInfo(p harness.Provider, sessionID string) transcriptInfo {
	r := p.Read(sessionID, answerTurns)
	text, at := harness.LastAnswer(r.Turns)
	return transcriptInfo{answer: text, answerAt: at, act: r.Activity, wroteAt: r.WroteAt, ok: r.OK}
}

// get returns the final message of the session's newest answer and what
// the newest exchange does now. p is nil for a harness with no provider.
func (c *answerCache) get(p harness.Provider, sessionID string) transcriptInfo {
	if p == nil {
		return transcriptInfo{}
	}
	sp, ok := p.(harness.Stamper)
	if !ok {
		return readInfo(p, sessionID)
	}
	key := p.Info().Name + "/" + sessionID
	stamp := sp.Stamp(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 512 {
		c.m = map[string]answerEntry{}
	}
	if stamp == "" {
		delete(c.m, key)
		return transcriptInfo{}
	}
	e := c.m[key]
	if e.stamp != stamp {
		e = answerEntry{stamp: stamp, info: readInfo(p, sessionID)}
		c.m[key] = e
	}
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

// sessionsCanary asks the verified providers whether the agents' files
// still have the shape they read. The first state that is not ok wins.
func (d *Daemon) sessionsCanary() (state, detail string) {
	for _, p := range d.harnesses.All() {
		c, ok := p.(harness.Checker)
		if !ok || !p.Info().Verified {
			continue
		}
		state, detail = c.Check()
		if state != "ok" {
			return state, detail
		}
	}
	return state, detail
}

// providerChecks is smif doctor's line for each provider. No real session
// checked an unverified provider, so a difference from the source reading
// can be a fault in the reading: its format is unchecked, never a failure.
func (d *Daemon) providerChecks() []api.ProviderCheck {
	out := []api.ProviderCheck{}
	for _, p := range d.harnesses.All() {
		c := api.ProviderCheck{Name: p.Info().Name, Verified: p.Info().Verified, State: "none"}
		if ck, ok := p.(harness.Checker); ok {
			c.State, c.Detail = ck.Check()
		}
		if !c.Verified {
			switch c.State {
			case "ok":
				c.Detail = "these files match the source reading: " + c.Detail
			case "changed":
				c.Detail = "these files differ from the source reading: " + c.Detail
			}
			c.State = "unchecked"
		}
		out = append(out, c)
	}
	return out
}
