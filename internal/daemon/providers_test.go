package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/harness/codex"
	"github.com/appnice/smif/internal/harness/copilot"
	"github.com/appnice/smif/internal/harness/droid"
	"github.com/appnice/smif/internal/harness/opencode"
	"github.com/appnice/smif/internal/harness/qwen"
	"github.com/appnice/smif/internal/notify"
	"github.com/appnice/smif/internal/store"
)

// The daemon with the real unverified providers, on files built from the
// research report (see testdata/README.md in each provider): a
// green test proves the gate and the parsers, not the real file format.

const (
	codexID    = "019a0000-0000-7000-8000-000000000001"
	qwenID     = "5f0d0000-0000-4000-8000-000000000001"
	opencodeID = "ses_0a1b2c3d4e5f"
	copilotID  = "4de47255-0000-4000-8000-000000000001"
	droidID    = "7c1e0000-0000-4000-8000-000000000001"
)

func fixture(t *testing.T, provider, name, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "harness", provider, "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "CWD", cwd)
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// codexSession registers a Codex provider whose process list holds this
// test's process, on a terminal, in cwd.
func codexSession(t *testing.T, d *Daemon, name, cwd string) string {
	t.Helper()
	home := t.TempDir()
	put(t, filepath.Join(home, "sessions", "2026", "11", "05", "rollout-2026-11-05T10-00-00-"+codexID+".jsonl"), fixture(t, "codex", name, cwd))
	p := codex.Proc{PID: os.Getpid(), TTY: "/dev/ttys999", Argv: []string{"codex"}, Start: time.Now().Add(-time.Hour), Cwd: cwd}
	d.harnesses.Register(codex.NewWithProcs(home, func() []codex.Proc { return []codex.Proc{p} }))
	return codexID
}

// qwenSession registers a Qwen Code provider with a session of this test's
// process in cwd.
func qwenSession(t *testing.T, d *Daemon, name, cwd string) string {
	t.Helper()
	home := t.TempDir()
	put(t, filepath.Join(home, "projects", "-repo", "chats", qwenID+".jsonl"), fixture(t, "qwen", name, cwd))
	put(t, filepath.Join(home, "sessions", "1.json"), fmt.Sprintf(`{"schemaVersion":1,"pid":%d,"sessionId":%q,"cwd":%q,"name":"qwen-work","startedAt":%d,"kind":"tui","ipcToken":"never-shown"}`,
		os.Getpid(), qwenID, cwd, time.Now().UnixMilli()))
	d.harnesses.Register(qwen.New(home, home))
	return qwenID
}

// opencodeSession builds a database and registers an OpenCode provider
// whose process list holds this test's process, on a terminal, in cwd. The
// test's process also stands in for the shared server.
func opencodeSession(t *testing.T, d *Daemon, name, cwd string) string {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	for _, f := range []string{"schema.sql", name} {
		if _, err := db.Exec(strings.ReplaceAll(fixture(t, "opencode", f, cwd), "NOW", now)); err != nil {
			t.Fatal(err)
		}
	}
	p := harness.Proc{PID: os.Getpid(), TTY: "/dev/ttys999", Argv: []string{"opencode"}, Start: time.Now().Add(-time.Hour), Cwd: cwd}
	server := harness.Proc{PID: os.Getpid(), Argv: []string{"opencode", "serve", "--service"}, Start: time.Now().Add(-time.Hour)}
	d.harnesses.Register(opencode.NewWithProcs(path, func() []harness.Proc { return []harness.Proc{p, server} }))
	return opencodeID
}

// copilotSession registers a Copilot provider with a session that this
// test's process holds, in cwd.
func copilotSession(t *testing.T, d *Daemon, name, cwd string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "session-state", copilotID)
	put(t, filepath.Join(dir, "events.jsonl"), fixture(t, "copilot", name, cwd))
	put(t, filepath.Join(dir, fmt.Sprintf("inuse.%d.lock", os.Getpid())), "")
	d.harnesses.Register(copilot.NewWithLook(home, func(int) (harness.Proc, bool) {
		return harness.Proc{TTY: "/dev/ttys999", Argv: []string{"copilot"}, Start: time.Now().Add(-time.Hour)}, true
	}))
	return copilotID
}

