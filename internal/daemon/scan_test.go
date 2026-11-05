package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// The JSON here is real herdr 0.9.5 output, seen 2026-11-05 on a named test
// session running `exec -a codex cat -v`, with the pid and folder replaced
// and the agent renamed: pi has no provider, so herdr alone decides here.
func herdrAgentJSON(status, cwd string) string { return herdrNamedJSON("pi", status, cwd) }

func herdrNamedJSON(agent, status, cwd string) string {
	return fmt.Sprintf(`{"agent":%q,"agent_status":%q,"cwd":%q,"focused":true,"foreground_cwd":%q,"pane_id":"w1:p1",`+
		`"revision":0,"state_change_seq":1,"tab_id":"w1:t1","terminal_id":"term_65f83469d314a1","workspace_id":"w1"}`, agent, status, cwd, cwd)
}

type fakeHerdr struct {
	dir, cwd string
	pid      int
	agent    string
}

// startFakeHerdr puts testdata/fakeherdr in place of herdr, with one running
// session and one pi agent, and a live process as its foreground.
func startFakeHerdr(t *testing.T, status string) *fakeHerdr {
	t.Helper()
	bin, err := filepath.Abs("../../testdata/fakeherdr/herdr")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	f := &fakeHerdr{dir: t.TempDir(), cwd: t.TempDir(), pid: cmd.Process.Pid, agent: "pi"}
	t.Setenv("SMIF_HERDR", bin)
	t.Setenv("FAKE_HERDR_DIR", f.dir)
	f.write(t, "sessions.json", `{"sessions":[{"default":true,"name":"default","running":false},{"default":false,"name":"smifh-test","running":true}]}`)
	f.write(t, "panes.json", `{"id":"cli:pane:list","result":{"panes":[{"agent":"pi","pane_id":"w1:p1","tab_id":"w1:t1","workspace_id":"w1"}],"type":"pane_list"}}`)
	f.write(t, "info.json", fmt.Sprintf(`{"id":"cli:pane:process_info","result":{"process_info":{"foreground_process_group_id":%d,`+
		`"foreground_processes":[{"argv":["pi","-v"],"argv0":"pi","name":"cat","pid":%d}],"pane_id":"w1:p1","shell_pid":%d},"type":"pane_process_info"}}`, f.pid, f.pid, f.pid))
	f.setIntegrations(t, herdrStatus("pi", "current (v9)"))
	f.setStatus(t, status)
	return f
}

// herdrStatus builds `herdr integration status` output. The line shapes and
// the state words are real herdr 0.9.5 output, seen 2026-11-05 under a temp
// HOME. The path differs from agent to agent, and smif does not read it.
func herdrStatus(pairs ...string) string {
	var b strings.Builder
	for i := 0; i < len(pairs); i += 2 {
		fmt.Fprintf(&b, "%s: %s (/home/u/.%[1]s/herdr-agent-state.sh)\n", pairs[i], pairs[i+1])
	}
	return b.String()
}

// setIntegrations changes what `integration status` says and drops the
// cached answer, so the next read sees it.
func (f *fakeHerdr) setIntegrations(t *testing.T, status string) {
	t.Helper()
	f.write(t, "integrations.txt", status)
	herdrIntegrations.forget()
}

func (f *fakeHerdr) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setStatus changes what both `agent list` and `agent get` say.
func (f *fakeHerdr) setStatus(t *testing.T, status string) {
	t.Helper()
	f.setListed(t, status)
	f.setLive(t, status)
}

func (f *fakeHerdr) setListed(t *testing.T, status string) {
	t.Helper()
	f.write(t, "agents.json", `{"id":"cli:agent:list","result":{"agents":[`+herdrNamedJSON(f.agent, status, f.cwd)+`],"type":"agent_list"}}`)
}

