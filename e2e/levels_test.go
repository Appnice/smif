package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/ui"
)

// The herdr level (ADR-0019) through the real binary, with
// testdata/fakeherdr in place of herdr: an agent no provider owns shows in
// `smif sessions`, a prompt goes through `agent prompt`, and a blocked agent
// takes none. The fake's JSON is real herdr 0.9.5 output. The agent is maki:
// herdr has an idle rule for it, smif has no provider for it, and this Mac
// runs no such agent. A made-up name would count as no idle rule.
func TestHerdrLevel(t *testing.T) {
	bin, err := filepath.Abs("../testdata/fakeherdr/herdr")
	if err != nil {
		t.Fatal(err)
	}
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	fake := t.TempDir()
	sleeper := exec.Command("sleep", "120") // stands in for the agent
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	pid, cwd := sleeper.Process.Pid, e.Repos["app"]
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status := func(s string) {
		t.Helper()
		a := fmt.Sprintf(`{"agent":"maki","agent_status":%q,"cwd":%q,"foreground_cwd":%q,"pane_id":"w1:p1","tab_id":"w1:t1","terminal_id":"term_1","workspace_id":"w1"}`, s, cwd, cwd)
		write("agents.json", `{"id":"cli:agent:list","result":{"agents":[`+a+`],"type":"agent_list"}}`)
		write("get.json", `{"id":"cli:agent:get","result":{"agent":`+a+`,"type":"agent_info"}}`)
	}
	write("sessions.json", `{"sessions":[{"default":false,"name":"smifh-e2e","running":true}]}`)
	write("panes.json", `{"id":"cli:pane:list","result":{"panes":[{"agent":"maki","pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}],"type":"pane_list"}}`)
	write("info.json", fmt.Sprintf(`{"id":"cli:pane:process_info","result":{"process_info":{"foreground_process_group_id":%d,"foreground_processes":[{"argv0":"maki","pid":%d}],"pane_id":"w1:p1","shell_pid":%d},"type":"pane_process_info"}}`, pid, pid, pid))
	status("idle")
	e.env = append(e.env, "SMIF_NO_AGENT_SCAN=", "SMIF_HERDR="+bin, "FAKE_HERDR_DIR="+fake)
	e.Start()

	row := func(want string) api.SessionView {
		t.Helper()
		end := time.Now().Add(10 * time.Second)
		for {
			var ss []api.SessionView
			e.Call("session.list", nil, &ss)
			for _, s := range ss {
				if s.Harness == "maki" && s.Turn == want {
					return s
				}
			}
			if time.Now().After(end) {
				t.Fatalf("no maki row with state %s: %+v", want, ss)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	v := row("idle")
	if v.Level != "herdr" || v.CanPrompt || !v.Unverified || !strings.Contains(v.Why, "no real session checked it") || v.Agent != "app" || v.Term != "herdr" {
		t.Fatalf("row %+v", v)
	}
	out, stderr, err := e.CLIDir(cwd, "", nil, "sessions")
	if err != nil {
		t.Fatalf("sessions: %v %s", err, stderr)
	}
	if !strings.Contains(out, "harness") || !strings.Contains(out, "level") || !strings.Contains(out, "maki") || !strings.Contains(out, "herdr") {
		t.Fatalf("sessions:\n%s", out)
	}
	if b, _ := json.Marshal(v); !strings.Contains(string(b), `"level":"herdr"`) {
		t.Fatalf("json %s", b)
	}

	log := func() string {
		b, _ := os.ReadFile(filepath.Join(fake, "prompts.log"))
		return string(b)
	}
	// No real session checked maki, so an idle row takes no typing either.
	if _, _, err := e.CLIDir(cwd, "", nil, "prompt", "maki", "run", "the", "tests"); err == nil {
		t.Fatal("smif prompt took an unchecked agent")
	}
	if got := log(); got != "" {
		t.Fatalf("prompts %q", got)
	}

	for _, c := range []struct{ herdr, turn string }{{"blocked", "waiting"}, {"unknown", "unknown"}} {
		status(c.herdr)
		v = row(c.turn)
		if v.CanPrompt || v.Why == "" {
			t.Fatalf("row %+v", v)
		}
		if _, _, err := e.CLIDir(cwd, "", nil, "prompt", "maki", "yes"); err == nil {
			t.Fatalf("%s: smif prompt took it", c.herdr)
		}
		if got := log(); got != "" {
			t.Fatalf("%s: prompts %q", c.herdr, got)
		}
	}
}

// [harnesses.omnis] through the real binary and the real process table: a
// process named omnis on a terminal shows as a Listed row, session.harnesses
// offers it, and session.new starts only the configured command. The "agent"
// is a shell script on a pseudo terminal.
func TestCustomHarness(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	omnis := filepath.Join(e.Root, "bin", "omnis")
	if err := os.MkdirAll(filepath.Dir(omnis), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(omnis, []byte("#!/bin/sh\nread x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(e.Config(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "\n[harnesses.omnis]\nprocess = [\"omnis\"]\ncommand = %q\n", omnis)
	f.Close()
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	agent := exec.Command(omnis)
	agent.Dir = e.Repos["app"]
	agent.Stdin, agent.Stdout, agent.Stderr = slave, slave, slave
	agent.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_ = agent.Wait()
	})
	e.env = append(e.env, "SMIF_NO_AGENT_SCAN=", "SMIF_HERDR=")
	e.Start()

	var row *api.SessionView
	e.WaitFor("the omnis row", 15*time.Second, func() bool {
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		for i := range ss {
			if ss[i].Harness == "omnis" {
				row = &ss[i]
				return true
			}
		}
		return false
	})
	if row.Level != "listed" || row.Turn != "unknown" || row.CanPrompt || row.Agent != "app" {
		t.Fatalf("row %+v", row)
	}
	var hs []string
	e.Call("session.harnesses", nil, &hs)
	if len(hs) < 2 || hs[0] != "claude" || !slices.Contains(hs, "omnis") {
		t.Fatalf("harnesses %v", hs)
	}
	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	if snap := src.Fetch(context.Background()); snap.Err != nil || !slices.Equal(snap.Harnesses, hs) {
		t.Fatalf("the UI sees %v, %v", snap.Harnesses, snap.Err)
	}
	var x any
	if err := src.NewSession(context.Background(), e.Repos["app"], "api", "tmux", "omnis"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(typed)
	if err != nil || !strings.Contains(string(b), `"argv":[`+strconv.Quote(omnis)+`]`) {
		t.Fatalf("session.new: %v %s", err, b)
	}
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("session.new", api.SessionNewParams{Dir: e.Repos["app"], Harness: "/bin/sh"}, &x, 5e9); !api.IsCode(err, api.CodeParams) {
		t.Errorf("an unknown harness: %v", err)
	}
	ac := e.AgentClient("app")
	defer ac.Close()
	if err := ac.Call("session.harnesses", nil, &hs, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent read the harnesses: %v", err)
	}
}

// An unverified provider through the real binary: a Qwen Code session built
// from the research report shows at the full level with the mark, its answer
// reads, `smif prompt` refuses it, and doctor calls the format unchecked
// and fails nothing for it, also when a Codex rollout differs from the
// reading. The files are not from a real session. They prove the wiring.
func TestUnverifiedProviderLevel(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	cwd := e.Repos["app"]
	sleeper := exec.Command("sleep", "120") // stands in for the agent
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	})
	const id = "5f0d0000-0000-4000-8000-000000000001"
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transcript, err := os.ReadFile("../internal/harness/qwen/testdata/report-idle.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	qwen := filepath.Join(e.Home, ".qwen")
	write(filepath.Join(qwen, "projects", "-repo", "chats", id+".jsonl"), string(transcript))
	write(filepath.Join(qwen, "sessions", fmt.Sprintf("%d.json", sleeper.Process.Pid)), fmt.Sprintf(
		`{"schemaVersion":1,"pid":%d,"sessionId":%q,"cwd":%q,"name":"qwenwork","startedAt":%d,"kind":"tui","ipcToken":"sentinel-token"}`,
		sleeper.Process.Pid, id, cwd, time.Now().UnixMilli()))
	write(filepath.Join(e.Home, ".codex", "sessions", "2026", "11", "05", "rollout-x.jsonl"), `{"type":"a_new_format"}`+"\n")
	e.Start()

	var ss []api.SessionView
	e.Call("session.list", nil, &ss)
	if len(ss) != 1 || ss[0].Harness != "qwen" || ss[0].Level != "full" || !ss[0].Unverified || ss[0].Turn != "idle" || ss[0].LastAnswer != "Done." ||
		ss[0].CanPrompt || !strings.Contains(ss[0].Why, "unverified") || ss[0].Agent != "app" {
		t.Fatalf("list: %+v", ss)
	}
	out, stderr, err := e.CLIDir(cwd, "", nil, "sessions")
	if err != nil || !strings.Contains(out, "full unverified") || strings.Contains(out, "sentinel-token") {
		t.Fatalf("sessions: %v %s\n%s", err, stderr, out)
	}
	_, stderr, err = e.CLIDir(cwd, "", nil, "prompt", "qwenwork", "run", "the", "tests")
	if err == nil || !strings.Contains(stderr, "unverified") {
		t.Fatalf("prompt: %v %s", err, stderr)
	}
	doctor, _ := e.CLI("", "doctor")
	for _, want := range []string{"[info] qwen format unchecked", "[info] codex format unchecked", "[info] opencode format unchecked",
		"[info] copilot format unchecked", "[info] droid format unchecked", "differ from the source reading"} {
		if !strings.Contains(doctor, want) {
			t.Errorf("doctor lacks %q:\n%s", want, doctor)
		}
	}
	for _, l := range strings.Split(doctor, "\n") {
		if strings.HasPrefix(l, "[FAIL]") && (strings.Contains(l, "codex") || strings.Contains(l, "qwen")) {
			t.Errorf("doctor failed a provider: %s", l)
		}
	}
	if strings.Contains(doctor, "sentinel-token") {
		t.Errorf("the token got out:\n%s", doctor)
	}
}
