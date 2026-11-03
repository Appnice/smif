// Package e2e runs the RFC 9.5 acceptance scenarios against a real daemon
// process, temp git repos per agent and the scripted fake claude.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

var (
	smifBin  string
	fakeBin  string
	repoRoot string
)

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot = filepath.Dir(filepath.Dir(file))
	dir, err := os.MkdirTemp("", "smif-bin-")
	if err != nil {
		panic(err)
	}
	smifBin = filepath.Join(dir, "smif")
	fakeBin = filepath.Join(dir, "claude")
	for _, b := range [][2]string{{smifBin, "./cmd/smif"}, {fakeBin, "./testdata/fakeclaude"}} {
		args := []string{"build", "-o", b[0], b[1]}
		cgo := "CGO_ENABLED=0"
		if raceBuild && b[0] == smifBin {
			args = []string{"build", "-race", "-o", b[0], b[1]}
			cgo = "CGO_ENABLED=1"
		}
		cmd := exec.Command("go", args...)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), cgo)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", b[1], err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Env is one isolated Smif installation.
type Env struct {
	t      *testing.T
	Root   string
	Home   string
	Repos  map[string]string
	daemon *exec.Cmd
	logBuf *bytes.Buffer
	env    []string
}

// AgentSpec configures a test agent.
type AgentSpec struct {
	ID     string
	Extra  string // TOML lines appended inside [agents.<id>]
	Rules  string // TOML rules; "" = default catch-all worker with profile triage
	Before func(repo string)
}

