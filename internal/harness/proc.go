package harness

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Proc is a process that may be an agent's session.
type Proc struct {
	PID   int
	TTY   string
	Argv  []string
	Start time.Time
	Cwd   string
}

// ParsePS reads `ps -ax -o pid=,tty=,lstart=,args=` in the C locale.
func ParsePS(out string) []Proc {
	var procs []Proc
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p := Proc{PID: pid, Start: ParseLstart(f[2:7]), Argv: f[7:]}
		if f[1] != "??" && f[1] != "?" {
			p.TTY = "/dev/" + f[1]
		}
		procs = append(procs, p)
	}
	return procs
}

// ProcLister gives a function that lists the processes keep accepts, each
// with its folder. It remembers the folders, because lsof is slow.
func ProcLister(keep func(Proc) bool) func() []Proc {
	cwds := map[string]string{}
	return func() []Proc {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ps", "-ax", "-o", "pid=,tty=,lstart=,args=")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var procs []Proc
		seen := map[string]string{}
		for _, p := range ParsePS(string(out)) {
			if !keep(p) {
				continue
			}
			key := strconv.Itoa(p.PID) + "/" + strconv.FormatInt(p.Start.Unix(), 10)
			cwd, ok := cwds[key]
			if !ok {
				cwd = ProcCwd(ctx, p.PID)
			}
			seen[key] = cwd
			p.Cwd = cwd
			procs = append(procs, p)
		}
		cwds = seen
		return procs
	}
}

// ArgWords are argv's words that are no flag, after the program and any
// wrapper's script. A flag's value counts as a word.
func ArgWords(argv []string) []string {
	var out []string
	base := filepath.Base(argv[0])
	skip := base == "node" || base == "bun"
	for _, a := range argv[1:] {
		switch {
		case skip && !strings.HasPrefix(a, "-"):
			skip = false
		case !skip && !strings.HasPrefix(a, "-"):
			out = append(out, a)
		}
	}
	return out
}

// SameDir compares folders after symlinks: lsof reports /private/tmp where
// an agent may have written /tmp.
func SameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, rb := a, b
	if r, err := filepath.EvalSymlinks(a); err == nil {
		ra = r
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		rb = r
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

// Candidate is a session that a process can own, for an agent that keeps
// no file to link a pid to a session.
type Candidate struct {
	ID      string
	Cwd     string
	Path    string // the transcript, for an agent that keeps files
	Name    string
	Created time.Time
	Wrote   time.Time
}

// Owned is a process and the session it owns.
type Owned struct {
	Proc    Proc
	Session Candidate
}

// MatchProcs ties processes to sessions. A process that names a session in
// argv (named, may be nil) owns it. Each other process owns the session in
// its folder that was written since it started, newest process to newest
// session, which can swap two processes in one folder when the newer one
// resumed an older session. A process with no session gets no match.
func MatchProcs(procs []Proc, found []Candidate, named func(Proc) string) []Owned {
	procs = slices.Clone(procs)
	slices.SortFunc(procs, func(a, b Proc) int { return b.Start.Compare(a.Start) })
	var out []Owned
	claimed := make([]bool, len(found))
	take := func(pr Proc, i int) {
		claimed[i] = true
		out = append(out, Owned{Proc: pr, Session: found[i]})
	}
	// best is the unclaimed session that better wins, -1 for none.
	best := func(ok func(c Candidate) bool, better func(a, b Candidate) bool) int {
		at := -1
		for i, c := range found {
			if !claimed[i] && ok(c) && (at < 0 || better(c, found[at])) {
				at = i
			}
		}
		return at
	}
	var rest []Proc
	for _, pr := range procs {
		id := ""
		if named != nil {
			id = named(pr)
		}
		i := -1
		if id != "" {
			i = best(func(c Candidate) bool { return c.ID == id }, func(a, b Candidate) bool { return a.Wrote.After(b.Wrote) })
		}
		if i < 0 {
			rest = append(rest, pr)
			continue
		}
		take(pr, i)
	}
	for _, pr := range rest {
		if pr.Start.IsZero() {
			continue
		}
		i := best(func(c Candidate) bool { return !c.Wrote.Before(pr.Start) && SameDir(c.Cwd, pr.Cwd) },
			func(a, b Candidate) bool { return a.Created.After(b.Created) })
		if i >= 0 {
			take(pr, i)
		}
	}
	return out
}

// ProcCwd is a process's working folder ("" when it cannot be read).
func ProcCwd(ctx context.Context, pid int) string {
	if runtime.GOOS == "linux" {
		dir, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
		return dir
	}
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			return l[1:]
		}
	}
	return ""
}

// ProcLook reads one process with ps: its terminal, start and argv. ok is
// false when it cannot be read. Cwd stays empty.
func ProcLook(pid int) (p Proc, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "pid=,tty=,lstart=,args=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return p, false
	}
	found := ParsePS(string(out))
	if len(found) != 1 {
		return p, false
	}
	return found[0], true
}

// ParseLstart reads the five words of `ps -o lstart=` in the C locale. A
// zero time means it cannot be read.
func ParseLstart(words []string) time.Time {
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(words, " "), time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ProcStart is when a process started, to the second. A zero time means it
// cannot be read.
func ProcStart(pid int) time.Time {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}
	}
	return ParseLstart(strings.Fields(string(out)))
}
