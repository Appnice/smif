package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// The herdr and Listed levels (ADR-0019). An agent with no provider still
// shows: a scan reads `herdr agent list` on every running herdr session and
// the process table. A scan calls external commands, so session.list reads
// its last result and a new scan runs beside it. Only a call that finds no
// recent result waits, and only a moment, so a first list is whole in the
// usual case and a hung herdr cannot stall the list.

const (
	scanEvery   = 2 * time.Second        // a result is fresh this long
	scanStale   = 3 * scanEvery          // an older result is worth a wait
	scanMaxWait = 200 * time.Millisecond // session.list stays under 300 ms
	scanTimeout = 5 * time.Second        // a hung herdr ends here
)

// scanRow is one agent a scan found.
type scanRow struct {
	level, harness, id, name, cwd string
	pid                           int
	pids                          []int // all the row's processes
	tty, term                     string
	turn                          string
	turnAt                        time.Time
}

// turnSeen is when a scan first saw a turn: herdr gives no time for it.
type turnSeen struct {
	turn string
	at   time.Time
}

// agentScan caches the scan. With run nil there is no scan.
type agentScan struct {
	run   func(context.Context) []scanRow
	procs func(context.Context) []proc // nil = the real process table

	mu      sync.Mutex
	rows    []scanRow
	at      time.Time
	running chan struct{} // closed when the running scan ends

	// Only the one running scan uses these.
	turns map[string]turnSeen
	cwds  map[string]string
}

// get returns the last result and starts a new scan when it is old. It
// waits for that scan only when the result is stale.
func (s *agentScan) get() []scanRow {
	if s.run == nil {
		return nil
	}
	s.mu.Lock()
	age := time.Since(s.at)
	if age < scanEvery {
		defer s.mu.Unlock()
		return s.rows
	}
	done := s.running
	if done == nil {
		done = make(chan struct{})
		s.running = done
		go s.refresh(done)
	}
	rows := s.rows
	s.mu.Unlock()
	if age < scanStale {
		return rows
	}
	select {
	case <-done:
	case <-time.After(scanMaxWait):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows
}

func (s *agentScan) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	rows := s.run(ctx)
	s.mu.Lock()
	s.rows, s.at, s.running = rows, time.Now(), nil
	s.mu.Unlock()
	close(done)
}

// expire makes the next get start a scan, after a prompt changed a state.
func (s *agentScan) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = time.Time{}
}

// runScan is one whole scan: herdr's agents, then the processes herdr does
// not hold.
func (d *Daemon) runScan(ctx context.Context) []scanRow {
	list := d.scan.procs
	if list == nil {
		list = listProcs
	}
	procs := list(ctx)
	rows := d.scan.herdrRows(ctx, procs)
	extra := d.Config().ListedProcesses()
	return append(rows, d.scan.listedRows(ctx, procs, rows, extra, func(pid int) string { return d.procs.get(pid, 0).term })...)
}

// addScanned adds the scanned rows that no earlier row owns: a pid or a
// tty in common means one session, so it shows once, at its best level.
func (d *Daemon) addScanned(rows []store.Presence, scan []scanRow) []store.Presence {
	pids, ttys := map[int]bool{}, map[string]bool{}
	for _, r := range rows {
		pids[r.PID] = true
		ttys[r.TTY] = true
	}
	delete(pids, 0)
	delete(ttys, "")
	now := time.Now()
	for _, s := range scan {
		if ttys[s.tty] || slices.ContainsFunc(s.pids, func(p int) bool { return pids[p] }) || !processAlive(s.pid) {
			continue
		}
		repo, ok := d.sessionRepo(s.cwd)
		if !ok {
			continue
		}
		rows = append(rows, store.Presence{Repo: repo, SessionID: s.id, State: "active", Harness: s.harness, Level: s.level,
			PID: s.pid, TTY: s.tty, Term: s.term, Cwd: filepath.Clean(s.cwd), Title: nonEmpty(s.name, s.harness),
			Turn: s.turn, TurnAt: s.turnAt, LastSeen: now})
		for _, p := range s.pids {
			pids[p] = true
		}
		if s.tty != "" {
			ttys[s.tty] = true
		}
	}
	return rows
}

