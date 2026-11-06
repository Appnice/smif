//go:build darwin || linux

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

// BACKGROUND SESSIONS (ADR-0021): the running daemon reads `claude agents
// --json --all` from the fake. Blocked reads waiting, working busy, done
// idle, and a word it does not know unknown. A failing command gives no
// rows and no error. Nothing types into such a row, and attach opens a tab
// with an id from the list.
func TestBackgroundSessions(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`+"\n"+`session_notify = "waiting,answered"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	repo := e.Repos["app"]
	const (
		blocked = "aaaaaaaa-0000-4000-8000-000000000001"
		working = "bbbbbbbb-0000-4000-8000-000000000002"
		done    = "cccccccc-0000-4000-8000-000000000003"
		oldDone = "dddddddd-0000-4000-8000-000000000004"
		strange = "eeeeeeee-0000-4000-8000-000000000005"
	)
	list := filepath.Join(e.Root, "fake", "agents.json")
	row := func(id, name, state string, started time.Time) string {
		return fmt.Sprintf(`{"id":%q,"cwd":%q,"kind":"background","startedAt":%d,"sessionId":%q,"name":%q,"state":%q}`,
			id[:8], repo, started.UnixMilli(), id, name, state)
	}
	write := func(rows ...string) {
		t.Helper()
		tmp := list + ".tmp"
		if err := os.WriteFile(tmp, []byte("["+strings.Join(rows, ",")+"]"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, list); err != nil {
			t.Fatal(err)
		}
	}
	// A transcript written now, with an answer of the given time: done and
	// oldDone must go by the answer and not by the file's time.
	transcript := func(id, answer string, at time.Time) {
		t.Helper()
		dir := filepath.Join(e.Home, ".claude", "projects", "-repo")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		stamp := at.UTC().Format(time.RFC3339)
		body := `{"type":"user","timestamp":"` + stamp + `","message":{"role":"user","content":"run the nightly"}}` + "\n" +
			`{"type":"assistant","timestamp":"` + stamp + `","message":{"role":"assistant","content":[{"type":"text","text":"` + answer + `"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	transcript(done, "the nightly passed", now.Add(-2*time.Hour))
	transcript(oldDone, "the old nightly passed", now.Add(-30*time.Hour))
	write(row(blocked, "blocked one", "working", now), row(working, "working one", "working", now),
		row(done, "done one", "done", now.Add(-3*time.Hour)), row(oldDone, "old one", "done", now.Add(-31*time.Hour)),
		row(strange, "strange one", "hibernating", now))
	e.Start()

	views := func() map[string]api.SessionView {
		t.Helper()
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		out := map[string]api.SessionView{}
		for _, s := range ss {
			out[s.SessionID] = s
		}
		return out
	}
	waitFor := func(what string, ok func(map[string]api.SessionView) bool) map[string]api.SessionView {
		t.Helper()
		end := time.Now().Add(15 * time.Second)
		for {
			v := views()
			if ok(v) {
				return v
			}
			if time.Now().After(end) {
				t.Fatalf("%s: %+v", what, v)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	v := waitFor("the rows", func(v map[string]api.SessionView) bool { return len(v) >= 4 })
	if _, ok := v[oldDone]; ok {
		t.Errorf("a done row from 30 h ago shows: %+v", v[oldDone])
	}
	for id, want := range map[string]string{working: "busy", done: "idle", strange: "unknown", blocked: "busy"} {
		s := v[id]
		if s.Turn != want || !s.Background || s.CanPrompt || s.TTY != "" || !strings.Contains(s.Why, "no tab") {
			t.Errorf("%s: %+v", id[:8], s)
		}
	}
	if s := v[done]; s.LastAnswer != "the nightly passed" || s.Harness != "claude" || s.Agent != "app" || s.Title != "done one" {
		t.Errorf("the transcript route: %+v", s)
	}
	var tail api.SessionTail
	e.Call("session.tail", api.SessionTailParams{SessionID: done}, &tail)
	if len(tail.Turns) != 2 || tail.Turns[1].Text != "the nightly passed" {
		t.Errorf("session.tail: %+v", tail.Turns)
	}
	if out, _ := e.CLI("", "sessions"); !strings.Contains(out, "full background") || !strings.Contains(out, "no: a background session has no tab") {
		t.Errorf("smif sessions:\n%s", out)
	}

	// blocked reads waiting, and the human hears of it like from any session.
	write(row(blocked, "blocked one", "blocked", now), row(working, "working one", "working", now),
		row(done, "done one", "done", now.Add(-3*time.Hour)), row(strange, "strange one", "hibernating", now))
	waitFor("blocked as waiting", func(v map[string]api.SessionView) bool { return v[blocked].Turn == "waiting" })
	end := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(e.Root, "notify.jsonl"))
		if strings.Contains(string(b), "session_waiting") && strings.Contains(string(b), "blocked one") {
			if strings.Contains(string(b), "in its tab") {
				t.Errorf("the notification names a tab: %s", b)
			}
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no notification: %s", b)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// No typing, no queue, no fork: each says why, and nothing is typed.
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, id := range []string{blocked, working, done, strange} {
		var x any
		for name, call := range map[string]func() error{
			"session.prompt": func() error {
				return c.Call("session.prompt", api.SessionPromptParams{SessionID: id, Text: "go on"}, &x, 5*time.Second)
			},
			"session.queue": func() error {
				return c.Call("session.queue", api.SessionQueueParams{SessionID: id, Text: "go on"}, &x, 5*time.Second)
			},
			"session.send": func() error {
				return c.Call("session.send", api.SessionSendParams{SessionID: id, Text: "go on"}, &x, 5*time.Second)
			},
		} {
			if err := call(); !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "background session has no tab") {
				t.Errorf("%s on %s: %v", name, id[:8], err)
			}
		}
	}
	if _, err := os.Stat(typed); !os.IsNotExist(err) {
		b, _ := os.ReadFile(typed)
		t.Fatalf("something reached the terminal: %s", b)
	}

	// attach: human only, an id on the list, argv from the config and the list.
	ac := e.AgentClient("app")
	defer ac.Close()
	var x any
	if err := ac.Call("session.attach", api.SessionAttachParams{SessionID: blocked}, &x, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an agent attached: %v", err)
	}
	for _, id := range []string{oldDone, "ffffffff-0000-4000-8000-000000000006", "x; id"} {
		if err := c.Call("session.attach", api.SessionAttachParams{SessionID: id}, &x, 5*time.Second); !api.IsCode(err, api.CodeNotFound) {
			t.Errorf("attach %q: %v", id, err)
		}
	}
	if _, err := os.Stat(typed); !os.IsNotExist(err) {
		t.Fatal("a refused attach reached the terminal")
	}
	e.Call("session.attach", api.SessionAttachParams{SessionID: blocked, Term: "tmux"}, nil)
	opened, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	var open struct {
		Open bool
		Dir  string
		Term string
		Argv []string
	}
	if err := json.Unmarshal(opened, &open); err != nil {
		t.Fatal(err)
	}
	if !open.Open || open.Dir != repo || open.Term != "tmux" || len(open.Argv) != 3 || open.Argv[1] != "attach" || open.Argv[2] != blocked[:8] || filepath.Base(open.Argv[0]) != "claude" {
		t.Fatalf("attach opened %+v", open)
	}

	// session.list keeps its speed under a slow command, and the command
	// never runs twice in 5 s, however often the list asks.
	if err := os.WriteFile(filepath.Join(e.Root, "fake", "agents.sleep"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(e.Root, "fake", "agents.calls")
	var slowest time.Duration
	for end := time.Now().Add(11 * time.Second); time.Now().Before(end); {
		var ss []api.SessionView
		start := time.Now()
		e.Call("session.list", nil, &ss)
		slowest = max(slowest, time.Since(start))
		time.Sleep(20 * time.Millisecond)
	}
	if slowest > 1500*time.Millisecond { // the race build; the fake takes 3 s
		t.Errorf("the slowest session.list took %v", slowest)
	}
	t.Logf("slowest session.list under a 3 s command: %v", slowest)
	raw, _ := os.ReadFile(calls)
	var at []int64
	for _, l := range strings.Fields(string(raw)) {
		n, _ := strconv.ParseInt(l, 10, 64)
		at = append(at, n)
	}
	if len(at) < 3 {
		t.Fatalf("the command ran %d times", len(at))
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i] - at[i-1]; gap < 4900 {
			t.Errorf("two runs %d ms apart: %v", gap, at)
		}
	}
	t.Logf("the command ran %d times, the smallest gap holds 5 s", len(at))
	os.Remove(filepath.Join(e.Root, "fake", "agents.sleep"))

	// A failing command gives no rows and no error. The list stays whole
	// for every other session, and the rows come back after it.
	if err := os.WriteFile(filepath.Join(e.Root, "fake", "agents.fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor("no rows from a failing command", func(v map[string]api.SessionView) bool { return len(v) == 0 })
	if out, err := e.CLI("", "sessions"); err != nil || !strings.Contains(out, "No live sessions") {
		t.Errorf("smif sessions: %v %s", err, out)
	}
	var h api.HealthView
	e.Call("admin.health", nil, &h)
	if len(h.Background) != 1 || h.Background[0].State != "none" || !h.OK {
		t.Errorf("health: %+v", h.Background)
	}
	if out, _ := e.CLI("", "doctor"); !strings.Contains(out, "[info] no background sessions") || strings.Contains(out, "FAIL] background") {
		t.Errorf("doctor:\n%s", out)
	}
	os.Remove(filepath.Join(e.Root, "fake", "agents.fail"))
	if err := os.WriteFile(list, []byte("no json here"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	if v := views(); len(v) != 0 {
		t.Errorf("rows from bad output: %+v", v)
	}
	e.Call("admin.health", nil, &h)
	if h.Background[0].State != "changed" {
		t.Errorf("health: %+v", h.Background)
	}
	write(row(working, "working one", "working", now))
	waitFor("the rows come back", func(v map[string]api.SessionView) bool { return v[working].Turn == "busy" })
	e.Call("admin.health", nil, &h)
	if out, _ := e.CLI("", "doctor"); !strings.Contains(out, "[ok  ] background sessions: 1 background sessions") {
		t.Errorf("doctor:\n%s", out)
	}
}
