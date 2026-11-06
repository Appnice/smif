// Package machines adds another machine to the session list (ADR-0024): it
// finds candidates, and it runs the one setup both `smif remote add` and the
// Machines screen use.
//
// The password goes to ssh and sudo and nowhere else. The daemon never uses
// your own key: smif makes one that can only read sessions there.
package machines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/daemon"
)

// Step is one line of the checklist.
type Step string

const (
	StepReach  Step = "reach"
	StepLogin  Step = "login"
	StepSystem Step = "system"
	StepSmif   Step = "smif"
	StepKey    Step = "key"
	StepLinger Step = "linger"
	StepConfig Step = "config"
	StepDaemon Step = "daemon"
)

// Steps is the checklist in order.
var Steps = []struct {
	ID    Step
	Label string
}{
	{StepReach, "Reach ssh"},
	{StepLogin, "Log in"},
	{StepSystem, "Detect the system"},
	{StepSmif, "Install smif"},
	{StepKey, "Add a key that can only read sessions"},
	{StepLinger, "Keep smif running after logout"},
	{StepConfig, "Add it to your list"},
	{StepDaemon, "Wait for your daemon to connect"},
}

// State is where a step is.
type State int

const (
	Waiting State = iota
	Running
	Done
	Skipped
	Failed
)

// Event says that a step changed.
type Event struct {
	Step   Step
	State  State
	Detail string
}

// Asker answers the questions only a person can. Each call blocks. False
// stops the setup, except Sudo, where false skips the step.
type Asker interface {
	// Closed says ssh does not answer on that machine. The setup keeps trying.
	Closed(name string)
	// Login asks for a password. problem is why it asks again, or empty.
	Login(user, problem string) (string, string, bool)
	// Sudo asks for the sudo password. problem is why it asks again, or empty.
	Sudo(problem string) (string, bool)
	// Install asks to install smif on that machine.
	Install() bool
}

// Daemon is the running daemon, which must reach the machine by itself.
type Daemon interface {
	Reload() error
	// Reached reports the daemon's last look at a host.
	Reached(host string) (ok bool, detail string, err error)
}

// Setup adds one machine. Set Host, Ask, Report and Daemon.
type Setup struct {
	Host   string // an ssh alias or user@host
	Write  bool   // you may type into that machine's sessions
	Ask    Asker
	Report func(Event)
	Daemon Daemon

	retry time.Duration
	await time.Duration
}

const (
	retryEvery = 3 * time.Second
	awaitFirst = 30 * time.Second // a poll comes every 15 s and may take 12
	loginTries = 3
)

// Remote commands. They are constants, and a host name is the only value from
// you that reaches ssh.
const (
	remoteLookup  = `PATH="$HOME/.local/bin:$PATH" command -v smif || true`
	remoteInstall = `curl -fsSL https://smif.dev/install.sh | sh`
	remoteSystem  = `uname -s; uname -m; (. /etc/os-release 2>/dev/null; echo "$PRETTY_NAME"); sw_vers -productVersion 2>/dev/null; true`
	remoteLinger  = `if command -v loginctl >/dev/null; then loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo unknown; else echo none; fi`
	remoteEnable  = `loginctl --no-ask-password enable-linger "$USER" 2>/dev/null; loginctl show-user "$USER" -p Linger --value 2>/dev/null`
	remoteSudo    = `sudo -S -p "" loginctl enable-linger "$USER"`
	remoteCheck   = `PATH="$HOME/.local/bin:$PATH" smif remote-read check`
	// remoteKey reads the line on stdin, so a key never shows in a process
	// list. It replaces an older line of the same key and no other.
	remoteKey = `k=$(cat); b=$(printf '%s' "$k" | sed 's/.*ssh-ed25519 /ssh-ed25519 /'); f=~/.ssh/authorized_keys; umask 077; mkdir -p ~/.ssh && touch $f && { grep -vF "$b" $f > $f.smif; printf '%s\n' "$k" >> $f.smif; mv $f.smif $f; }`
)

// The command to run on a machine with ssh off. smif cannot do this from here.
const (
	CommandLinux = "sudo apt install -y openssh-server && sudo systemctl enable --now ssh"
	CommandMac   = "sudo systemsetup -setremotelogin on"
)

var cleanPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// ErrStopped is a setup you stopped.
var ErrStopped = errors.New("stopped")

// Run does the setup. A failed step has told Report why.
func (s *Setup) Run(ctx context.Context) error {
	if !config.ValidRemote(s.Host) {
		return fmt.Errorf("%q is not an ssh host name", s.Host)
	}
	if s.retry == 0 {
		s.retry = retryEvery
	}
	if s.await == 0 {
		s.await = awaitFirst
	}
	if err := os.MkdirAll(filepath.Join(home(), ".ssh"), 0o700); err != nil {
		return s.fail(StepReach, err)
	}
	s.exit()
	defer s.exit()
	if err := s.login(ctx); err != nil {
		return err
	}
	linux, err := s.system(ctx)
	if err != nil {
		return err
	}
	path, err := s.smif(ctx)
	if err != nil {
		return err
	}
	if err := s.key(ctx, path); err != nil {
		return err
	}
	if linux {
		if err := s.linger(ctx); err != nil {
			return err
		}
	} else {
		s.emit(StepLinger, Done, "not needed")
	}
	return s.finish()
}

func (s *Setup) emit(step Step, st State, detail string) {
	if s.Report != nil {
		s.Report(Event{step, st, detail})
	}
}

func (s *Setup) fail(step Step, err error) error {
	s.emit(step, Failed, err.Error())
	return err
}

// login leaves one shared connection open, so the password is typed once.
func (s *Setup) login(ctx context.Context) error {
	s.emit(StepReach, Running, s.Host)
	if isThisMachine(ctx, s.Host) {
		return s.fail(StepReach, errors.New("that is this machine, and its sessions are already in your list"))
	}
	closed := false
	for {
		err := s.connect(ctx, nil)
		if err == nil {
			s.emit(StepReach, Done, s.Host)
			s.emit(StepLogin, Done, "your ssh login works")
			return nil
		}
		if ctx.Err() != nil {
			return ErrStopped
		}
		var denied *errDenied
		if errors.As(err, &denied) {
			break
		}
		if !isAway(err) {
			return s.fail(StepReach, err)
		}
		if !closed {
			closed = true
			s.Ask.Closed(nameOf(s.Host))
		}
		select {
		case <-ctx.Done():
			return ErrStopped
		case <-time.After(s.retry):
		}
	}
	s.emit(StepReach, Done, s.Host)
	s.emit(StepLogin, Running, "")
	proposed := userOf(s.Host)
	if proposed == "" {
		proposed = resolve(ctx, s.Host)["user"]
	}
	problem := ""
	for try := 0; try < loginTries; try++ {
		user, pass, ok := s.Ask.Login(proposed, problem)
		if !ok {
			return ErrStopped
		}
		if user != "" && user != proposed {
			s.Host = user + "@" + nameOf(s.Host)
			proposed = user
		}
		env, stop, err := servePassword(pass)
		if err != nil {
			return s.fail(StepLogin, err)
		}
		err = s.connect(ctx, env)
		stop()
		if err == nil {
			s.emit(StepLogin, Done, "password used once, not stored")
			return nil
		}
		if ctx.Err() != nil {
			return ErrStopped
		}
		var denied *errDenied
		if !errors.As(err, &denied) {
			return s.fail(StepLogin, err)
		}
		problem = "That user and password did not work."
	}
	return s.fail(StepLogin, errors.New("the login did not work 3 times"))
}

type errDenied struct{ text string }

func (e *errDenied) Error() string { return e.text }

type errAway struct{ text string }

func (e *errAway) Error() string { return e.text }

func isAway(err error) bool {
	var a *errAway
	return errors.As(err, &a)
}