// scanID is a row's session id. It stays the same from scan to scan, and
// passes harness.ValidID. Its first letter tells it from a real session id
// in the eight characters the UI shows.
func scanID(kind string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return kind + hex.EncodeToString(sum[:8])
}

// herdrRows reads every running herdr session. No herdr, no server or a
// failing call gives no rows and no error: the level is simply down
// (ADR-0019).
func (s *agentScan) herdrRows(ctx context.Context, procs []proc) []scanRow {
	if herdrPath() == "" {
		return nil
	}
	sessions, err := herdrSessions(ctx)
	if err != nil {
		return nil
	}
	var out []scanRow
	turns, now := map[string]turnSeen{}, time.Now()
	for _, session := range sessions {
		list, err := herdrRun(ctx, "--session", session, "agent", "list")
		if err != nil {
			continue
		}
		agents, err := parseHerdrAgents(list)
		if err != nil {
			continue
		}
		for _, a := range agents {
			// The foreground is what ties a pane to a process: without it
			// the row could double a full session.
			info, err := herdrRun(ctx, "--session", session, "pane", "process-info", "--pane", a.Pane)
			if err != nil {
				continue
			}
			pids := herdrForeground(info)
			if a.Agent == "" || len(pids) == 0 {
				continue
			}
			id := scanID("h", session, nonEmpty(a.Terminal, a.Pane))
			turn := herdrTurn(ctx, a.Agent, a.Status)
			seen, ok := s.turns[id]
			if !ok || seen.turn != turn {
				seen = turnSeen{turn: turn, at: now}
			}
			turns[id] = seen
			out = append(out, scanRow{level: harness.LevelHerdr, harness: oneLine(a.Agent, 40), id: id, name: oneLine(a.Name, 60),
				cwd: nonEmpty(a.ForegroundCwd, a.Cwd), pid: pids[0], pids: pids, tty: ttyOf(procs, pids), term: termHerdr,
				turn: turn, turnAt: seen.at})
		}
	}
	s.turns = turns
	return out
}

// listedRows are the processes on a terminal that the table of agent names
// or the config (extra, ADR-0021) knows, one for each agent and terminal (a
// wrapper and its child are one). A process with no terminal is a server or
// a daemon, no session.
func (s *agentScan) listedRows(ctx context.Context, procs []proc, herdr []scanRow, extra map[string]string, termOf func(int) string) []scanRow {
	var out []scanRow
	seen, cwds := map[string]bool{}, map[string]string{}
	for _, r := range herdr {
		seen[r.tty] = true
	}
	for _, p := range procs {
		name := harness.ListedProcessWith(p.argv, extra)
		key := name + "/" + p.tty
		if name == "" || p.tty == "" || seen[p.tty] || seen[key] {
			continue
		}
		seen[key] = true
		ckey := strconv.Itoa(p.pid) + "/" + strings.Join(p.argv, " ")
		cwd, ok := s.cwds[ckey]
		if !ok {
			cwd = harness.ProcCwd(ctx, p.pid)
		}
		cwds[ckey] = cwd
		if cwd == "" {
			continue
		}
		out = append(out, scanRow{level: harness.LevelListed, harness: name, id: scanID("p", strconv.Itoa(p.pid), name), cwd: cwd,
			pid: p.pid, pids: []int{p.pid}, tty: p.tty, term: termOf(p.pid), turn: "unknown"})
	}
	s.cwds = cwds
	return out
}

// proc is one line of the process table.
type proc struct {
	pid  int
	tty  string // "/dev/ttys004", "" with no terminal
	argv []string
}

func listProcs(ctx context.Context) []proc {
	out, err := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,tty=,args=").Output()
	if err != nil {
		return nil
	}
	return parseProcs(string(out))
}

func parseProcs(ps string) []proc {
	var out []proc
	sc := bufio.NewScanner(strings.NewReader(ps))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := proc{pid: pid, argv: f[2:]}
		if f[1] != "??" && f[1] != "?" {
			p.tty = "/dev/" + f[1]
		}
		out = append(out, p)
	}
	return out
}

// ttyOf is the terminal of the first of pids that has one.
func ttyOf(procs []proc, pids []int) string {
	for _, pid := range pids {
		for _, p := range procs {
			if p.pid == pid && p.tty != "" {
				return p.tty
			}
		}
	}
	return ""
}