func (f *fakeHerdr) setLive(t *testing.T, status string) {
	t.Helper()
	f.write(t, "get.json", `{"id":"cli:agent:get","result":{"agent":`+herdrNamedJSON(f.agent, status, f.cwd)+`,"type":"agent_info"}}`)
}

func (f *fakeHerdr) prompts(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "prompts.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// scanNow runs one scan and keeps it, so a test need not wait for one.
func scanNow(d *Daemon) {
	rows := d.runScan(context.Background())
	d.scan.mu.Lock()
	defer d.scan.mu.Unlock()
	d.scan.rows, d.scan.at = rows, time.Now()
}

// scanDaemon is a test daemon with the scan on and a terminal that really
// drives herdr. Only herdr-level rows exist here, so nothing reaches a real
// terminal: the Level branch runs before any AppleScript. The host's process
// table stays out of it.
func scanDaemon(t *testing.T) *Daemon {
	t.Helper()
	d, _ := testDaemon(t)
	d.scan.run = d.runScan
	d.scan.procs = func(context.Context) []proc { return nil }
	d.terminal = autoTerminal{harnesses: d.harnesses}
	return d
}

func agentRow(t *testing.T, d *Daemon) api.SessionView { return namedRow(t, d, "pi") }

func namedRow(t *testing.T, d *Daemon, agent string) api.SessionView {
	t.Helper()
	ss, err := d.sessionList()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.Harness == agent {
			return s
		}
	}
	t.Fatalf("no %s row: %+v", agent, ss)
	return api.SessionView{}
}

func TestParseHerdrAgents(t *testing.T) {
	a, err := parseHerdrAgents([]byte(`{"agents":[` + herdrAgentJSON("idle", "/w/app") + `],"type":"agent_list"}`))
	if err != nil || len(a) != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	if a[0].Agent != "pi" || a[0].Status != "idle" || a[0].ForegroundCwd != "/w/app" || a[0].Pane != "w1:p1" || a[0].Terminal != "term_65f83469d314a1" {
		t.Fatalf("%+v", a[0])
	}
}

// Every herdr state maps to a turn; only idle and done may type, and a word
// herdr adds later counts as unknown.
func TestHerdrStateMap(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	for _, c := range []struct {
		status, turn string
		can          bool
	}{
		{"working", "busy", false},
		{"blocked", "waiting", false},
		{"idle", "idle", true},
		{"done", "idle", true},
		{"unknown", "unknown", false},
		{"sleeping", "unknown", false},
		{"", "unknown", false},
	} {
		f.setStatus(t, c.status)
		scanNow(d)
		v := agentRow(t, d)
		if v.Turn != c.turn || v.CanPrompt != c.can {
			t.Errorf("status %q: turn %q can %v, want %q %v (why %q)", c.status, v.Turn, v.CanPrompt, c.turn, c.can, v.Why)
		}
		if !c.can && v.Why == "" {
			t.Errorf("status %q: no reason why typing is off", c.status)
		}
		if v.Level != harness.LevelHerdr || v.Term != termHerdr || v.Repo != f.cwd || v.Title != "pi" || v.LastAnswer != "" || v.Activity != nil {
			t.Errorf("status %q: row %+v", c.status, v)
		}
		if v.TurnAt.IsZero() {
			t.Errorf("status %q: no turn time", c.status)
		}
	}
}

// The row keeps its id from scan to scan, and the id passes session.tail.
func TestHerdrRowIDIsStable(t *testing.T) {
	d := scanDaemon(t)
	startFakeHerdr(t, "idle")
	scanNow(d)
	a := agentRow(t, d)
	first := a.TurnAt
	scanNow(d)
	b := agentRow(t, d)
	if a.SessionID != b.SessionID || !harness.ValidID(a.SessionID) {
		t.Fatalf("ids %q %q", a.SessionID, b.SessionID)
	}
	if !b.TurnAt.Equal(first) {
		t.Fatalf("turn time moved with no state change: %v then %v", first, b.TurnAt)
	}
	if tail, err := d.sessionTail(api.SessionTailParams{SessionID: a.SessionID}); err != nil || len(tail.Turns) != 0 {
		t.Fatalf("tail %+v %v", tail, err)
	}
}