// classify turns ssh's words into what to do next.
func classify(out string, err error) error {
	line := lastLine(out, err)
	switch {
	case containsAny(out, "Permission denied", "Too many authentication failures", "No supported authentication methods"):
		return &errDenied{line}
	case containsAny(out, "IDENTIFICATION HAS CHANGED", "Host key verification failed"):
		return fmt.Errorf("the host key changed, so smif stopped. If you expected that, run `ssh-keygen -R <host>`")
	case containsAny(out, "Could not resolve hostname", "Name or service not known", "nodename nor servname"):
		return fmt.Errorf("no machine has that name: %s", line)
	case containsAny(out, "Connection refused", "timed out", "No route to host", "Network is unreachable", "Connection reset", "Connection closed by", "kex_exchange_identification"):
		return &errAway{line}
	}
	return errors.New(line)
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func lastLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return err.Error()
	}
	return out[strings.LastIndex(out, "\n")+1:]
}

func (s *Setup) control() []string {
	return []string{"-o", "ControlPath=" + filepath.Join(home(), ".ssh", "smif-setup-%C")}
}

// connect opens the shared connection. env carries a password, or nil for a
// login that asks for nothing. An unknown host key is accepted once, as ssh
// asks you to; a changed one is refused.
func (s *Setup) connect(ctx context.Context, env []string) error {
	argv := append([]string{"-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=accept-new", "-o", "ControlMaster=yes", "-o", "ControlPersist=300"}, s.control()...)
	if env == nil {
		argv = append(argv, "-o", "BatchMode=yes")
	} else {
		argv = append(argv, "-o", "PreferredAuthentications=keyboard-interactive,password", "-o", "NumberOfPasswordPrompts=1")
	}
	argv = append(argv, "-f", "-N", "--", s.Host)
	// -f leaves a process behind that holds stderr, so it cannot be a pipe.
	f, err := os.CreateTemp("", "smif-ssh-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(cctx, daemon.SSHCommand(), argv...)
	c.Stderr = f
	c.Env = append(os.Environ(), env...)
	if err := c.Run(); err != nil {
		b, _ := os.ReadFile(f.Name())
		return classify(string(b), err)
	}
	return nil
}

// exit closes the shared connection.
func (s *Setup) exit() {
	argv := append(s.control(), "-O", "exit", "--", s.Host)
	c := exec.Command(daemon.SSHCommand(), argv...)
	_ = c.Run()
}

// run does one command over the shared connection. stdin stays out of ps.
func (s *Setup) run(ctx context.Context, stdin, command string) (string, error) {
	argv := append([]string{"-o", "BatchMode=yes", "-o", "ControlMaster=no"}, s.control()...)
	argv = append(argv, "--", s.Host, command)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(cctx, daemon.SSHCommand(), argv...)
	c.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), errors.New(lastLine(errb.String(), err))
	}
	return out.String(), nil
}

// system reports a Linux machine.
func (s *Setup) system(ctx context.Context) (bool, error) {
	s.emit(StepSystem, Running, "")
	out, err := s.run(ctx, "", remoteSystem)
	if err != nil {
		return false, s.fail(StepSystem, err)
	}
	f := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(f) < 2 {
		return false, s.fail(StepSystem, fmt.Errorf("uname said %q", out))
	}
	name := f[0]
	if len(f) > 2 && strings.TrimSpace(f[2]) != "" {
		name = f[2]
	} else if f[0] == "Darwin" {
		name = "macOS"
		if len(f) > 3 {
			name += " " + f[3]
		}
	}
	s.emit(StepSystem, Done, name+" · "+f[1])
	return f[0] == "Linux", nil
}

