package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/harness"
	claudeharness "github.com/appnice/smif/internal/harness/claude"
	"github.com/appnice/smif/internal/store"
)

// fakeProvider is an agent with no real files. Its sessions use this test's
// own pid, so they count as live.
type fakeProvider struct {
	name     string
	process  string
	verified bool
	sessions []harness.Session
	reading  harness.Reading
}

func (f *fakeProvider) Info() harness.Info {
	return harness.Info{Name: f.name, Process: f.process, Verified: f.verified}
}
func (f *fakeProvider) Discover() []harness.Session { return f.sessions }
func (f *fakeProvider) Read(id string, _ int) harness.Reading {
	for _, s := range f.sessions {
		if s.SessionID == id {
			return f.reading
		}
	}
	return harness.Reading{}
}

// statefulFake adds a state source, as a provider with a Stater has.
type statefulFake struct{ *fakeProvider }

func (statefulFake) State(s harness.Session) (string, time.Time) { return s.Status, s.StatusAt }

// testDaemon builds a daemon that is not started: nothing listens and no
// claude runs. Its Claude Code folder is empty, and the terminal only logs.
func testDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	t.Setenv("QWEN_HOME", filepath.Join(dir, "qwen"))
	t.Setenv("QWEN_RUNTIME_DIR", filepath.Join(dir, "qwen"))
	t.Setenv("OPENCODE_DB", filepath.Join(dir, "opencode.db"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("COPILOT_HOME", filepath.Join(dir, "copilot"))
	t.Setenv("FACTORY_HOME_OVERRIDE", dir)
	t.Setenv("SMIF_NO_AGENT_SCAN", "1") // the host's agents stay out; a scan test turns it on
	typed := filepath.Join(dir, "typed.jsonl")
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[daemon]\nterminal = \"log:"+typed+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{DataDir: filepath.Join(dir, "data"), Socket: filepath.Join(dir, "s"), ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	// No test runs the real claude: `claude agents` stays off here.
	d.harnesses.Get(harness.ClaudeName).(*claudeharness.Provider).Command = nil
	t.Cleanup(func() {
		d.store.Close()
		d.logFile.Close()
		d.lock.Close()
	})
	return d, typed
}

// row gives a session a terminal, as a hook does, so that only the provider
// can stop the prompt.
func row(t *testing.T, d *Daemon, id, harnessName, repo string) {
	t.Helper()
	if err := d.store.Update(func(tx *store.Tx) error {
		return tx.UpsertPresence(repo, id, "active", store.PresenceUpdate{TTY: "/dev/ttys999", Term: termTmux, PID: os.Getpid(), Harness: harnessName})
	}); err != nil {
		t.Fatal(err)
	}
}

func viewOf(t *testing.T, d *Daemon, id string) api.SessionView {
	t.Helper()
	ss, err := d.sessionList()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.SessionID == id {
			return s
		}
	}
	t.Fatalf("session %s is not in the list: %+v", id, ss)
	return api.SessionView{}
}

func typedText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// A registered provider's session shows in session.list with its harness
// name, with its answer and steps from Read, and session.tail finds it.
func TestFakeProviderInSessionList(t *testing.T) {
	d, _ := testDaemon(t)
	at := time.Now().Add(-time.Minute)
	f := &fakeProvider{name: "fakeagent", process: "fakeagent",
		sessions: []harness.Session{{PID: os.Getpid(), SessionID: "fake-session-01", Cwd: t.TempDir(), Name: "from fake"}},
		reading: harness.Reading{OK: true, WroteAt: at, Turns: []harness.Turn{
			{Role: harness.RoleHuman, Text: "go", At: at}, {Role: harness.RoleAgent, Text: "done", At: at, Tools: 1}},
			Activity: harness.Activity{Prompt: "go", Tools: 1, Steps: []harness.Step{{Tool: "edit", Target: "a.go", Done: true}}}}}
	d.harnesses.Register(f)
	v := viewOf(t, d, "fake-session-01")
	if v.Harness != "fakeagent" || v.Title != "from fake" || v.LastAnswer != "done" || v.Activity == nil || v.Activity.Steps[0].Tool != "edit" {
		t.Fatalf("view: %+v", v)
	}
	b, err := json.Marshal(v)
	if err != nil || !strings.Contains(string(b), `"harness":"fakeagent"`) {
		t.Fatalf("json: %s %v", b, err)
	}
	tail, err := d.sessionTail(api.SessionTailParams{SessionID: "fake-session-01"})
	if err != nil || len(tail.Turns) != 2 || tail.Turns[1].Text != "done" {
		t.Fatalf("tail: %+v %v", tail, err)
	}
	if st, _ := d.sessionsCanary(); st != "none" {
		t.Fatalf("doctor: %s", st) // the fake has no Check; Claude Code's folder is empty
	}
}

// A row from before migration 005, or from a hook, is a Claude Code session.
func TestHookRowIsClaude(t *testing.T) {
	d, _ := testDaemon(t)
	row(t, d, "hook-session-01", "", t.TempDir())
	if v := viewOf(t, d, "hook-session-01"); v.Harness != "claude" {
		t.Fatalf("view: %+v", v)
	}
}

// ADR-0019: an unverified provider never gets a key or a fork, whatever
// state it reports.
func TestUnverifiedProviderIsReadOnly(t *testing.T) {
	for _, status := range []string{"idle", "busy", "waiting"} {
		t.Run(status, func(t *testing.T) {
			d, typed := testDaemon(t)
			id := "unverified-" + status
			f := statefulFake{&fakeProvider{name: "fakeagent", process: "fakeagent", sessions: []harness.Session{
				{PID: os.Getpid(), SessionID: id, Cwd: t.TempDir(), Name: "x", Status: status, StatusAt: time.Now()}}}}
			d.harnesses.Register(f)
			row(t, d, id, "fakeagent", t.TempDir())
			v := viewOf(t, d, id)
			if v.CanPrompt || !strings.Contains(v.Why, "unverified") {
				t.Fatalf("view: %+v", v)
			}
			ctx := context.Background()
			_, err := d.sessionPrompt(ctx, api.SessionPromptParams{SessionID: id, Text: "hello"})
			if !api.IsCode(err, api.CodeRejected) {
				t.Fatalf("prompt: %v", err)
			}
			_, err = d.sessionSend(ctx, auth.Principal{}, api.SessionSendParams{SessionID: id, Text: "hello"})
			if !api.IsCode(err, api.CodeRejected) {
				t.Fatalf("send: %v", err)
			}
			if _, _, err := d.forkTarget(store.Presence{Harness: "fakeagent", SessionID: id, Repo: "/r", Cwd: "/r"}); !api.IsCode(err, api.CodeRejected) ||
				!strings.Contains(err.Error(), "no fork") {
				t.Fatalf("fork: %v", err)
			}
			if got := typedText(t, typed); got != "" {
				t.Fatalf("typed into a read-only session: %s", got)
			}
		})
	}
}

// ADR-0019: a verified provider with no state source shows unknown, not
// idle, and smif does not type, even when a stored row says idle.
func TestUnknownStateMeansNoTyping(t *testing.T) {
	d, typed := testDaemon(t)
	id := "no-state-source"
	d.harnesses.Register(&fakeProvider{name: "fakeagent", process: "fakeagent", verified: true,
		sessions: []harness.Session{{PID: os.Getpid(), SessionID: id, Cwd: t.TempDir()}}})
	repo := t.TempDir()
	row(t, d, id, "fakeagent", repo)
	if err := d.store.Update(func(tx *store.Tx) error { return tx.SetPresenceTurn(id, "idle") }); err != nil {
		t.Fatal(err)
	}
	v := viewOf(t, d, id)
	if v.Turn != "unknown" || v.CanPrompt {
		t.Fatalf("view: %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: id, Text: "hello"}); !api.IsCode(err, api.CodeRejected) {
		t.Fatalf("prompt: %v", err)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed with an unknown state: %s", got)
	}
}

// A verified provider with a state source types when idle, and the gate
// does not stop it. With no Forker, a busy session takes no side question.
func TestVerifiedProviderTypesWhenIdle(t *testing.T) {
	d, typed := testDaemon(t)
	id := "verified-session"
	f := &fakeProvider{name: "fakeagent", process: "fakeagent", verified: true,
		sessions: []harness.Session{{PID: os.Getpid(), SessionID: id, Cwd: t.TempDir(), Status: "idle", StatusAt: time.Now()}}}
	d.harnesses.Register(statefulFake{f})
	row(t, d, id, "fakeagent", t.TempDir())
	if v := viewOf(t, d, id); !v.CanPrompt {
		t.Fatalf("view: %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: id, Text: "hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got := typedText(t, typed); !strings.Contains(got, `"text":"hello"`) {
		t.Fatalf("typed: %q", got)
	}
	f.sessions[0].Status, f.sessions[0].StatusAt = "busy", time.Now().Add(time.Second)
	_, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: id, Text: "hello"})
	if !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "no safe fork") {
		t.Fatalf("send to a busy session without a Forker: %v", err)
	}
}

// The foreground check asks the provider for its process name. This test's
// own process is the session's process: its name passes for the fake and
// fails for claude, and a harness with no provider is refused.
func TestReadyAsksProviderForProcessName(t *testing.T) {
	reg := harness.NewRegistry(
		&fakeProvider{name: "claude", process: "claude", verified: true},
		&fakeProvider{name: "fakeagent", process: "daemon.test", verified: true},
		&fakeProvider{name: "noname", verified: true})
	term := autoTerminal{harnesses: reg}
	p := store.Presence{PID: os.Getpid(), TTY: "/dev/ttys999"}

	p.Harness = "claude"
	if err := term.Ready(p); err == nil || !strings.Contains(err.Error(), "not claude") {
		t.Fatalf("claude: %v", err)
	}
	p.Harness = "fakeagent"
	if err := term.Ready(p); err == nil || !strings.Contains(err.Error(), " is on ") {
		t.Fatalf("the name must pass, and the tty must stop it: %v", err)
	}
	for _, h := range []string{"noname", "gone", ""} {
		p.Harness = h
		if err := term.Ready(p); err == nil || !strings.Contains(err.Error(), "no process name") {
			t.Fatalf("%q: %v", h, err)
		}
	}
}

// setConfig adds to the config and loads it, as admin.reload does.
func setConfig(t *testing.T, d *Daemon, extra string) {
	t.Helper()
	path := d.Config().Path
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte("\n"+extra+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
}

// pathWith makes a PATH with only these programs in it.
func pathWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// opened reads what the log terminal got from session.new, the last call.
func opened(t *testing.T, typed string) (argv []string, label string) {
	t.Helper()
	b, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var rec struct {
		Argv  []string `json:"argv"`
		Label string   `json:"label"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Argv, rec.Label
}

// The registry and the name list that config validation reads stay in step.
func TestBuiltinProvidersMatchRegistry(t *testing.T) {
	d, _ := testDaemon(t)
	for _, p := range d.harnesses.All() {
		if !harness.IsBuiltinProvider(p.Info().Name) {
			t.Errorf("%s is missing from builtinProviders", p.Info().Name)
		}
		if p.Info().Name != harness.ClaudeName && harness.BuiltinCommand(p.Info().Name) == "" {
			t.Errorf("%s has no built-in command", p.Info().Name)
		}
	}
	if harness.IsBuiltinProvider("gemini") {
		t.Error("gemini has no provider")
	}
}

// n offers claude, plus each harness whose command the daemon finds.
func TestHarnessOptions(t *testing.T) {
	d, _ := testDaemon(t)
	bin := pathWith(t, "codex", "omnis-bin")
	if got := d.harnessOptions(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Fatalf("no config: %v", got)
	}
	setConfig(t, d, `[harnesses.omnis]
process = ["omnis"]
command = "omnis-bin"
[harnesses.nocmd]
process = ["nocmd"]
[harnesses.gone]
process = ["gone"]
command = "/no/such/gone"
[harnesses.gemini]
command = "`+bin+`/omnis-bin"`)
	if got := d.harnessOptions(); !slices.Equal(got, []string{"claude", "codex", "gemini", "omnis"}) {
		t.Fatalf("with config: %v", got)
	}
}

// session.new builds argv from the harness's name alone: the name of the
// session reaches claude only, and no RPC argument adds a word or changes
// the program. An unknown harness is refused before a terminal opens.
func TestSessionNewArgv(t *testing.T) {
	d, typed := testDaemon(t)
	bin := pathWith(t, "codex", "omnis-bin")
	setConfig(t, d, "[harnesses.omnis]\nprocess = [\"omnis\"]\ncommand = \"omnis-bin\"\n[harnesses.nocmd]\nprocess = [\"nocmd\"]")
	dir := t.TempDir()
	ctx := context.Background()
	open := func(p api.SessionNewParams) []string {
		t.Helper()
		p.Dir = dir
		if _, err := d.sessionNew(ctx, p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		argv, _ := opened(t, typed)
		return argv
	}
	for _, c := range []struct {
		p    api.SessionNewParams
		want []string
	}{
		{api.SessionNewParams{}, []string{"claude"}},
		{api.SessionNewParams{Name: "api work"}, []string{"claude", "--name", "api work"}},
		{api.SessionNewParams{Harness: "claude", Name: "api"}, []string{"claude", "--name", "api"}},
		{api.SessionNewParams{Harness: "codex"}, []string{"codex"}},
		{api.SessionNewParams{Harness: "codex", Name: "api work"}, []string{"codex"}},
		{api.SessionNewParams{Harness: "omnis", Name: "api"}, []string{"omnis-bin"}},
	} {
		if got := open(c.p); !slices.Equal(got, c.want) {
			t.Errorf("%+v: argv %q, want %q", c.p, got, c.want)
		}
	}
	if _, label := opened(t, typed); label != "api" {
		t.Errorf("the name stays the terminal's label: %q", label)
	}

	before, _ := os.ReadFile(typed)
	for _, h := range []string{"omnis2", "nocmd", "gemini", "/bin/sh", "sh -c id", "codex --yolo", "../codex", "OMNIS", bin + "/codex"} {
		_, err := d.sessionNew(ctx, api.SessionNewParams{Dir: dir, Harness: h})
		code := api.CodeParams
		if h == "gemini" {
			code = api.CodeUnavailable // known, not on this PATH
		}
		if !api.IsCode(err, code) {
			t.Errorf("harness %q: %v", h, err)
		}
	}
	if _, err := d.sessionNew(ctx, api.SessionNewParams{Dir: dir, Harness: "codex", Name: "--dangerously-skip-permissions"}); !api.IsCode(err, api.CodeParams) {
		t.Errorf("a flag as a name: %v", err)
	}
	if after, _ := os.ReadFile(typed); string(after) != string(before) {
		t.Fatalf("a refused call opened a terminal: %s", after[len(before):])
	}

	// A field the API does not have changes nothing.
	var p api.SessionNewParams
	raw, _ := json.Marshal(map[string]any{"dir": dir, "harness": "omnis", "command": "/bin/sh", "argv": []string{"/bin/sh", "-c", "id"}, "process": []string{"sh"}})
	if err := decode(raw, &p); err != nil {
		t.Fatal(err)
	}
	if got := open(p); !slices.Equal(got, []string{"omnis-bin"}) {
		t.Fatalf("extra fields reached argv: %q", got)
	}
}

// [harnesses.omnis] feeds the Listed level: a fake process named omnis on a
// terminal becomes a row through session.list, read only, and a config
// change needs no restart.
func TestConfiguredHarnessIsListed(t *testing.T) {
	t.Setenv("SMIF_HERDR", "") // the host's herdr stays out
	d := scanDaemon(t)
	me := os.Getpid()
	repo, _ := os.Getwd()
	d.scan.procs = func(context.Context) []proc {
		return []proc{{pid: me, tty: "/dev/ttys901", argv: []string{"/opt/omnis/bin/omnis", "--fast"}}}
	}
	rows := func() []api.SessionView {
		t.Helper()
		found := d.runScan(context.Background()) // once, here: lsof can outlast the wait in session.list
		d.scan.run = func(context.Context) []scanRow { return found }
		d.scan.expire()
		ss, err := d.sessionList()
		if err != nil {
			t.Fatal(err)
		}
		return ss
	}
	if ss := rows(); len(ss) != 0 {
		t.Fatalf("no config, no row: %+v", ss)
	}
	setConfig(t, d, "[harnesses.omnis]\nprocess = [\"omnis\"]")
	ss := rows()
	if len(ss) != 1 {
		t.Fatalf("rows %+v", ss)
	}
	v := ss[0]
	if v.Harness != "omnis" || v.Level != harness.LevelListed || v.Turn != "unknown" || v.CanPrompt || v.Title != "omnis" ||
		!strings.Contains(v.Why, "listed only") {
		t.Fatalf("row %+v", v)
	}
	a, _ := filepath.EvalSymlinks(repo)
	b, _ := filepath.EvalSymlinks(v.Repo)
	if !strings.HasPrefix(a, b) {
		t.Fatalf("folder %q, want %q", b, a)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
		t.Fatal("session.prompt typed into a listed row")
	}
}