// NewEnv creates repos and a config. daemonExtra is appended to [daemon].
func NewEnv(t *testing.T, daemonExtra, profiles string, agents ...AgentSpec) *Env {
	t.Helper()
	// Short root: Unix socket paths are limited to 104 bytes on macOS.
	root, err := os.MkdirTemp("/tmp", "mr-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	e := &Env{t: t, Root: root, Home: filepath.Join(root, "home"), Repos: map[string]string{}}
	t.Cleanup(func() {
		e.Stop()
		if !t.Failed() || os.Getenv("SMIF_KEEP") == "" {
			os.RemoveAll(root)
		} else {
			t.Logf("kept %s", root)
		}
	})
	for _, d := range []string{"home/.claude", "cfg", "data", "fake", "repos"} {
		os.MkdirAll(filepath.Join(root, d), 0o700)
	}
	e.env = append(cleanEnv(),
		"HOME="+e.Home,
		"SMIF_CONFIG_DIR="+filepath.Join(root, "cfg"),
		"SMIF_DATA_DIR="+filepath.Join(root, "data"),
		"SMIF_SOCKET="+filepath.Join(root, "s.sock"),
		"SMIF_FAKE_LOG="+filepath.Join(root, "fake"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(e.Home, ".claude"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"),
		// The harness starts its own daemon; a CLI call must not start
		// another one that outlives the test (TestAutostart turns it on).
		"SMIF_NO_AUTOSTART=1",
	)
	os.WriteFile(filepath.Join(root, "gitconfig"), []byte("[user]\n\temail = test@example.com\n\tname = Test\n[init]\n\tdefaultBranch = main\n"), 0o644)
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "[daemon]\nmax_workers = 3\nnotify = \"log:%s\"\nretry_backoff = [\"500ms\", \"500ms\", \"500ms\"]\n%s\n",
		filepath.Join(root, "notify.jsonl"), daemonExtra)
	fmt.Fprintf(&cfg, "[defaults]\nclaude_command = %q\n\n%s\n", fakeBin, profiles)
	for _, a := range agents {
		repo := filepath.Join(root, "repos", a.ID)
		e.Repos[a.ID] = repo
		e.git(repo, "init", "-q", "-b", "main", repo)
		os.WriteFile(filepath.Join(repo, "README.md"), []byte("# "+a.ID+"\n"), 0o644)
		os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644)
		if a.Before != nil {
			a.Before(repo)
		}
		e.git(repo, "-C", repo, "add", "-A")
		e.git(repo, "-C", repo, "commit", "-qm", "init")
		fmt.Fprintf(&cfg, "[agents.%s]\nrepo = %q\n%s\n", a.ID, repo, a.Extra)
		rules := a.Rules
		if rules == "" {
			rules = "[[agents." + a.ID + ".rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"triage\"\n"
		}
		cfg.WriteString(strings.ReplaceAll(rules, "AGENT", a.ID) + "\n")
	}
	if err := os.WriteFile(filepath.Join(root, "cfg", "config.toml"), []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SMIF_") || strings.HasPrefix(k, "CLAUDE") || k == "HOME" || strings.HasPrefix(k, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (e *Env) git(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Git runs git -C repo.
func (e *Env) Git(repo string, args ...string) string {
	return e.git(repo, append([]string{"-C", repo}, args...)...)
}

// Config returns the config path.
func (e *Env) Config() string { return filepath.Join(e.Root, "cfg", "config.toml") }

// Start launches the daemon and waits for the socket.
func (e *Env) Start() {
	e.t.Helper()
	cmd := exec.Command(smifBin, "daemon")
	cmd.Env = e.env
	e.logBuf = &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = e.logBuf, e.logBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.daemon = cmd
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := e.Client(); err == nil {
			var h api.HealthView
			err := c.Call("admin.health", nil, &h, time.Second)
			c.Close()
			if err == nil {
				return
			}
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("daemon did not start:\n%s", e.logBuf.String())
}

// Stop sends SIGTERM and waits.
func (e *Env) Stop() {
	if e.daemon == nil || e.daemon.Process == nil {
		return
	}
	e.daemon.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { e.daemon.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		e.daemon.Process.Kill()
		<-done
	}
	e.daemon = nil
	e.checkRace()
}

// checkRace fails the test if the daemon (built with -race) reported a race.
func (e *Env) checkRace() {
	if e.logBuf != nil && strings.Contains(e.logBuf.String(), "WARNING: DATA RACE") {
		out := e.logBuf.String()
		i := strings.Index(out, "WARNING: DATA RACE")
		end := i + 4000
		if end > len(out) {
			end = len(out)
		}
		e.t.Errorf("daemon data race:\n%s", out[i:end])
	}
}

// Kill9 kills the daemon process only (its workers live in their own groups).
func (e *Env) Kill9() {
	e.daemon.Process.Signal(syscall.SIGKILL)
	e.daemon.Wait()
	e.daemon = nil
	e.checkRace()
}

func (e *Env) token(name string) string {
	b, err := os.ReadFile(filepath.Join(e.Root, "cfg", "tokens", name))
	if err != nil {
		e.t.Fatalf("token %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// Client dials as the human.
func (e *Env) Client() (*api.Client, error) {
	b, err := os.ReadFile(filepath.Join(e.Root, "cfg", "tokens", "human"))
	if err != nil {
		return nil, err
	}
	return api.Dial(filepath.Join(e.Root, "s.sock"), api.Auth{Agent: "human", Token: strings.TrimSpace(string(b))}, time.Second)
}

// AgentClient dials as an agent (origin interactive).
func (e *Env) AgentClient(agent string) *api.Client {
	e.t.Helper()
	c, err := api.Dial(filepath.Join(e.Root, "s.sock"), api.Auth{Agent: agent, Token: e.token(agent), Origin: "interactive"}, time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// Call is a human call that must succeed.
func (e *Env) Call(method string, params, out any) {
	e.t.Helper()
	c, err := e.Client()
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(method, params, out, 30*time.Second); err != nil {
		e.t.Fatalf("%s: %v", method, err)
	}
}

// Send sends as an agent and returns the ack.
func (e *Env) Send(from, method string, d envelope.Draft) api.SendResult {
	e.t.Helper()
	c := e.AgentClient(from)
	defer c.Close()
	var r api.SendResult
	if err := c.Call(method, api.SendParams{Draft: d}, &r, 30*time.Second); err != nil {
		e.t.Fatalf("%s from %s: %v", method, from, err)
	}
	return r
}

// CLI runs the smif binary with the env and optional stdin.
func (e *Env) CLI(stdin string, args ...string) (string, error) {
	cmd := exec.Command(smifBin, args...)
	cmd.Env = e.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%v: %s", err, errb.String())
	}
	return out.String(), nil
}

// CLIDir runs the binary in a directory.
func (e *Env) CLIDir(dir, stdin string, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(smifBin, args...)
	cmd.Env = append(append([]string{}, e.env...), extraEnv...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// Message reads a message through thread.get.
func (e *Env) Message(id string) api.MessageView {
	e.t.Helper()
	var tv api.ThreadView
	e.Call("thread.get", api.IDParams{ID: id}, &tv)
	for _, m := range tv.Messages {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatalf("message %s not in its thread", id)
	return api.MessageView{}
}

// Thread reads a thread.
func (e *Env) Thread(id string) api.ThreadView {
	e.t.Helper()
	var tv api.ThreadView
	e.Call("thread.get", api.IDParams{ID: id}, &tv)
	return tv
}

// WaitState waits until a message reaches one of the states.
func (e *Env) WaitState(id string, timeout time.Duration, states ...string) api.MessageView {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var m api.MessageView
	for time.Now().Before(deadline) {
		m = e.Message(id)
		for _, s := range states {
			if m.State == s {
				return m
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("message %s stuck in %s (reason %q), want %v\ndaemon log:\n%s", id, m.State, m.Reason, states, e.tailLog())
	return m
}

// WaitFor polls cond.
func (e *Env) WaitFor(what string, timeout time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s\n%s", what, e.tailLog())
}

func (e *Env) tailLog() string {
	b, _ := os.ReadFile(filepath.Join(e.Root, "data", "logs", "smifd.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	return strings.Join(lines, "\n")
}

// Invocation is one fake claude call.
type Invocation struct {
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Stdin     string            `json:"stdin"`
	PID       int               `json:"pid"`
	SessionID string            `json:"session_id"`
	Env       map[string]string `json:"env"`
	ChildPID  int               `json:"child_pid"`
	PGID      int               `json:"pgid"`
}

// Invocations returns every fake claude call so far.
func (e *Env) Invocations() []Invocation {
	f, err := os.Open(filepath.Join(e.Root, "fake", "invocations.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Invocation
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 16<<20)
	for s.Scan() {
		var inv Invocation
		if json.Unmarshal(s.Bytes(), &inv) == nil {
			out = append(out, inv)
		}
	}
	return out
}

// WorkerCalls returns invocations that are worker runs (not --version/--help/children).
func (e *Env) WorkerCalls() []Invocation {
	var out []Invocation
	for _, i := range e.Invocations() {
		if len(i.Argv) > 1 && i.Argv[1] == "-p" {
			out = append(out, i)
		}
	}
	return out
}

// Notifications returns the notifier log.
func (e *Env) Notifications() []map[string]any {
	b, _ := os.ReadFile(filepath.Join(e.Root, "notify.jsonl"))
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// Audit returns audit rows for a message from the JSONL log.
func (e *Env) Audit() []map[string]any {
	var out []map[string]any
	files, _ := filepath.Glob(filepath.Join(e.Root, "data", "audit", "*.jsonl"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// Runs lists runs for a message.
func (e *Env) Runs(msgID string) []api.RunView {
	var rs []api.RunView
	e.Call("run.list", api.ListParams{MessageID: msgID, Limit: 100}, &rs)
	return rs
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