// droidSession registers a Droid provider whose process list holds this
// test's process, on a terminal, in cwd.
func droidSession(t *testing.T, d *Daemon, name, cwd string) string {
	t.Helper()
	home := t.TempDir()
	put(t, filepath.Join(home, "sessions", "-repo", droidID+".jsonl"), fixture(t, "droid", name, cwd))
	p := harness.Proc{PID: os.Getpid(), TTY: "/dev/ttys999", Argv: []string{"droid"}, Start: time.Now().Add(-time.Hour), Cwd: cwd}
	d.harnesses.Register(droid.NewWithProcs(home, func() []harness.Proc { return []harness.Proc{p} }))
	return droidID
}

// agentOwns gives the session's folder an agent, so that a fork has a
// worker and a prompt can name the agent: only the provider can stop it.
func agentOwns(d *Daemon, id, repo string) {
	cfg := *d.Config()
	cfg.Agents = map[string]*config.Agent{id: {Repo: repo}}
	d.cfg.Store(&cfg)
}

var unverifiedCases = []struct {
	harness, idle, working string
	start                  func(*testing.T, *Daemon, string, string) string
}{
	{"qwen", "report-idle.jsonl", "report-working.jsonl", qwenSession},
	{"copilot", "report-idle.jsonl", "report-working.jsonl", copilotSession},
	{"droid", "report-idle.jsonl", "report-working.jsonl", droidSession},
}

// OpenCode is verified, and a pending tool call still does not read idle: an
// approval looks the same as a running tool.
func TestOpenCodeStateGatesTyping(t *testing.T) {
	d, _ := testDaemon(t)
	v := viewOf(t, d, opencodeSession(t, d, "report-idle.sql", t.TempDir()))
	if v.Unverified || v.Turn != "idle" || strings.Contains(v.Why, "unverified") {
		t.Fatalf("idle: %+v", v)
	}
	d, _ = testDaemon(t)
	v = viewOf(t, d, opencodeSession(t, d, "report-pending.sql", t.TempDir()))
	if v.Turn == "idle" || v.CanPrompt {
		t.Fatalf("pending: %+v", v)
	}
}

// session.list shows them at the full level, with the mark and the answer,
// and session.tail reads them.
func TestUnverifiedProvidersInSessionList(t *testing.T) {
	for _, c := range unverifiedCases {
		t.Run(c.harness, func(t *testing.T) {
			d, _ := testDaemon(t)
			cwd := t.TempDir()
			id := c.start(t, d, c.idle, cwd)
			v := viewOf(t, d, id)
			if v.Harness != c.harness || v.Level != harness.LevelFull || !v.Unverified || v.Turn != "idle" || v.LastAnswer == "" || v.CanPrompt {
				t.Fatalf("view: %+v", v)
			}
			if !strings.Contains(v.Why, "unverified") {
				t.Fatalf("why: %q", v.Why)
			}
			tail, err := d.sessionTail(api.SessionTailParams{SessionID: id})
			if err != nil || len(tail.Turns) < 2 {
				t.Fatalf("tail: %+v %v", tail, err)
			}
			if vv := viewOf(t, d, id); vv.Activity == nil || vv.Activity.Prompt == "" {
				t.Fatalf("activity: %+v", vv)
			}
		})
	}
}