// An idle agent gets the prompt through `agent prompt`, with text that
// starts with a dash as it is, and no presence row stays behind.
func TestHerdrPromptGoesThroughAgentPrompt(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	scanNow(d)
	v := agentRow(t, d)
	for _, text := range []string{"run the tests", "-v is a flag"} {
		if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.prompts(t); got != "w1:p1 run the tests\nw1:p1 -v is a flag\n" {
		t.Fatalf("prompts %q", got)
	}
	if all, err := d.store.AllPresence(); err != nil || len(all) != 0 {
		t.Fatalf("presence rows %+v %v", all, err)
	}
	// By its name, through session.send, as `smif prompt pi …` does.
	scanNow(d)
	r, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{Name: "pi", Text: "by name"})
	if err != nil || r.Mode != "typed" {
		t.Fatalf("send %+v %v", r, err)
	}
	if !strings.HasSuffix(f.prompts(t), "w1:p1 by name\n") {
		t.Fatalf("prompts %q", f.prompts(t))
	}
}

// Safety rule of ADR-0019: a blocked agent and an unknown state refuse
// typing, on the list's state and on the state at the moment of typing.
func TestHerdrBlockedAndUnknownRefuseTyping(t *testing.T) {
	for _, status := range []string{"blocked", "unknown", "working"} {
		t.Run("listed "+status, func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, status)
			scanNow(d)
			v := agentRow(t, d)
			_, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"})
			if err == nil || f.prompts(t) != "" {
				t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
			}
			if _, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
				t.Fatal("session.send took a prompt")
			}
		})
		t.Run("stale idle, live "+status, func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, "idle")
			scanNow(d)
			v := agentRow(t, d)
			if !v.CanPrompt {
				t.Fatalf("row %+v", v)
			}
			f.setLive(t, status) // the list still says idle
			_, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"})
			if err == nil || !strings.Contains(err.Error(), "herdr says") || f.prompts(t) != "" {
				t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
			}
		})
		t.Run("direct type, live "+status, func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, status)
			p := store.Presence{Level: harness.LevelHerdr, Harness: "pi", PID: f.pid, Term: termHerdr}
			if err := d.terminal.Type(context.Background(), p, "yes"); err == nil || f.prompts(t) != "" {
				t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
			}
		})
	}
}

// Safety rule of ADR-0019: an agent with no idle rule reads as idle in herdr
// when no rule matches, so an approval dialog herdr does not know also reads
// as idle, and `agent prompt` does not refuse it. For such an agent, and for
// a name smif does not know, herdr's idle and done are unknown: no typing at
// any gate. Blocked and working keep their meaning. Real herdr 0.9.5 prints
// qodercli and agy for Qoder and Antigravity.
func TestHerdrNoIdleRuleNeverTypes(t *testing.T) {
	for _, name := range []string{"gemini", "copilot", "cursor", "droid", "qoder", "qodercli", "antigravity", "agy", "madeupagent"} {
		for _, status := range []string{"idle", "done"} {
			t.Run(name+" "+status, func(t *testing.T) {
				d := scanDaemon(t)
				f := startFakeHerdr(t, status)
				f.agent = name
				f.setStatus(t, status)
				scanNow(d)
				v := namedRow(t, d, name)
				if v.Turn != "unknown" || v.CanPrompt || v.Level != harness.LevelHerdr {
					t.Fatalf("row %+v", v)
				}
				why := "cannot tell idle from a dialog for " + name
				if name == "copilot" || name == "droid" {
					why = "unverified" // their provider's mark comes first
				}
				if !strings.Contains(v.Why, why) {
					t.Fatalf("why %q, want %q", v.Why, why)
				}
				if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
					t.Fatal("session.prompt took it")
				}
				if _, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
					t.Fatal("session.send took it")
				}
				p := store.Presence{Level: harness.LevelHerdr, Harness: name, PID: f.pid, Term: termHerdr}
				if err := d.terminal.Ready(p); err == nil || !strings.Contains(err.Error(), "cannot tell idle from a dialog for "+name) {
					t.Fatalf("Ready: %v", err)
				}
				if err := d.terminal.Type(context.Background(), p, "yes"); err == nil {
					t.Fatal("Type took it")
				}
				if got := f.prompts(t); got != "" {
					t.Fatalf("prompts %q", got)
				}
			})
		}
		t.Run(name+" keeps blocked and working", func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, "blocked")
			f.agent = name
			for status, turn := range map[string]string{"blocked": "waiting", "working": "busy"} {
				f.setStatus(t, status)
				scanNow(d)
				if v := namedRow(t, d, name); v.Turn != turn || v.CanPrompt {
					t.Fatalf("%s: row %+v", status, v)
				}
			}
		})
	}
}

