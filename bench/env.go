package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/appnice/smif/internal/api"
)

// env is one throw-away Smif: its own config, data, socket, repos and
// a daemon process, with the fake claude as claude_command.
type env struct {
	root    string
	bin     string // smif
	fake    string // fake claude
	vars    []string
	daemon  *exec.Cmd
	logs    bytes.Buffer
	repos   map[string]string
	started time.Time
}

// agentSpec is one agent: its id and the TOML of its rules (AGENT is
// replaced by the id).
type agentSpec struct {
	id, rules, extra string
	files            map[string]string // repo files written before the first commit
}

// buildBinaries builds smif and the fake claude once.
func buildBinaries(repoRoot, dir string) (smif, fake string, err error) {
	smif, fake = filepath.Join(dir, "smif"), filepath.Join(dir, "claude")
	for _, b := range [][2]string{{smif, "./cmd/smif"}, {fake, "./testdata/fakeclaude"}} {
		cmd := exec.Command("go", "build", "-o", b[0], b[1])
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("build %s: %v\n%s", b[1], err, out)
		}
	}
	return smif, fake, nil
}

func newEnv(bin, fake, daemonExtra, profiles string, agents ...agentSpec) (*env, error) {
	root, err := os.MkdirTemp("/tmp", "mr-b") // short: the socket path limit is 104 bytes on macOS
	if err != nil {
		return nil, err
	}
	root, _ = filepath.EvalSymlinks(root)
	e := &env{root: root, bin: bin, fake: fake, repos: map[string]string{}}
	for _, d := range []string{"home/.claude", "cfg", "data", "fake", "repos"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SMIF_") || strings.HasPrefix(k, "CLAUDE") || k == "HOME" || strings.HasPrefix(k, "GIT_") {
			continue
		}
		e.vars = append(e.vars, kv)
	}
	e.vars = append(e.vars,
		"HOME="+filepath.Join(root, "home"),
		"SMIF_CONFIG_DIR="+filepath.Join(root, "cfg"),
		"SMIF_DATA_DIR="+filepath.Join(root, "data"),
		"SMIF_SOCKET="+filepath.Join(root, "s.sock"),
		"SMIF_FAKE_LOG="+filepath.Join(root, "fake"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(root, "home", ".claude"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "gitconfig"),
	)
	os.WriteFile(filepath.Join(root, "gitconfig"), []byte("[user]\n\temail = bench@example.com\n\tname = Bench\n[init]\n\tdefaultBranch = main\n"), 0o644)
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "[daemon]\nmax_workers = 4\nnotify = \"log:%s\"\nretry_backoff = [\"200ms\", \"200ms\", \"200ms\"]\n%s\n",
		filepath.Join(root, "notify.jsonl"), daemonExtra)
	fmt.Fprintf(&cfg, "[defaults]\nclaude_command = %q\n\n%s\n", fake, profiles)
	for _, a := range agents {
		repo := filepath.Join(root, "repos", a.id)
		e.repos[a.id] = repo
		if err := e.git("init", "-q", "-b", "main", repo); err != nil {
			return nil, err
		}
		os.WriteFile(filepath.Join(repo, "README.md"), []byte("# "+a.id+"\n"), 0o644)
		for p, body := range a.files {
			full := filepath.Join(repo, p)
			os.MkdirAll(filepath.Dir(full), 0o755)
			os.WriteFile(full, []byte(body), 0o755)
		}
		if err := e.git("-C", repo, "add", "-A"); err != nil {
			return nil, err
		}
		if err := e.git("-C", repo, "commit", "-qm", "init"); err != nil {
			return nil, err
		}
		fmt.Fprintf(&cfg, "[agents.%s]\nrepo = %q\n%s\n", a.id, repo, a.extra)
		cfg.WriteString(strings.ReplaceAll(a.rules, "AGENT", a.id) + "\n")
	}
	return e, os.WriteFile(filepath.Join(root, "cfg", "config.toml"), []byte(cfg.String()), 0o600)
}

func (e *env) git(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = e.vars
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return nil
}

// start runs the daemon and waits until it answers.
func (e *env) start() error {
	cmd := exec.Command(e.bin, "daemon")
	cmd.Env = e.vars
	cmd.Stdout, cmd.Stderr = &e.logs, &e.logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	e.daemon, e.started = cmd, time.Now()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := e.client("human", ""); err == nil {
			var h api.HealthView
			err := c.Call("admin.health", nil, &h, time.Second)
			c.Close()
			if err == nil {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start:\n%s", e.logs.String())
}

// stop sends SIGTERM; kill9 sends SIGKILL to the daemon only.
func (e *env) stop() {
	if e.daemon == nil {
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
}

func (e *env) kill9() {
	e.daemon.Process.Signal(syscall.SIGKILL)
	e.daemon.Wait()
	e.daemon = nil
}

func (e *env) close() {
	e.stop()
	os.RemoveAll(e.root)
}

// client dials as an agent ("human" for the human token). origin is
// "interactive" for an agent's session.
func (e *env) client(agent, origin string) (*api.Client, error) {
	b, err := os.ReadFile(filepath.Join(e.root, "cfg", "tokens", agent))
	if err != nil {
		return nil, err
	}
	return api.Dial(filepath.Join(e.root, "s.sock"), api.Auth{Agent: agent, Token: strings.TrimSpace(string(b)), Origin: origin}, 2*time.Second)
}

// human makes one human call.
func (e *env) human(method string, params, out any) error {
	c, err := e.client("human", "")
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(method, params, out, 10*time.Second)
}

// cli runs the smif binary with stdin and returns stdout.
func (e *env) cli(dir, stdin string, args ...string) (string, error) {
	cmd := exec.Command(e.bin, args...)
	cmd.Env, cmd.Dir = e.vars, dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	return string(out), err
}

// fakeCalls reads the fake claude's invocation records.
func (e *env) fakeCalls() []map[string]any {
	b, _ := os.ReadFile(filepath.Join(e.root, "fake", "invocations.jsonl"))
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["argv"] != nil {
			out = append(out, m)
		}
	}
	return out
}

// waitStates polls thread.list until every id is in a terminal state (or
// the deadline passes) and returns the last view of each.
func (e *env) waitTerminal(ids []string, timeout time.Duration) (map[string]api.MessageView, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	deadline := time.Now().Add(timeout)
	for {
		var all []api.MessageView
		if err := e.human("thread.list", api.ListParams{Limit: 100000}, &all); err != nil {
			return nil, err
		}
		got, open := map[string]api.MessageView{}, 0
		for _, m := range all {
			if !want[m.ID] {
				continue
			}
			got[m.ID] = m
			switch m.State {
			case "done", "rejected", "expired", "dead_letter", "needs_human":
			default:
				open++
			}
		}
		if open == 0 && len(got) == len(want) {
			return got, nil
		}
		if time.Now().After(deadline) {
			return got, fmt.Errorf("%d of %d messages not terminal after %v", open+len(want)-len(got), len(want), timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
