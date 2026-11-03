package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

// SESSION ASKS (ADR-0013): the human writes to a session. Idle: the text is
// typed in. Busy: a worker resumes a fork of the session in its folder and
// answers; nothing is typed. An agent's ask to a session's name goes the
// same way. Agents cannot call session.send, and a session in a folder no
// agent owns has no worker.
func TestSessionAsk(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()}, AgentSpec{ID: "web", Rules: digestRule()})
	e.Start()
	dir := filepath.Join(e.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := func(file, id, cwd, name, status string) {
		t.Helper()
		now := time.Now().UnixMilli()
		b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": id, "cwd": cwd, "kind": "interactive",
			"name": name, "status": status, "startedAt": now - 1000, "statusUpdatedAt": now})
		if err := os.WriteFile(filepath.Join(dir, file), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(e.Repos["app"], "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	session("a.json", "sess-app-0001", sub, "app-rfc", "busy")
	session("o.json", "sess-other-01", t.TempDir(), "loose", "busy")

	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Busy: a worker answers from a fork of the session, in its folder.
	var res api.SessionSendResult
	if err := c.Call("session.send", api.SessionSendParams{Name: "APP-RFC", Text: "what changed today? #fake:reply=two-commits"}, &res, 10e9); err != nil {
		t.Fatalf("send to a busy session: %v", err)
	}
	if res.Mode != "worker" || res.Agent != "app" || res.MessageID == "" {
		t.Fatalf("send result: %+v", res)
	}
	e.WaitState(res.MessageID, 30*time.Second, "done")
	var fork *Invocation
	for _, inv := range e.WorkerCalls() {
		if i := slices.Index(inv.Argv, "--resume"); i > 0 && inv.Argv[i+1] == "sess-app-0001" {
			fork = &inv
		}
	}
	if fork == nil || !slices.Contains(fork.Argv, "--fork-session") {
		t.Fatalf("no forked worker run: %+v", e.WorkerCalls())
	}
	if real, _ := filepath.EvalSymlinks(sub); fork.Cwd != sub && fork.Cwd != real {
		t.Errorf("fork ran in %s, want the session's folder %s", fork.Cwd, sub)
	}
	if fork.SessionID == "sess-app-0001" {
		t.Error("the fork kept the session's id")
	}
	if b, _ := os.ReadFile(typed); len(b) > 0 {
		t.Fatalf("text was typed into a busy session: %s", b)
	}
	var tail api.SessionTail
	e.Call("session.tail", api.SessionTailParams{SessionID: "sess-app-0001"}, &tail)
	n := len(tail.Turns)
	if n < 2 || tail.Turns[n-2].Role != "ask" || tail.Turns[n-2].From != "you" || !strings.HasPrefix(tail.Turns[n-2].Text, "what changed today?") ||
		tail.Turns[n-1].Role != "worker" || tail.Turns[n-1].Text != "two-commits" {
		t.Fatalf("tail: %+v", tail.Turns)
	}

	// Idle: the text is typed into the session.
	session("a.json", "sess-app-0001", sub, "app-rfc", "idle")
	e.Call("presence.update", api.PresenceParams{Event: "idle", Agent: "app", SessionID: "sess-app-0001", PID: os.Getpid(),
		TTY: "/dev/ttys999", Term: "Apple_Terminal"}, nil)
	if err := c.Call("session.send", api.SessionSendParams{SessionID: "sess-app-0001", Text: "now run the tests"}, &res, 10e9); err != nil {
		t.Fatalf("send to an idle session: %v", err)
	}
	if b, _ := os.ReadFile(typed); res.Mode != "typed" || !strings.Contains(string(b), `"text":"now run the tests"`) {
		t.Fatalf("idle send: %+v %s", res, b)
	}

	// An agent's ask to a session's name: a fork answers, nothing is typed.
	before := len(e.WorkerCalls())
	r := e.Send("web", "message.ask", envelope.Draft{To: "app-rfc", Kind: "ask", Intent: "q", Subject: "status? #fake:reply=fine"})
	e.WaitState(r.ID, 30*time.Second, "done")
	calls := e.WorkerCalls()
	if len(calls) != before+1 || !slices.Contains(calls[len(calls)-1].Argv, "--fork-session") {
		t.Fatalf("agent ask to a session name did not fork: %+v", calls[before:])
	}
	if b, _ := os.ReadFile(typed); strings.Contains(string(b), "status?") {
		t.Fatalf("an agent's ask was typed into a session: %s", b)
	}

	// Agents cannot send; a session no agent owns has no worker.
	ac := e.AgentClient("web")
	defer ac.Close()
	var x any
	if err := ac.Call("session.send", api.SessionSendParams{Name: "app-rfc", Text: "hi"}, &x, 5e9); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an agent used session.send: %v", err)
	}
	if err := c.Call("session.send", api.SessionSendParams{Name: "loose", Text: "hi"}, &x, 5e9); !api.IsCode(err, api.CodeRejected) ||
		!strings.Contains(err.Error(), "no agent owns") {
		t.Errorf("busy session without an agent: %v", err)
	}

	// mailroom stats counts the cockpit; doctor's canary reads the files.
	var st api.StatsResult
	e.Call("stats.get", api.StatsParams{}, &st)
	if st.Sessions.Typed != 1 || st.Sessions.WorkerAsks != 1 || st.Sessions.AgentAsks != 1 {
		t.Errorf("session stats: %+v", st.Sessions)
	}
	var h api.HealthView
	e.Call("admin.health", nil, &h)
	if h.Sessions != "ok" {
		t.Errorf("canary: %s %s", h.Sessions, h.SessionsDetail)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"pid":1,"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"o.json"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	e.Call("admin.health", nil, &h)
	if h.Sessions != "changed" {
		t.Errorf("canary on a changed file: %s %s", h.Sessions, h.SessionsDetail)
	}
}