// integrationCase runs one integration-only agent at idle and returns the
// row, after the row and the live check saw the given status.
func integrationCase(t *testing.T, name, state string) (*Daemon, *fakeHerdr, api.SessionView) {
	t.Helper()
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	f.agent = name
	f.setIntegrations(t, herdrStatus(name, state))
	f.setStatus(t, "idle")
	scanNow(d)
	return d, f, namedRow(t, d, name)
}

func assertNoTyping(t *testing.T, d *Daemon, f *fakeHerdr, v api.SessionView) {
	t.Helper()
	if v.Turn != "unknown" || v.CanPrompt || v.Level != harness.LevelHerdr {
		t.Fatalf("row %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
		t.Fatal("session.prompt took it")
	}
	if _, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
		t.Fatal("session.send took it")
	}
	p := store.Presence{Level: harness.LevelHerdr, Harness: v.Harness, PID: f.pid, Term: termHerdr}
	if err := d.terminal.Ready(p); err == nil {
		t.Fatal("Ready took it")
	}
	if err := d.terminal.Type(context.Background(), p, "yes"); err == nil {
		t.Fatal("Type took it")
	}
	if got := f.prompts(t); got != "" {
		t.Fatalf("prompts %q", got)
	}
}

// An agent whose only idle rule is herdr's integration: with no integration
// herdr's idle means that no rule matched, so smif does not type.
func TestHerdrIntegrationGate(t *testing.T) {
	agents := []string{"kimi", "omp", "pi", "kilo", "mastracode"}
	for _, name := range agents {
		for _, state := range []string{"not installed", "outdated (legacy < v8)", "needs repair (missing hook entry)"} {
			t.Run(name+" "+state, func(t *testing.T) {
				d, f, v := integrationCase(t, name, state)
				if !strings.Contains(v.Why, "no current herdr integration for "+name) {
					t.Fatalf("why %q", v.Why)
				}
				assertNoTyping(t, d, f, v)
			})
		}
		t.Run(name+" current", func(t *testing.T) {
			d, f, v := integrationCase(t, name, "current (v8)")
			if v.Turn != "idle" || !v.CanPrompt {
				t.Fatalf("row %+v", v)
			}
			if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "run"}); err != nil {
				t.Fatal(err)
			}
			p := store.Presence{Level: harness.LevelHerdr, Harness: name, PID: f.pid, Term: termHerdr}
			if err := d.terminal.Type(context.Background(), p, "again"); err != nil {
				t.Fatal(err)
			}
			if got := f.prompts(t); got != "w1:p1 run\nw1:p1 again\n" {
				t.Fatalf("prompts %q", got)
			}
		})
	}

	// OpenCode has a read-only provider, so its gate is the state, not typing.
	t.Run("opencode not installed", func(t *testing.T) {
		_, _, v := integrationCase(t, "opencode", "not installed")
		if v.Turn != "unknown" || v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
	})
	t.Run("opencode current", func(t *testing.T) {
		_, _, v := integrationCase(t, "opencode", "current (v13)")
		if v.Turn != "idle" || v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
	})

	// An unreadable answer counts as not installed.
	unreadable := map[string]func(t *testing.T, f *fakeHerdr){
		"text":          func(t *testing.T, f *fakeHerdr) { f.setIntegrations(t, "kimi is fine\n") },
		"json":          func(t *testing.T, f *fakeHerdr) { f.setIntegrations(t, `{"kimi":"current"}`) },
		"empty":         func(t *testing.T, f *fakeHerdr) { f.setIntegrations(t, "") },
		"other agent":   func(t *testing.T, f *fakeHerdr) { f.setIntegrations(t, herdrStatus("omp", "current (v10)")) },
		"no state word": func(t *testing.T, f *fakeHerdr) { f.setIntegrations(t, "kimi: \n") },
		"exit 1": func(t *testing.T, f *fakeHerdr) {
			f.write(t, "integrations.fail", "")
			f.setIntegrations(t, herdrStatus("kimi", "current (v8)"))
		},
		"no answer": func(t *testing.T, f *fakeHerdr) {
			if err := os.Remove(filepath.Join(f.dir, "integrations.txt")); err != nil {
				t.Fatal(err)
			}
			herdrIntegrations.forget()
		},
	}
	for name, spoil := range unreadable {
		t.Run("unreadable "+name, func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, "idle")
			f.agent = "kimi"
			f.setStatus(t, "idle")
			spoil(t, f)
			scanNow(d)
			assertNoTyping(t, d, f, namedRow(t, d, "kimi"))
		})
	}

	// The live check reads the status too: a row that still shows idle
	// does not type once the integration is gone.
	t.Run("live check", func(t *testing.T) {
		d, f, v := integrationCase(t, "kimi", "current (v8)")
		if !v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
		f.setIntegrations(t, herdrStatus("kimi", "not installed"))
		if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil || f.prompts(t) != "" {
			t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
		}
	})

	// The answer stays for a moment, so a scan of many agents runs one command.
	t.Run("cache", func(t *testing.T) {
		d, f, v := integrationCase(t, "kimi", "current (v8)")
		f.write(t, "integrations.txt", herdrStatus("kimi", "not installed"))
		scanNow(d)
		if v2 := namedRow(t, d, "kimi"); v2.Turn != "idle" || v2.SessionID != v.SessionID {
			t.Fatalf("row %+v", v2)
		}
		herdrIntegrations.mu.Lock()
		herdrIntegrations.at = time.Now().Add(-integrationEvery)
		herdrIntegrations.mu.Unlock()
		scanNow(d)
		if v2 := namedRow(t, d, "kimi"); v2.Turn != "unknown" {
			t.Fatalf("row %+v", v2)
		}
	})

	// An agent with its own idle rule does not depend on the status.
	t.Run("idle rule needs no integration", func(t *testing.T) {
		d := scanDaemon(t)
		f := startFakeHerdr(t, "idle")
		f.agent = "maki"
		f.setIntegrations(t, "not a status")
		f.setStatus(t, "idle")
		scanNow(d)
		if v := namedRow(t, d, "maki"); v.Turn != "idle" || !v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
	})
}