// ADR-0019 at the daemon level, on the real providers: every writing route
// refuses with a plain reason, in an idle session too, and with a terminal
// and an agent known, so that nothing but the provider's mark stops it. No
// key reaches the terminal. A focus sends no key either.
func TestUnverifiedProvidersRefuseEveryWrite(t *testing.T) {
	for _, c := range unverifiedCases {
		for _, fx := range []string{c.idle, c.working} {
			t.Run(c.harness+"/"+fx, func(t *testing.T) {
				d, typed := testDaemon(t)
				cwd := t.TempDir()
				id := c.start(t, d, fx, cwd)
				row(t, d, id, c.harness, cwd)
				agentOwns(d, "app", cwd)
				ctx := context.Background()
				for what, err := range map[string]error{
					"session.prompt by id":    second(d.sessionPrompt(ctx, api.SessionPromptParams{SessionID: id, Text: "hello"})),
					"session.prompt by agent": second(d.sessionPrompt(ctx, api.SessionPromptParams{Agent: "app", Text: "hello"})),
					"session.send":            second(d.sessionSend(ctx, auth.Principal{}, api.SessionSendParams{SessionID: id, Text: "hello"})),
				} {
					if err == nil || strings.Contains(err.Error(), "hello") {
						t.Fatalf("%s: %v", what, err)
					}
				}
				_, err := d.sessionPrompt(ctx, api.SessionPromptParams{SessionID: id, Text: "hello"})
				if !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "unverified") {
					t.Fatalf("prompt: %v", err)
				}
				_, err = d.sessionSend(ctx, auth.Principal{}, api.SessionSendParams{SessionID: id, Text: "hello"})
				if !api.IsCode(err, api.CodeRejected) {
					t.Fatalf("send: %v", err)
				}
				s := store.Presence{Harness: c.harness, SessionID: id, Repo: cwd, Cwd: cwd}
				if _, _, err := d.forkTarget(s); !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "unverified") {
					t.Fatalf("fork: %v", err)
				}
				if got := typedText(t, typed); got != "" {
					t.Fatalf("typed into a read-only session: %s", got)
				}
				if _, err := d.sessionFocus(ctx, api.SessionFocusParams{SessionID: id}); err != nil {
					t.Fatalf("focus: %v", err)
				}
				if got := typedText(t, typed); strings.Contains(got, `"text"`) || !strings.Contains(got, `"focus":true`) {
					t.Fatalf("a focus typed: %s", got)
				}
			})
		}
	}
}

func second[T any](_ T, err error) error { return err }

// A pending tool call: the state is unknown, the step shows.
func TestPendingCallIsUnknownNotIdle(t *testing.T) {
	for _, c := range []struct {
		harness, pending string
		start            func(*testing.T, *Daemon, string, string) string
	}{
		{"codex", "report-pending.jsonl", codexSession},
		{"qwen", "report-pending.jsonl", qwenSession},
		{"opencode", "report-pending.sql", opencodeSession},
		{"copilot", "report-tool.jsonl", copilotSession},
		{"droid", "report-pending.jsonl", droidSession},
	} {
		t.Run(c.harness, func(t *testing.T) {
			d, _ := testDaemon(t)
			id := c.start(t, d, c.pending, t.TempDir())
			v := viewOf(t, d, id)
			if v.Turn != "unknown" || v.CanPrompt || v.Activity == nil || v.Activity.Now == nil {
				t.Fatalf("view: %+v", v)
			}
		})
	}
}

// Copilot's log records a permission: the session waits, and the step shows.
func TestCopilotPermissionWaits(t *testing.T) {
	d, typed := testDaemon(t)
	id := copilotSession(t, d, "report-permission.jsonl", t.TempDir())
	v := viewOf(t, d, id)
	if v.Turn != "waiting" || v.CanPrompt || v.Activity == nil || v.Activity.Now == nil || v.Activity.Now.Target != "go test ./..." {
		t.Fatalf("view: %+v", v)
	}
	if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: id, Text: "yes"}); !api.IsCode(err, api.CodeRejected) {
		t.Fatalf("prompt: %v", err)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed: %s", got)
	}
}

// watchNotes records what the session watcher sends.
type watchNotes struct{ kinds []string }

func (n *watchNotes) Notify(ev notify.Event) error {
	n.kinds = append(n.kinds, ev.Kind)
	return nil
}

// Codex passed a check on a real session: its row is full and only a state
// or a missing terminal stops typing.
func TestCodexIsVerified(t *testing.T) {
	d, _ := testDaemon(t)
	id := codexSession(t, d, "report-idle-paginated.jsonl", t.TempDir())
	if v := viewOf(t, d, id); v.Unverified || v.Level != harness.LevelFull || v.Turn != "idle" || strings.Contains(v.Why, "unverified") {
		t.Fatalf("view: %+v", v)
	}
}

