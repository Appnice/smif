//go:build darwin || linux

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

// SEND WHEN IDLE (ADR-0021): a busy session takes a prompt in a queue, and
// the running daemon types it once when the session reads idle. An agent
// reaches neither method, and a session that ended turns its items gone.
func TestSessionQueue(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	e.Start()
	report := func(session, event string, pid int) {
		t.Helper()
		e.Call("presence.update", api.PresenceParams{Event: event, Agent: "app", SessionID: session, PID: pid,
			TTY: "/dev/ttys999", Term: "Apple_Terminal", Title: "release"}, nil)
	}
	view := func(session string) api.SessionView {
		t.Helper()
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		for _, s := range ss {
			if s.SessionID == session {
				return s
			}
		}
		return api.SessionView{}
	}
	typedTexts := func() []string {
		b, _ := os.ReadFile(typed)
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r struct{ Text string }
			if json.Unmarshal([]byte(l), &r) == nil && r.Text != "" {
				out = append(out, r.Text)
			}
		}
		return out
	}
	stand := exec.Command("sleep", "120") // stands in for claude
	if err := stand.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stand.Process.Kill(); _ = stand.Wait() }()
	pid := stand.Process.Pid
	report("q-session-01", "busy", pid)

	// An agent token gets refused on both methods.
	ac := e.AgentClient("app")
	defer ac.Close()
	var x any
	if err := ac.Call("session.queue", api.SessionQueueParams{SessionID: "q-session-01", Text: "rm -rf /"}, &x, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an agent queued a prompt: %v", err)
	}
	if err := ac.Call("session.queue.cancel", api.SessionQueueCancelParams{ID: 1}, &x, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an agent cancelled an item: %v", err)
	}

	// The CLI queues on a busy session. Nothing is typed, and the list says so.
	out, err := e.CLI("", "prompt", "release", "--when-idle", "run", "the", "tests")
	if err != nil || !strings.Contains(out, "queued for app") {
		t.Fatalf("smif prompt --when-idle: %v %s", err, out)
	}
	var q2 api.SessionQueueResult
	e.Call("session.queue", api.SessionQueueParams{SessionID: "q-session-01", Text: "then the lint"}, &q2)
	if s := view("q-session-01"); len(s.Queue) != 2 || s.Queue[0].Text != "run the tests" {
		t.Fatalf("queue: %+v", s.Queue)
	}
	if out, _ := e.CLI("", "sessions"); !strings.Contains(out, "busy (2 queued)") {
		t.Fatalf("smif sessions: %s", out)
	}
	time.Sleep(2500 * time.Millisecond)
	if got := typedTexts(); len(got) != 0 {
		t.Fatalf("typed into a busy session: %q", got)
	}

	// It goes idle: the oldest item is typed once, and the session is busy
	// again, so the second one waits.
	report("q-session-01", "idle", pid)
	e.WaitFor("the first item typed", 10*time.Second, func() bool { return len(typedTexts()) >= 1 })
	time.Sleep(3500 * time.Millisecond)
	if got := typedTexts(); len(got) != 1 || got[0] != "run the tests" {
		t.Fatalf("typed %q", got)
	}
	if s := view("q-session-01"); s.Turn != "busy" || len(s.Queue) != 1 {
		t.Fatalf("after the first item: %+v", s)
	}

	// A second idle sends the second item.
	report("q-session-01", "idle", pid)
	e.WaitFor("the second item typed", 10*time.Second, func() bool { return len(typedTexts()) >= 2 })
	if got := typedTexts(); len(got) != 2 || got[1] != "then the lint" {
		t.Fatalf("typed %q", got)
	}

	// x: a cancelled item is never typed.
	var q3 api.SessionQueueResult
	e.Call("session.queue", api.SessionQueueParams{SessionID: "q-session-01", Text: "never mind"}, &q3)
	e.Call("session.queue.cancel", api.SessionQueueCancelParams{ID: q3.ID}, nil)

	// The session ends: its items turn gone, with a notification.
	e.Call("session.queue", api.SessionQueueParams{SessionID: "q-session-01", Text: "the last one"}, nil)
	if err := stand.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = stand.Wait()
	e.WaitFor("the gone notification", 15*time.Second, func() bool {
		for _, n := range e.Notifications() {
			if n["kind"] == "session_queue_gone" {
				return true
			}
		}
		return false
	})
	for _, n := range e.Notifications() {
		if n["kind"] == "session_queue_gone" && (n["agent"] != "release" || strings.Contains(n["summary"].(string), "the last one")) {
			t.Fatalf("notification: %v", n)
		}
	}
	counts := map[string]int{}
	var page api.AuditPage
	e.Call("audit.since", api.AuditParams{Limit: 1000}, &page)
	for _, ev := range page.Events {
		counts[ev.Event]++
	}
	for event, want := range map[string]int{"session.queue": 4, "session.queue.sent": 2, "session.queue.cancelled": 1,
		"session.queue.gone": 1, "session.prompt": 2} {
		if counts[event] != want {
			t.Errorf("%d %s rows, want %d (%v)", counts[event], event, want, counts)
		}
	}
	if got := typedTexts(); len(got) != 2 {
		t.Fatalf("typed %q after the end", got)
	}
}

// The real binary on a pseudo terminal: enter queues on a busy session,
// ctrl+q and x remove the item, ctrl+f asks a fork.
func TestSessionQueueKeys(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	e.Start()
	e.Call("presence.update", api.PresenceParams{Event: "busy", Agent: "app", SessionID: "q-keys-0001", PID: os.Getpid(),
		TTY: "/dev/ttys999", Term: "Apple_Terminal"}, nil)
	keys := strings.Join([]string{"\r", "go on\r", "\x11", "x", "why?\x06", "\x03"}, ptyPause)
	out, _, err := runInSilentPTY(t, e.env, keys, 1500*time.Millisecond, 30*time.Second, "ui")
	if err != nil {
		t.Fatalf("smif ui: %v\n%q", err, out)
	}
	counts := map[string]int{}
	var page api.AuditPage
	e.Call("audit.since", api.AuditParams{Limit: 1000}, &page)
	for _, ev := range page.Events {
		counts[ev.Event]++
	}
	if counts["session.queue"] != 1 || counts["session.queue.cancelled"] != 1 {
		t.Fatalf("audit: %v", counts)
	}
	e.WaitFor("the fork's worker", 10*time.Second, func() bool { return len(e.WorkerCalls()) > 0 })
	if b, _ := os.ReadFile(typed); len(b) != 0 {
		t.Fatalf("typed into a busy session: %s", b)
	}
}