// The real shapes, one line for each state word.
func TestHerdrIntegrationsCurrent(t *testing.T) {
	got := harness.HerdrIntegrationsCurrent("pi: current (v9) (/h/.pi/agent/extensions/herdr-agent-state.ts)\n" +
		"omp: outdated (legacy < v10) (/h/.omp/agent/extensions/herdr-omp-agent-state.ts)\n" +
		"kimi: not installed (/h/.kimi-code/hooks/herdr-agent-state.sh)\n" +
		"letta (experimental): current (v2) (/h/.letta/hooks/herdr-agent-session.sh)\n")
	if !got["pi"] || got["omp"] || got["kimi"] || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// The rule stays narrow: an agent with an idle rule, or with a state source
// that covers permissions, still types. pi has no provider, so herdr alone
// decides for it in the tests above; claude has a verified provider.
func TestHerdrIdleRuleStillTypes(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		d := scanDaemon(t)
		f := startFakeHerdr(t, "idle")
		f.agent = "claude"
		f.setStatus(t, "idle")
		scanNow(d)
		v := namedRow(t, d, "claude")
		if v.Turn != "idle" || !v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
		if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "run"}); err != nil {
			t.Fatal(err)
		}
		if got := f.prompts(t); got != "w1:p1 run\n" {
			t.Fatalf("prompts %q", got)
		}
	})
	// Codex's provider is unverified, so session.prompt stays closed for it
	// (TestUnverifiedProviderAtHerdrLevel). This rule must not be what stops
	// it: the state stays idle, and the route under that gate still types.
	t.Run("codex", func(t *testing.T) {
		d := scanDaemon(t)
		f := startFakeHerdr(t, "idle")
		f.agent = "codex"
		f.setStatus(t, "idle")
		scanNow(d)
		v := namedRow(t, d, "codex")
		if v.Turn != "idle" || v.CanPrompt || !strings.Contains(v.Why, "unverified") {
			t.Fatalf("row %+v", v)
		}
		p := store.Presence{Level: harness.LevelHerdr, Harness: "codex", PID: f.pid, Term: termHerdr}
		if err := d.terminal.Type(context.Background(), p, "run"); err != nil {
			t.Fatal(err)
		}
		if got := f.prompts(t); got != "w1:p1 run\n" {
			t.Fatalf("prompts %q", got)
		}
	})
	t.Run("codex with no provider", func(t *testing.T) {
		d := scanDaemon(t)
		d.harnesses = harness.NewRegistry()
		f := startFakeHerdr(t, "idle")
		f.agent = "codex"
		f.setStatus(t, "idle")
		scanNow(d)
		v := namedRow(t, d, "codex")
		if v.Turn != "idle" || !v.CanPrompt {
			t.Fatalf("row %+v", v)
		}
		if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "run"}); err != nil {
			t.Fatal(err)
		}
		if got := f.prompts(t); got != "w1:p1 run\n" {
			t.Fatalf("prompts %q", got)
		}
	})
}

