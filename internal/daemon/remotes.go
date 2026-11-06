package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/api"
)

// Other machines (ADR-0023): a remote is an ssh host that runs smif too. The
// daemon asks it for its session list and shows the rows here, read only.
const (
	remoteEvery   = 15 * time.Second
	remoteTimeout = 12 * time.Second
	remoteStale   = time.Minute // rows older than this leave the list
	// remoteCommand runs in a non-login shell, which has no ~/.local/bin.
	remoteCommand = `PATH="$HOME/.local/bin:$PATH" smif sessions --json`
)

type remote struct {
	rows []api.SessionView
	at   time.Time
	err  string
}

type remoteState struct {
	mu    sync.Mutex
	hosts map[string]remote
}

func sshCommand() string {
	if p, ok := os.LookupEnv("SMIF_SSH"); ok {
		return p
	}
	return "ssh"
}

// remoteArgv is the ssh call. The host passed config validation, and -- ends
// the options. A shared connection keeps a poll cheap.
func remoteArgv(host string) []string {
	return []string{sshCommand(), "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/smif-%C", "-o", "ControlPersist=60", "--", host, remoteCommand}
}

// fetchRemote asks one host for its sessions.
func fetchRemote(ctx context.Context, host string) ([]api.SessionView, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	argv := remoteArgv(host)
	var errb strings.Builder
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stderr = &errb
	out, err := c.Output()
	if err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return nil, errors.New(lastErrLine(msg))
		}
		return nil, err
	}
	var rows []api.SessionView
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("no session list: %v", err)
	}
	for i := range rows {
		v := &rows[i]
		v.Host = host
		v.SessionID = host + "/" + v.SessionID
		v.Repo = host + ":" + v.Repo
		v.Term, v.TTY, v.Queue = "ssh", "", nil
		v.CanPrompt, v.Why = false, "on "+host+": read only"
	}
	return rows, nil
}

func lastErrLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// remoteLoop polls the configured hosts until the daemon stops.
func (d *Daemon) remoteLoop() {
	t := time.NewTicker(remoteEvery)
	defer t.Stop()
	for {
		d.pollRemotes()
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) pollRemotes() {
	var wg sync.WaitGroup
	for _, host := range d.Config().Daemon.Remotes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := fetchRemote(d.ctx, host)
			d.remotes.mu.Lock()
			defer d.remotes.mu.Unlock()
			if d.remotes.hosts == nil {
				d.remotes.hosts = map[string]remote{}
			}
			r := d.remotes.hosts[host]
			if err != nil {
				r.err = err.Error()
			} else {
				r = remote{rows: rows, at: time.Now()}
			}
			d.remotes.hosts[host] = r
		}()
	}
	wg.Wait()
}

// remoteRows is the rows of the configured hosts that are still fresh.
func (d *Daemon) remoteRows(now time.Time) []api.SessionView {
	d.remotes.mu.Lock()
	defer d.remotes.mu.Unlock()
	var out []api.SessionView
	for _, host := range d.Config().Daemon.Remotes {
		if r := d.remotes.hosts[host]; now.Sub(r.at) < remoteStale {
			out = append(out, r.rows...)
		}
	}
	return out
}

// remoteChecks is smif doctor's line for each host. A host that is away is
// news, never a failure.
func (d *Daemon) remoteChecks() []api.ProviderCheck {
	d.remotes.mu.Lock()
	defer d.remotes.mu.Unlock()
	out := []api.ProviderCheck{}
	for _, host := range d.Config().Daemon.Remotes {
		r, c := d.remotes.hosts[host], api.ProviderCheck{Name: host, State: "ok"}
		c.Detail = fmt.Sprintf("%d sessions", len(r.rows))
		if r.err != "" {
			c.State, c.Detail = "none", r.err
		} else if r.at.IsZero() {
			c.State, c.Detail = "none", "no answer yet"
		}
		out = append(out, c)
	}
	return out
}
