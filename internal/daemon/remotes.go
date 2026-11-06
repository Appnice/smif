package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/paths"
)

// Other machines (ADR-0023): a remote is an ssh host that runs smif too. The
// daemon asks it for its session list and shows the rows here, read only.
const (
	remoteEvery   = 15 * time.Second
	remoteTimeout = 12 * time.Second
	remoteStale   = time.Minute // rows older than this leave the list
	// RemoteCommand runs in a non-login shell, which has no ~/.local/bin.
	RemoteCommand = `PATH="$HOME/.local/bin:$PATH" smif sessions --json`
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

// SSHCommand is the ssh binary.
func SSHCommand() string {
	if p, ok := os.LookupEnv("SMIF_SSH"); ok {
		return p
	}
	return "ssh"
}

// RemoteKey is the daemon's own key in ~/.ssh (ADR-0024). The other machine
// lets it run the session list and nothing else.
const RemoteKey = "smif_ed25519"

// RemoteArgv is the ssh call. The host passed config validation, and -- ends
// the options. A shared connection keeps a poll cheap.
func RemoteArgv(host, command string) []string {
	argv := []string{SSHCommand(), "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/smif-%C", "-o", "ControlPersist=60"}
	if key := filepath.Join(paths.Home(), ".ssh", RemoteKey); fileExists(key) {
		argv = append(argv, "-i", key)
	}
	return append(argv, "--", host, command)
}

// RemoteDisconnect closes the daemon's shared connection to a host. sshd reads
// a key's line at login, so a connection that stays open keeps the old rights:
// after a change of the key, the next poll must log in again.
func RemoteDisconnect(host string) {
	_ = exec.Command(SSHCommand(), "-o", "ControlPath=~/.ssh/smif-%C", "-O", "exit", "--", host).Run()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fetchRemote asks one host for its sessions.
func fetchRemote(ctx context.Context, host, name string, write bool) ([]api.SessionView, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	argv := RemoteArgv(host, RemoteCommand)
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
	rows = ownRows(rows)
	for i := range rows {
		v := &rows[i]
		label := host
		if name != "" {
			label = name
		}
		v.Host, v.HostName = host, name
		v.SessionID = host + "/" + v.SessionID
		v.Repo = label + ":" + v.Repo
		v.Term, v.TTY = "ssh", ""
		if v.Writable = write; !write {
			v.Queue = nil
			v.CanPrompt, v.Why = false, "on "+label+": read only"
		}
	}
	return rows, nil
}

// ownRows keeps a host's own sessions. Its list also holds the machines it
// watches, and those can be this one, or the host itself: taking them would
// repeat each row on every poll, with no end.
func ownRows(rows []api.SessionView) []api.SessionView {
	own := rows[:0]
	for _, v := range rows {
		if v.Host == "" {
			own = append(own, v)
		}
	}
	return own
}

// remoteSession splits a remote row's id into its host and its id there.
func (d *Daemon) remoteSession(id string) (host, rest string, ok bool) {
	for _, h := range d.Config().Daemon.Remotes {
		if r, found := strings.CutPrefix(id, h+"/"); found {
			return h, r, true
		}
	}
	return "", "", false
}

// remoteWrite passes a line to that machine's daemon, on stdin, so the text
// never reaches a command line. That daemon's own gates decide: it types only
// into an idle session with claude in front, and it never answers a prompt.
func (d *Daemon) remoteWrite(host, request, text string) ([]byte, error) {
	if !slices.Contains(d.Config().Daemon.RemoteWrite, host) {
		return nil, api.Errf(api.CodeRejected, "%s is read only. Press w on it under Machines (m) to allow writing", host)
	}
	ctx, cancel := context.WithTimeout(d.ctx, 20*time.Second)
	defer cancel()
	argv := RemoteArgv(host, `PATH="$HOME/.local/bin:$PATH" smif remote-read `+request)
	var errb strings.Builder
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin = strings.NewReader(text)
	c.Stderr = &errb
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimPrefix(strings.TrimSpace(errb.String()), "smif: ")
		if msg == "" {
			msg = err.Error()
		}
		return nil, api.Errf(api.CodeRejected, "%s", lastErrLine(msg))
	}
	return out, nil
}

// remoteTail reads a conversation on another machine. The id passed
// harness.ValidID, and it is the only value in the command.
func remoteTail(ctx context.Context, host, id string, limit int) (*api.SessionTail, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	argv := RemoteArgv(host, fmt.Sprintf(`PATH="$HOME/.local/bin:$PATH" smif remote-read tail %s %d`, id, limit))
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
	var tail api.SessionTail
	if json.Unmarshal(out, &tail) != nil {
		return nil, fmt.Errorf("%s has an older setup. Press enter on it under Machines (m) to update it", host)
	}
	return &tail, nil
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
			rows, err := fetchRemote(d.ctx, host, d.Config().Daemon.RemoteNames[host], slices.Contains(d.Config().Daemon.RemoteWrite, host))
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
		r, c := d.remotes.hosts[host], api.ProviderCheck{Name: host, Title: d.Config().Daemon.RemoteNames[host], State: "ok",
			Write: slices.Contains(d.Config().Daemon.RemoteWrite, host)}
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