// The name at the moment of typing counts, not the list's: a pane that now
// runs an agent with no idle rule takes no text.
func TestHerdrLiveAgentNameCounts(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	scanNow(d)
	v := agentRow(t, d)
	if !v.CanPrompt {
		t.Fatalf("row %+v", v)
	}
	f.agent = "gemini"
	f.setLive(t, "idle") // the list still says pi
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil || f.prompts(t) != "" {
		t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
	}
}

// A process that left the pane's foreground takes no text.
func TestHerdrPromptNeedsPaneForeground(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	p := store.Presence{Level: harness.LevelHerdr, Harness: "pi", PID: f.pid + 1, Term: termHerdr}
	if err := d.terminal.Type(context.Background(), p, "yes"); err == nil || f.prompts(t) != "" {
		t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
	}
}

// A busy herdr-level Claude row must not start a fork: its id is no real
// session id, so `claude --resume` has nothing to resume.
func TestScannedRowHasNoFork(t *testing.T) {
	d, _ := testDaemon(t)
	s := store.Presence{Level: harness.LevelHerdr, Harness: "claude", Repo: "/r", Turn: "busy"}
	if _, _, err := d.forkTarget(s); err == nil || !strings.Contains(err.Error(), "no fork") {
		t.Fatalf("fork target: %v", err)
	}
}

