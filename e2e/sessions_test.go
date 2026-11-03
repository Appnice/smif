package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
)

// SESSIONS (ADR-0012): the hooks report idle and busy turns, the human
// lists sessions and types a prompt into an idle one; agents can do
// neither, and cannot forge where a session runs. The daemon types through
// daemon.terminal = "log:<file>" here instead of Terminal.app.
func TestSessionPrompt(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()}, AgentSpec{ID: "web", Rules: digestRule()})
	e.Start()
	pid := os.Getpid() // a live process stands in for claude
	report := func(session, event, term string, pid int) {
		t.Helper()
		e.Call("presence.update", api.PresenceParams{Event: event, Agent: "app", SessionID: session, PID: pid,
			TTY: "/dev/ttys999", Term: term}, nil)
	}
	list := func() map[string]api.SessionView {
		t.Helper()
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		out := map[string]api.SessionView{}
		for _, s := range ss {
			out[s.SessionID] = s
		}
		return out
	}

	// The hook maps Stop to idle and reads the session's name and last
	// prompt from its transcript. (Its terminal depends on where the test
	// runs: under a claude session it finds that one.)
	tr := filepath.Join(t.TempDir(), "hook-session-1.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"custom-title","customTitle":"web-rfc"}`+"\n"+
		`{"type":"last-prompt","lastPrompt":"fix the\nlogin page"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := `{"session_id":"hook-session-1","cwd":"` + e.Repos["web"] + `","transcript_path":"` + tr + `"}`
	if _, stderr, err := e.CLIDir(e.Repos["web"], in, nil, "hook", "Stop"); err != nil {
		t.Fatalf("hook: %v %s", err, stderr)
	}
	if s := list()["hook-session-1"]; s.Turn != "idle" || s.Agent != "web" || s.Title != "web-rfc" || s.LastPrompt != "fix the login page" {
		t.Fatalf("hook session: %+v", s)
	}
	// UserPromptSubmit carries the new prompt itself.
	in = `{"session_id":"hook-session-1","cwd":"` + e.Repos["web"] + `","transcript_path":"` + tr + `","prompt":"now the signup page"}`
	if _, stderr, err := e.CLIDir(e.Repos["web"], in, nil, "hook", "UserPromptSubmit"); err != nil {
		t.Fatalf("hook: %v %s", err, stderr)
	}
	if s := list()["hook-session-1"]; s.Turn != "busy" || s.LastPrompt != "now the signup page" || s.Title != "web-rfc" {
		t.Fatalf("hook session after a prompt: %+v", s)
	}

	report("s-idle-0001", "start", "Apple_Terminal", pid)
	report("s-iterm-001", "idle", "WezTerm", pid)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	report("s-dead-0001", "idle", "Apple_Terminal", dead.Process.Pid)
	ss := list()
	if s := ss["s-idle-0001"]; s.Turn != "idle" || !s.CanPrompt || s.TTY != "/dev/ttys999" {
		t.Fatalf("idle session: %+v", s)
	}
	if s := ss["s-iterm-001"]; s.CanPrompt || !strings.Contains(s.Why, "Terminal.app, iTerm2 and tmux") {
		t.Fatalf("iTerm session: %+v", s)
	}
	if _, ok := ss["s-dead-0001"]; ok {
		t.Fatalf("a session whose process is gone is listed: %+v", ss["s-dead-0001"])
	}

	// Agents cannot list, prompt, or say where a session runs.
	ac := e.AgentClient("web")
	defer ac.Close()
	var x any
	if err := ac.Call("session.list", nil, &x, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent listed sessions: %v", err)
	}
	if err := ac.Call("session.prompt", api.SessionPromptParams{Agent: "app", Text: "rm -rf /"}, &x, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent typed a prompt: %v", err)
	}
	if err := ac.Call("presence.update", api.PresenceParams{Event: "idle", Agent: "web", SessionID: "s-forged-01", PID: pid,
		TTY: "/dev/ttys998", Term: "Apple_Terminal", Title: "trust me"}, &x, 5e9); err != nil {
		t.Fatal(err)
	}
	if s := list()["s-forged-01"]; s.CanPrompt || s.TTY != "" || s.Turn == "idle" || s.Title != "" {
		t.Errorf("an agent set a session's terminal or turn: %+v", s)
	}

	// Bad text is refused before anything is typed.
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, bad := range []string{"", "two\nlines", "esc \x1b[2J"} {
		if err := c.Call("session.prompt", api.SessionPromptParams{SessionID: "s-idle-0001", Text: bad}, &x, 5e9); !api.IsCode(err, api.CodeParams) {
			t.Errorf("prompt %q: %v", bad, err)
		}
	}

	// The CLI types into the agent's one idle session; it is busy after.
	out, err := e.CLI("", "prompt", "app", "run", "the", "tests")
	if err != nil || !strings.Contains(out, "sent to app (session s-idle-0") {
		t.Fatalf("mailroom prompt: %v %s", err, out)
	}
	b, _ := os.ReadFile(typed)
	var got struct{ SessionID, TTY, Text string }
	if err := json.Unmarshal(b, &got); err != nil || got.Text != "run the tests" || got.TTY != "/dev/ttys999" {
		t.Fatalf("typed %q (%v)", b, err)
	}
	if s := list()["s-idle-0001"]; s.Turn != "busy" || s.CanPrompt {
		t.Fatalf("after the prompt: %+v", s)
	}
	err = c.Call("session.prompt", api.SessionPromptParams{SessionID: "s-idle-0001", Text: "again"}, &x, 5e9)
	if !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "a prompt is running") {
		t.Fatalf("prompt into a busy session: %v", err)
	}

	// The Stop hook makes it idle again; the CLI lists it.
	report("s-idle-0001", "idle", "", 0)
	out, err = e.CLI("", "sessions")
	if err != nil || !strings.Contains(out, "s-idle-0") || !strings.Contains(out, "idle") {
		t.Fatalf("mailroom sessions: %v\n%s", err, out)
	}
	if err := c.Call("session.prompt", api.SessionPromptParams{Agent: "app", Text: "now the lint"}, &x, 5e9); err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	var evs api.AuditPage
	e.Call("audit.since", api.AuditParams{Limit: 1000}, &evs)
	n := 0
	for _, ev := range evs.Events {
		if ev.Event == "session.prompt" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d session.prompt audit rows, want 2", n)
	}

	// session.focus brings its tab to the front; agents cannot.
	if err := c.Call("session.focus", api.SessionFocusParams{SessionID: "s-idle-0001"}, &x, 5e9); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if b, _ := os.ReadFile(typed); !strings.Contains(string(b), `"focus":true`) {
		t.Fatalf("focus not logged: %s", b)
	}
	if err := ac.Call("session.focus", api.SessionFocusParams{SessionID: "s-idle-0001"}, &x, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent focused a session: %v", err)
	}

	// A session is picked by its name, in any case.
	e.Call("presence.update", api.PresenceParams{Event: "idle", Agent: "app", SessionID: "s-idle-0001", Title: "Release Train"}, nil)
	if out, err := e.CLI("", "prompt", "release train", "ship", "it"); err != nil || !strings.Contains(out, "session s-idle-0") {
		t.Fatalf("prompt by name: %v %s", err, out)
	}
	b, _ = os.ReadFile(typed)
	if !strings.HasSuffix(strings.TrimSpace(string(b)), `"text":"ship it","tty":"/dev/ttys999"}`) {
		t.Fatalf("typed by name: %s", b)
	}
	if _, err := e.CLI("", "prompt", "no-such-name", "x"); err == nil {
		t.Fatal("a prompt to an unknown name was sent")
	}
}

// Discovery (ADR-0012): sessions that Claude Code lists in
// $CLAUDE_CONFIG_DIR/sessions show up with their name and status before
// any hook ran; background sessions and dead processes do not.
func TestSessionDiscovery(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.Start()
	dir := filepath.Join(e.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	write := func(pid int, id, kind, status string, at int64) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "cwd": e.Repos["app"], "kind": kind,
			"name": "named-" + id, "status": status, "startedAt": now - 60000, "statusUpdatedAt": at})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d-%s.json", pid, id)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(os.Getpid(), "disc-1", "interactive", "idle", now-5000)
	write(os.Getpid(), "disc-bg", "bg", "idle", now)
	write(dead.Process.Pid, "disc-dead", "interactive", "idle", now)
	list := func() map[string]api.SessionView {
		t.Helper()
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		out := map[string]api.SessionView{}
		for _, s := range ss {
			out[s.SessionID] = s
		}
		return out
	}
	ss := list()
	if s, ok := ss["disc-1"]; !ok || s.Agent != "app" || s.Title != "named-disc-1" || s.Turn != "idle" {
		t.Fatalf("discovered session: %+v (all %+v)", s, ss)
	}
	// A session in a folder no agent owns still shows, without an agent.
	other := t.TempDir()
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "disc-other", "cwd": other, "kind": "interactive",
		"name": "digi", "status": "idle", "startedAt": now, "statusUpdatedAt": now})
	if err := os.WriteFile(filepath.Join(dir, "other.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, ok := list()["disc-other"]; !ok || s.Agent != "" || s.Title != "digi" || s.Repo != filepath.Clean(other) {
		t.Fatalf("session outside the agents' repos: %+v %v", s, ok)
	}
	for _, id := range []string{"disc-bg", "disc-dead"} {
		if _, ok := ss[id]; ok {
			t.Errorf("%s is listed", id)
		}
	}
	// A newer hook turn wins over an older file status; a newer file
	// status wins over an older hook turn.
	e.Call("presence.update", api.PresenceParams{Event: "busy", Agent: "app", SessionID: "disc-1", LastPrompt: "go on"}, nil)
	if s := list()["disc-1"]; s.Turn != "busy" || s.LastPrompt != "go on" || s.Title != "named-disc-1" {
		t.Fatalf("after the hook: %+v", s)
	}
	write(os.Getpid(), "disc-1", "interactive", "idle", time.Now().Add(time.Second).UnixMilli())
	if s := list()["disc-1"]; s.Turn != "idle" {
		t.Fatalf("after the file: %+v", s)
	}

	// session.tail reads the conversation from the transcript, for the
	// human only: prompts and answers, tool calls counted, system text and
	// subagent records left out.
	pdir := filepath.Join(e.Home, ".claude", "projects", "-r-app")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"role":"user","content":"is 1.0.2 live?"}}`,
		`{"type":"user","isMeta":true,"timestamp":"2026-11-03T08:00:00Z","message":{"role":"user","content":"meta"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"text","text":"Checking."},{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"user","timestamp":"2026-11-03T08:00:02Z","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-03T08:00:03Z","message":{"content":[{"type":"text","text":"subagent"}]}}`,
		`{"type":"user","timestamp":"2026-11-03T08:00:03Z","message":{"content":[{"type":"text","text":"<system-reminder>x</system-reminder>"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:04Z","message":{"content":[{"type":"tool_use","name":"Read"},{"type":"text","text":"Yes, it is live."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(pdir, "tail-session-01.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	var tail api.SessionTail
	e.Call("session.tail", api.SessionTailParams{SessionID: "tail-session-01"}, &tail)
	if len(tail.Turns) != 2 || tail.Turns[0].Role != "you" || tail.Turns[0].Text != "is 1.0.2 live?" ||
		tail.Turns[1].Role != "claude" || tail.Turns[1].Text != "Yes, it is live." || tail.Turns[1].Tools != 2 {
		t.Fatalf("tail: %+v", tail)
	}
	ac := e.AgentClient("app")
	defer ac.Close()
	var x any
	if err := ac.Call("session.tail", api.SessionTailParams{SessionID: "tail-session-01"}, &x, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent read a session's conversation: %v", err)
	}
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("session.tail", api.SessionTailParams{SessionID: "../../x"}, &x, 5e9); !api.IsCode(err, api.CodeParams) {
		t.Errorf("a bad session id: %v", err)
	}
	// session.list carries the final message of the last answer.
	write(os.Getpid(), "tail-session-01", "interactive", "idle", now)
	if s := list()["tail-session-01"]; s.LastAnswer != "Yes, it is live." || s.LastAnswerAt.IsZero() {
		t.Fatalf("last answer: %+v", s)
	}
	// ... and what the newest exchange does now: the prompt, the steps, the
	// call without a result (paths in the session's folder relative to it).
	more := strings.Join([]string{
		`{"type":"user","timestamp":"2026-11-03T09:00:00Z","message":{"content":"push the fix"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:00:01Z","message":{"content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"` + e.Repos["app"] + `/cart/cart.go"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"e1","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:00:03Z","message":{"content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"git push origin main"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(pdir, "tail-session-01.jsonl"), []byte(transcript+more), 0o600); err != nil {
		t.Fatal(err)
	}
	write(os.Getpid(), "tail-session-01", "interactive", "waiting", time.Now().UnixMilli())
	s := list()["tail-session-01"]
	if a := s.Activity; s.Turn != "waiting" || a == nil || a.Prompt != "push the fix" || a.Tools != 2 || len(a.Steps) != 2 ||
		a.Steps[0].Target != "cart/cart.go" || !a.Steps[0].Done || a.Now == nil || a.Now.Tool != "Bash" ||
		a.Now.Target != "git push origin main" || a.WroteAt.IsZero() {
		t.Fatalf("activity: %+v %+v", s, s.Activity)
	}
}