// README promises notifications at the level Full, read only. The rows come
// from the fixtures, so this proves the watcher's path, not a real agent.
func TestUnverifiedProviderNotifies(t *testing.T) {
	for _, c := range []struct {
		harness, fixture, want string
		start                  func(*testing.T, *Daemon, string, string) string
	}{
		{"codex", "report-idle-paginated.jsonl", "session_answered", codexSession},
		{"qwen", "report-idle.jsonl", "session_answered", qwenSession},
		{"opencode", "report-idle.sql", "session_answered", opencodeSession},
		{"copilot", "report-idle.jsonl", "session_answered", copilotSession},
		{"copilot", "report-permission.jsonl", "session_waiting", copilotSession},
		{"droid", "report-idle.jsonl", "session_answered", droidSession},
	} {
		t.Run(c.harness+"/"+c.want, func(t *testing.T) {
			d, _ := testDaemon(t)
			notes := &watchNotes{}
			d.notifier = notes
			id := c.start(t, d, c.fixture, t.TempDir())
			if v := viewOf(t, d, id); v.Unverified != (c.harness != "codex" && c.harness != "opencode") {
				t.Fatalf("view: %+v", v)
			}
			d.watchSessions(map[string]watchedSession{id: {turn: "busy", since: time.Unix(1, 0)}}, time.Now())
			if len(notes.kinds) != 1 || notes.kinds[0] != c.want {
				t.Fatalf("notifications: %v", notes.kinds)
			}
		})
	}
}

// A provider's row takes the process from a scan's row, and a herdr row
// with no session yet stays read only because its provider is unverified.
// herdr has no idle rule for Copilot or Droid, so TestHerdrNoIdleRuleNeverTypes
// holds them too.
func TestUnverifiedProviderAtHerdrLevel(t *testing.T) {
	for _, name := range []string{"codex", "opencode", "copilot", "droid"} {
		t.Run(name, func(t *testing.T) {
			d := uncheckedScanDaemon(t)
			f := startFakeHerdr(t, "idle")
			f.write(t, "agents.json", `{"id":"cli:agent:list","result":{"agents":[`+strings.ReplaceAll(herdrAgentJSON("idle", f.cwd), `"agent":"pi"`, `"agent":"`+name+`"`)+`],"type":"agent_list"}}`)
			scanNow(d)
			ss, err := d.sessionList()
			if err != nil || len(ss) != 1 {
				t.Fatalf("list: %+v %v", ss, err)
			}
			v := ss[0]
			if v.Harness != name || v.Level != harness.LevelHerdr || v.CanPrompt || !v.Unverified || !strings.Contains(v.Why, "unverified") {
				t.Fatalf("row %+v", v)
			}
			if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}); !api.IsCode(err, api.CodeRejected) || f.prompts(t) != "" {
				t.Fatalf("typed: err %v, prompts %q", err, f.prompts(t))
			}
		})
	}
}

// smif doctor: a format that differs is unchecked on an unverified provider
// and changed on a verified one, never a failure, and Claude Code's own check
// stays apart.
func TestDoctorUncheckedProviders(t *testing.T) {
	d, _ := testDaemon(t)
	h := d.health()
	if len(h.Providers) != 6 || h.Providers[0].Name != "claude" || !h.Providers[0].Verified || h.Providers[0].State != "none" {
		t.Fatalf("providers: %+v", h.Providers)
	}
	for _, p := range h.Providers[1:] {
		if verified := p.Name == "codex" || p.Name == "opencode"; p.Verified != verified || !verified && p.State != "unchecked" {
			t.Fatalf("no files: %+v", p)
		}
	}

	bad := t.TempDir()
	put(t, filepath.Join(bad, "sessions", "2026", "11", "05", "rollout-2026-11-05T10-00-00-x.jsonl"), `{"type":"a_new_format"}`+"\n")
	d.harnesses.Register(codex.NewWithProcs(bad, func() []codex.Proc { return nil }))
	h = d.health()
	var got api.ProviderCheck
	for _, p := range h.Providers {
		if p.Name == "codex" {
			got = p
		}
	}
	if got.State != "changed" || !strings.Contains(got.Detail, "no session_meta") {
		t.Fatalf("codex: %+v", got)
	}
	if h.Sessions != "none" || !h.OK {
		t.Fatalf("health: %s %v", h.Sessions, h.OK)
	}
}