// Unverified providers never type: a herdr-level row of an agent whose
// provider is unverified stays read only.
func TestHerdrLevelKeepsProviderReadOnly(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	d.harnesses.Register(statefulFake{&fakeProvider{name: "pi", process: "pi"}})
	scanNow(d)
	v := agentRow(t, d)
	if v.CanPrompt || !strings.Contains(v.Why, "unverified") {
		t.Fatalf("row %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil || f.prompts(t) != "" {
		t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
	}
}

// A session a provider already reports shows once, at the full level.
func TestNoDoubleRowWithProvider(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle")
	d.harnesses.Register(statefulFake{&fakeProvider{name: "claude", process: "claude", verified: true, sessions: []harness.Session{
		{PID: f.pid, SessionID: "11111111-2222-3333-4444-555555555555", Cwd: f.cwd, Status: "idle", StatusAt: time.Now()}}}})
	scanNow(d)
	ss, err := d.sessionList()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Harness != "claude" || ss[0].Level != harness.LevelFull {
		t.Fatalf("rows %+v", ss)
	}
}

// A tty in common is one session too: an agent may have a child in the
// pane's foreground while its own pid is not there.
func TestNoDoubleRowsByTTY(t *testing.T) {
	d, _ := testDaemon(t)
	cwd := t.TempDir()
	alive := os.Getpid()
	rows := d.addScanned([]store.Presence{{SessionID: "full", TTY: "/dev/ttys7", PID: 1}}, []scanRow{
		{level: harness.LevelHerdr, harness: "claude", id: "same-tty", cwd: cwd, pid: alive, pids: []int{alive}, tty: "/dev/ttys7"},
		{level: harness.LevelHerdr, harness: "pi", id: "herdr-row", cwd: cwd, pid: alive, pids: []int{alive}, tty: "/dev/ttys8"},
		{level: harness.LevelListed, harness: "pi", id: "listed-row", cwd: cwd, pid: alive, pids: []int{alive}, tty: "/dev/ttys8"},
		{level: harness.LevelListed, harness: "gemini", id: "other-row", cwd: cwd, pid: alive, pids: []int{alive}, tty: "/dev/ttys9"},
	})
	var got []string
	for _, r := range rows {
		got = append(got, r.SessionID)
	}
	if !slices.Equal(got, []string{"full", "herdr-row"}) {
		t.Fatalf("rows %v", got)
	}
}

// herdr not installed, or installed with no server: no rows, no error, and
// the list stays under 300 ms, also when the fake hangs.
func TestScanDownGivesNoRows(t *testing.T) {
	cases := []struct {
		name string
		env  func(t *testing.T)
	}{
		{"not installed", func(t *testing.T) { t.Setenv("SMIF_HERDR", "") }},
		{"no server", func(t *testing.T) {
			startFakeHerdr(t, "idle").write(t, "sessions.json", `{"sessions":[{"default":true,"name":"default","running":false}]}`)
		}},
		{"command fails", func(t *testing.T) { t.Setenv("SMIF_HERDR", "/nonexistent/herdr") }},
		{"bad output", func(t *testing.T) { startFakeHerdr(t, "idle").write(t, "sessions.json", `not json`) }},
		{"hangs", func(t *testing.T) { startFakeHerdr(t, "idle").write(t, "sleep", "3") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.env(t)
			d := scanDaemon(t)
			d.scan.procs = nil // the real ps counts in the time
			for i := range 3 {
				start := time.Now()
				ss, err := d.sessionList()
				took := time.Since(start)
				if err != nil {
					t.Fatal(err)
				}
				if took >= 300*time.Millisecond {
					t.Fatalf("call %d took %v", i, took)
				}
				for _, s := range ss {
					if s.Level == harness.LevelHerdr {
						t.Fatalf("row %+v", s)
					}
				}
			}
		})
	}
}

// parseProcs reads macOS and Linux ps output.
func TestParseProcs(t *testing.T) {
	got := parseProcs("  101 ??       /usr/sbin/cfprefsd agent\n  102 ttys003  codex -v\n  103 pts/2    node /a/bin/codex\n  bad\n  104 ?        x y\n")
	if len(got) != 4 || got[0].tty != "" || got[1].tty != "/dev/ttys003" || got[2].tty != "/dev/pts/2" || got[3].tty != "" {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got[2].argv, []string{"node", "/a/bin/codex"}) {
		t.Fatalf("%+v", got[2])
	}
}

// Listed: a known process on a terminal gives name and folder, one row per
// agent and terminal; a process with no terminal is no session.
func TestListedRows(t *testing.T) {
	d, _ := testDaemon(t)
	me := os.Getpid() // lsof or /proc gives this process's real folder
	procs := []proc{
		{pid: 2, tty: "/dev/ttys1", argv: []string{"/bin/zsh"}},
		{pid: me, tty: "/dev/ttys2", argv: []string{"gemini"}},
		{pid: me, tty: "/dev/ttys2", argv: []string{"node", "/x/bin/gemini"}},
		{pid: 3, argv: []string{"opencode", "serve"}},
		{pid: me, tty: "/dev/ttys3", argv: []string{"copilot"}},
	}
	herdr := []scanRow{{tty: "/dev/ttys3"}}
	rows := d.scan.listedRows(context.Background(), procs, herdr, func(int) string { return termApple })
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	r := rows[0]
	wd, _ := os.Getwd()
	if r.harness != "gemini" || r.level != harness.LevelListed || r.turn != "unknown" || r.term != termApple {
		t.Fatalf("row %+v", r)
	}
	a, _ := filepath.EvalSymlinks(wd)
	b, _ := filepath.EvalSymlinks(r.cwd)
	if a != b {
		t.Fatalf("cwd %q, want %q", r.cwd, wd)
	}
}

// A listed session shows why typing is off and refuses it at each gate.
func TestListedNeverTypes(t *testing.T) {
	d := scanDaemon(t)
	f := startFakeHerdr(t, "idle") // herdr would take it, so only the gate stops it
	cwd := t.TempDir()
	me := f.pid
	d.scan.run = func(context.Context) []scanRow {
		return []scanRow{{level: harness.LevelListed, harness: "gemini", id: scanID("p", "1", "gemini"), cwd: cwd, pid: me, pids: []int{me},
			tty: "/dev/ttys900", term: termHerdr, turn: "idle"}} // idle must not count here
	}
	ss, err := d.sessionList()
	if err != nil || len(ss) != 1 {
		t.Fatalf("%+v %v", ss, err)
	}
	v := ss[0]
	if v.Level != harness.LevelListed || v.Turn != "unknown" || v.CanPrompt || !strings.Contains(v.Why, "listed only") || v.Title != "gemini" {
		t.Fatalf("row %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
		t.Fatal("session.prompt took it")
	}
	if _, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: v.SessionID, Text: "yes"}); err == nil {
		t.Fatal("session.send took it")
	}
	p := store.Presence{Level: harness.LevelListed, Harness: "gemini", PID: me, TTY: "/dev/ttys900", Term: termHerdr, Turn: "idle"}
	if err := d.terminal.Ready(p); err == nil {
		t.Fatal("Ready passed a listed session")
	}
	if err := d.terminal.Type(context.Background(), p, "yes"); err == nil || f.prompts(t) != "" {
		t.Fatalf("Type: err %v, prompts %q", err, f.prompts(t))
	}
	if err := d.terminal.Enter(context.Background(), p); err == nil {
		t.Fatal("Enter took a listed session")
	}
}

// The view is JSON that names the level.
func TestSessionViewLevelJSON(t *testing.T) {
	b, err := json.Marshal(api.SessionView{Harness: "pi", Level: harness.LevelHerdr})
	if err != nil || !strings.Contains(string(b), `"level":"herdr"`) {
		t.Fatalf("%s %v", b, err)
	}
}
