package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

func TestConversationFinalMessage(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		t.Helper()
		p := filepath.Join(dir, "c.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := strings.Repeat("The release is live in both stores. ", 6)
	// Steps between tool calls are dropped; the text after the last one is the answer.
	p := write(
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"live?"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"text","text":"Checking the stores."},{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:05Z","message":{"content":[{"type":"text","text":"`+long+`"}]}}`,
	)
	if ts, _ := readTranscript(p, 5); len(ts) != 2 || ts[1].Text != strings.TrimSpace(long) || ts[1].Tools != 1 {
		t.Fatalf("final message: %+v", ts)
	}
	// Answered, then saved a note and ended short: both show, the long one first.
	p = write(
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"live?"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:05Z","message":{"content":[{"type":"text","text":"`+long+`"},{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:06Z","message":{"content":[{"type":"text","text":"Saved."}]}}`,
	)
	if ts, _ := readTranscript(p, 5); len(ts) != 2 || ts[1].Text != strings.TrimSpace(long)+"\n\nSaved." {
		t.Fatalf("short final message: %q", ts[1].Text)
	}
}

// The newest exchange's activity: the prompt, each tool call with its
// target, which ones are done, and the pending one (what runs now, or
// what a waiting session asks for in its tab).
func TestReadActivity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"old prompt"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"tool_use","id":"t0","name":"Read","input":{"file_path":"/x/old.md"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:00:00Z","message":{"content":"push the fix"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:00:02Z","message":{"content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make check\nmake e2e","description":"run checks"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:01:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:01:02Z","message":{"content":[{"type":"tool_use","id":"t2","name":"mcp__claude_ai_Slack__slack_send_message","input":{"text":"hi"}},{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"git push origin main"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:01:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"sent"}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-03T09:01:04Z","message":{"content":[{"type":"tool_use","id":"s1","name":"Grep","input":{"pattern":"x"}}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, act := readTranscript(p, 10)
	if len(turns) != 4 || act.Prompt != "push the fix" || !act.PromptAt.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("turns %d, activity %+v", len(turns), act)
	}
	if act.Tools != 3 || len(act.Steps) != 3 {
		t.Fatalf("steps: %+v", act.Steps)
	}
	s := act.Steps
	if s[0].Tool != "Bash" || s[0].Target != "make check …" || !s[0].Done ||
		s[1].Tool != "slack_send_message" || s[1].Target != "" || !s[1].Done ||
		s[2].Target != "git push origin main" || s[2].Done {
		t.Fatalf("steps: %+v", s)
	}
	if act.Pending == nil || act.Pending.Target != "git push origin main" {
		t.Fatalf("pending: %+v", act.Pending)
	}

	// A question in its tab: the pending step carries the question.
	q := `{"type":"assistant","timestamp":"2026-11-03T09:02:00Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"question":"Which store first?","options":[]}]}}]}}`
	if err := os.WriteFile(p, []byte(lines[2]+"\n"+q+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, act := readTranscript(p, 10); act.Pending == nil || act.Pending.Tool != "AskUserQuestion" || act.Pending.Target != "Which store first?" {
		t.Fatalf("question: %+v", act.Pending)
	}
}

func TestFileTurnShellSaysNothing(t *testing.T) {
	for in, want := range map[string]string{"shell": "", "busy": "busy", "idle": "idle", "waiting": "waiting"} {
		if got := fileTurn(in); got != want {
			t.Errorf("fileTurn(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeSession writes one session file, as Claude Code does.
func writeSession(t *testing.T, dir, file, id, kind, status string, pid int, started int64) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "cwd": "/repo/sub", "kind": kind, "name": "rfc",
		"status": status, "startedAt": started, "statusUpdatedAt": started + 5})
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", file), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Discover keeps live interactive sessions; of two processes with one
// session id, the newer wins.
func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	me := os.Getpid()
	writeSession(t, dir, "1.json", "sess-live-0001", "interactive", "idle", me, 2000)
	writeSession(t, dir, "2.json", "sess-live-0001", "interactive", "busy", me, 1000)
	writeSession(t, dir, "3.json", "sess-bg-000001", "bg", "busy", me, 1000)
	writeSession(t, dir, "4.json", "sess-dead-0001", "interactive", "idle", 1<<22+7, 1000)
	p := New(dir)
	ss := p.Discover()
	if len(ss) != 1 || ss[0].SessionID != "sess-live-0001" || ss[0].Status != "idle" || ss[0].PID != me || ss[0].Name != "rfc" {
		t.Fatalf("discover: %+v", ss)
	}
	if turn, at := p.State(ss[0]); turn != "idle" || !at.Equal(time.UnixMilli(2005)) {
		t.Fatalf("state: %q %v", turn, at)
	}
	if len(New("").Discover()) != 0 {
		t.Fatal("no config folder should discover nothing")
	}
}

// Read finds a transcript by session id, the stamp follows the file, and a
// transcript that shows up later is read at once by Read.
func TestReadAndStamp(t *testing.T) {
	dir := t.TempDir()
	p := New(dir)
	const id = "sess-read-0001"
	if r := p.Read(id, 4); r.OK {
		t.Fatalf("no transcript yet: %+v", r)
	}
	if p.Stamp(id) != "" {
		t.Fatal("stamp without a transcript")
	}
	f := filepath.Join(dir, "projects", "-repo", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"live?"}}` + "\n"
	if err := os.WriteFile(f, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	r := p.Read(id, 4)
	if !r.OK || len(r.Turns) != 1 || r.Turns[0].Role != harness.RoleHuman || r.Activity.Prompt != "live?" || r.WroteAt.IsZero() {
		t.Fatalf("read: %+v", r)
	}
	if p.Stamp(id) != "" {
		t.Fatal("a missing transcript is looked for at most every 30 s")
	}
	p.paths = map[string]pathEntry{}
	one := p.Stamp(id)
	if err := os.WriteFile(f, []byte(line+line), 0o600); err != nil {
		t.Fatal(err)
	}
	if two := p.Stamp(id); one == "" || one == two {
		t.Fatalf("stamps %q %q", one, two)
	}
	if p.Read("../../etc/passwd", 4).OK {
		t.Fatal("a bad id must read nothing")
	}
}

func TestInfoAndRoutes(t *testing.T) {
	p := New(t.TempDir())
	if i := p.Info(); i.Name != "claude" || i.Process != "claude" || !i.Verified {
		t.Fatalf("info: %+v", i)
	}
	if harness.ReadOnly("claude", p) != "" {
		t.Fatal("Claude Code must stay writable")
	}
	if p.ForkDir("/repo/sub", "/repo") != "/repo/sub" || p.ForkDir("", "/repo") != "/repo" {
		t.Fatal("fork folder")
	}
	if len(p.HookEvents()) != 4 {
		t.Fatalf("hooks: %q", p.HookEvents())
	}
}

// Check reads the files like the session view, so a format change shows.
func TestCheck(t *testing.T) {
	dir := t.TempDir()
	p := New(dir)
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("no files: %s", st)
	}
	writeSession(t, dir, "1.json", "sess-chk-00001", "interactive", "", os.Getpid(), 1)
	if st, d := p.Check(); st != "changed" || !strings.Contains(d, "no status") {
		t.Fatalf("no status: %s %s", st, d)
	}
	writeSession(t, dir, "1.json", "sess-chk-00001", "interactive", "idle", os.Getpid(), 1)
	if st, d := p.Check(); st != "ok" || !strings.Contains(d, "1 live sessions") {
		t.Fatalf("ok: %s %s", st, d)
	}
}