// smif leaves smif on the machine and returns where it is.
func (s *Setup) smif(ctx context.Context) (string, error) {
	s.emit(StepSmif, Running, "")
	lookup := func() string {
		out, _ := s.run(ctx, "", remoteLookup)
		return strings.TrimSpace(out)
	}
	path := lookup()
	detail := "already there"
	current := func() bool {
		_, err := s.run(ctx, "", remoteCheck)
		return err == nil
	}
	if path == "" || !current() {
		if !s.Ask.Install() {
			s.emit(StepSmif, Failed, "not installed")
			return "", ErrStopped
		}
		s.emit(StepSmif, Running, "downloading")
		if _, err := s.run(ctx, "", remoteInstall); err != nil {
			return "", s.fail(StepSmif, err)
		}
		if path = lookup(); path == "" {
			return "", s.fail(StepSmif, errors.New("the install ran, but smif is not there"))
		}
		if !current() {
			return "", s.fail(StepSmif, errors.New("smif there is too old, and the install did not change that"))
		}
		detail = "installed"
	}
	if !cleanPath.MatchString(path) {
		return "", s.fail(StepSmif, fmt.Errorf("smif is at an unusual path: %q", path))
	}
	out, err := s.run(ctx, "", `PATH="$HOME/.local/bin:$PATH" `+path+` sessions --json`)
	if err != nil {
		return "", s.fail(StepSmif, err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return "", s.fail(StepSmif, fmt.Errorf("smif there gave no session list: %v", err))
	}
	s.emit(StepSmif, Done, fmt.Sprintf("%s, %d sessions", detail, len(rows)))
	return path, nil
}

func (s *Setup) key(ctx context.Context, path string) error {
	s.emit(StepKey, Running, "")
	pub, err := ensureKey(ctx)
	if err != nil {
		return s.fail(StepKey, err)
	}
	if _, err := s.run(ctx, keyLine(path, pub, s.Write)+"\n", remoteKey); err != nil {
		return s.fail(StepKey, err)
	}
	daemon.RemoteDisconnect(s.Host)
	detail := "read only"
	if s.Write {
		detail = "and type into them"
	}
	s.emit(StepKey, Done, detail)
	return nil
}

func (s *Setup) linger(ctx context.Context) error {
	s.emit(StepLinger, Running, "")
	state := func(command string) string {
		out, _ := s.run(ctx, "", command)
		return strings.TrimSpace(out)
	}
	switch state(remoteLinger) {
	case "yes":
		s.emit(StepLinger, Done, "already on")
		return nil
	case "none":
		s.emit(StepLinger, Done, "no systemd, not needed")
		return nil
	}
	if state(remoteEnable) == "yes" {
		s.emit(StepLinger, Done, "on")
		return nil
	}
	problem := ""
	for try := 0; try < loginTries; try++ {
		pass, ok := s.Ask.Sudo(problem)
		if !ok {
			break
		}
		_, err := s.run(ctx, pass+"\n", remoteSudo)
		if err == nil && state(remoteLinger) == "yes" {
			s.emit(StepLinger, Done, "on")
			return nil
		}
		if ctx.Err() != nil {
			return ErrStopped
		}
		problem = "sudo said no."
	}
	s.emit(StepLinger, Skipped, "it can drop out when nobody is logged in there")
	return nil
}

func (s *Setup) finish() error {
	s.emit(StepConfig, Running, "")
	added, err := config.AddRemote("", s.Host)
	if err != nil {
		return s.fail(StepConfig, err)
	}
	if err := config.SetRemoteWrite("", s.Host, s.Write); err != nil {
		return s.fail(StepConfig, err)
	}
	detail := "daemon.remotes"
	if !added {
		detail = "already there"
	}
	s.emit(StepConfig, Done, detail)

	s.emit(StepDaemon, Running, "first look within 15 s")
	if err := s.Daemon.Reload(); err != nil {
		return s.fail(StepDaemon, err)
	}
	last := "no answer yet"
	for deadline := time.Now().Add(s.await); time.Now().Before(deadline); time.Sleep(time.Second) {
		ok, detail, err := s.Daemon.Reached(s.Host)
		if err != nil {
			return s.fail(StepDaemon, err)
		}
		if ok {
			s.emit(StepDaemon, Done, detail)
			return nil
		}
		if detail != "" {
			last = detail
		}
	}
	return s.fail(StepDaemon, fmt.Errorf("your daemon cannot reach it: %s", last))
}

// isThisMachine says that a target is an address of this machine. Loopback
// does not count: a tunnel or a container can sit behind it.
func isThisMachine(ctx context.Context, host string) bool {
	name := resolve(ctx, nameOf(host))["hostname"]
	if name == "" {
		name = nameOf(host)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(cctx, name)
	if err != nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() {
			continue
		}
		for _, ip := range ips {
			if ip.IP.Equal(n.IP) {
				return true
			}
		}
	}
	return false
}

func userOf(host string) string {
	if i := strings.Index(host, "@"); i > 0 {
		return host[:i]
	}
	return ""
}

func nameOf(host string) string { return host[strings.Index(host, "@")+1:] }
