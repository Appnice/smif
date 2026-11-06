package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/store"
)

// Facts next to an answer (ADR-0021): the transcript part comes from the
// provider's Factser through the answer cache, the rest from git here.

// gitStatusWait caps one `git status`.
const gitStatusWait = 2 * time.Second

// factsView is a session's facts. session.list and the status line both
// come through here, so git never runs in the caller: the view shows what
// the last read found.
func (d *Daemon) factsView(cfg *config.Config, p store.Presence, ti transcriptInfo, turn string) *api.SessionFacts {
	out := &api.SessionFacts{}
	if ti.facts != nil {
		out.Files, out.Commits, out.Tests = ti.facts.Files, ti.facts.Commits, ti.facts.Tests
		out.ContextTokens = ti.facts.ContextTokens
	}
	if warn, _ := config.ContextWarnTokens(cfg.Daemon.ContextWarn); warn > 0 {
		out.ContextHigh = out.ContextTokens > warn
	}
	if ti.stamp != "" {
		out.Dirty = d.dirty.get(p.SessionID, ti.stamp, nonEmpty(p.Cwd, p.Repo), turn != "busy")
	}
	if ti.facts == nil && out.Dirty == nil {
		return nil
	}
	return out
}

// dirtyCache keeps each session's uncommitted paths. It starts one read in
// the background when the transcript stamp changed and the session is not
// busy, so a slow git holds up no caller.
type dirtyCache struct {
	mu sync.Mutex
	m  map[string]*dirtyEntry
}

type dirtyEntry struct {
	stamp   string // the transcript stamp of the last read
	count   *int   // nil when that read gave nothing
	running bool
}

// get gives the last known count and starts a new read when one is due.
func (c *dirtyCache) get(sessionID, stamp, dir string, read bool) *int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 512 {
		c.m = map[string]*dirtyEntry{}
	}
	e := c.m[sessionID]
	if e == nil {
		e = &dirtyEntry{}
		c.m[sessionID] = e
	}
	if read && dir != "" && !e.running && e.stamp != stamp {
		e.stamp, e.running = stamp, true
		go func() {
			n := gitDirty(dir)
			c.mu.Lock()
			defer c.mu.Unlock()
			e.count, e.running = n, false
		}()
	}
	return e.count
}

// gitDirty counts the changed paths in dir. Any failure gives nil: no git,
// no repo, a timeout. The optional-locks setting keeps it from taking
// index.lock while the agent or the human works in the same tree.
func gitDirty(dir string) *int {
	ctx, cancel := context.WithTimeout(context.Background(), gitStatusWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = 200 * time.Millisecond // a child that keeps stdout open
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	n := bytes.Count(b, []byte("\n"))
	return &n
}
