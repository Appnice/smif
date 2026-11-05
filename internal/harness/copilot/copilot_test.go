package copilot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The event logs in testdata are built from the research report (Copilot's
// event schema at 1.0.60), not captured from a session here: see
// testdata/README.md. A green test here proves only this parser.

const sessionID = "4de47255-0000-4000-8000-000000000001"

// place makes a session folder with a fixture and a lock of this process.
func place(t *testing.T, home, fixture, id, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "session-state", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "CWD", cwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lock(t *testing.T, home, id string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "session-state", id, fmt.Sprintf("inuse.%d.lock", pid)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// isCopilot says any pid is a copilot on a terminal that started an hour ago.
func isCopilot(int) (harness.Proc, bool) {
	return harness.Proc{TTY: "/dev/ttys900", Argv: []string{"copilot"}, Start: time.Now().Add(-time.Hour)}, true
}

func TestUnverifiedAndNoWriteRoute(t *testing.T) {
	var p harness.Provider = New(t.TempDir())
	if p.Info().Verified || p.Info().Name != "copilot" {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("an unverified provider must have no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("an unverified provider must have no hooks")
	}
	if why := harness.ReadOnly("copilot", p); !strings.Contains(why, "unverified") {
		t.Fatalf("read only: %q", why)
	}
}

func TestReadFixtures(t *testing.T) {
	for _, c := range []struct {
		fixture, turn, answer, prompt, pending string
	}{
		{"report-idle.jsonl", "idle", "The test passes now.", "fix the test", ""},
		{"report-between-turns.jsonl", "busy", "", "fix the test", ""},
		{"report-working.jsonl", "busy", "", "fix the test", ""},
		{"report-tool.jsonl", "", "", "fix the test", "bash: go test ./..."}, // a tool or a question: unknown
		{"report-permission.jsonl", "waiting", "", "fix the test", "bash: go test ./..."},
		{"report-permission-first.jsonl", "waiting", "", "fix the test", "shell: go test ./..."},
		{"report-permission-done.jsonl", "idle", "The test passes now.", "fix the test", ""},
		{"report-aborted.jsonl", "idle", "", "fix the test", ""},
		{"report-resumed.jsonl", "idle", "", "fix the test", ""},
		{"report-shutdown.jsonl", "", "The test passes now.", "fix the test", ""},
		{"report-new.jsonl", "", "", "", ""},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			place(t, home, c.fixture, sessionID, cwd)
			lock(t, home, sessionID, os.Getpid())
			p := NewWithLook(home, isCopilot)
			found := p.Discover()
			if len(found) != 1 || found[0].SessionID != sessionID || !strings.HasPrefix(found[0].Cwd, cwd) || found[0].PID != os.Getpid() {
				t.Fatalf("discover: %+v", found)
			}
			turn, at := p.State(found[0])
			if turn != c.turn {
				t.Fatalf("state %q, want %q", turn, c.turn)
			}
			if at.IsZero() {
				t.Fatal("no state time")
			}
			r := p.Read(sessionID, 4)
			if !r.OK || r.WroteAt.IsZero() || r.Activity.Prompt != c.prompt {
				t.Fatalf("reading: %+v", r)
			}
			if got, _ := harness.LastAnswer(r.Turns); got != c.answer {
				t.Fatalf("answer %q, want %q", got, c.answer)
			}
			now := ""
			if s := r.Activity.Pending; s != nil {
				now = harness.StepText(s.Tool, s.Target)
			}
			if now != c.pending {
				t.Fatalf("step %q, want %q", now, c.pending)
			}
		})
	}
}

// A lock counts only with a live process that is copilot, on a terminal,
// and that did not start after the lock.
func TestLockNeedsTheRightProcess(t *testing.T) {
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	copilot := harness.Proc{TTY: "/dev/ttys900", Argv: []string{"copilot"}, Start: time.Now().Add(-time.Hour)}
	for name, tc := range map[string]struct {
		pid  int
		look harness.Proc
		ok   bool
		want int
	}{
		"live":        {os.Getpid(), copilot, true, 1},
		"dead":        {c.Process.Pid, copilot, true, 0},
		"gone":        {os.Getpid(), copilot, false, 0},
		"other":       {os.Getpid(), harness.Proc{TTY: "/dev/ttys900", Argv: []string{"vim"}, Start: copilot.Start}, true, 0},
		"no terminal": {os.Getpid(), harness.Proc{Argv: []string{"copilot"}, Start: copilot.Start}, true, 0},
		"pid reused":  {os.Getpid(), harness.Proc{TTY: "/dev/ttys900", Argv: []string{"copilot"}, Start: time.Now().Add(time.Hour)}, true, 0},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			place(t, home, "report-idle.jsonl", sessionID, t.TempDir())
			lock(t, home, sessionID, tc.pid)
			p := NewWithLook(home, func(int) (harness.Proc, bool) { return tc.look, tc.ok })
			if got := p.Discover(); len(got) != tc.want {
				t.Fatalf("sessions: %+v", got)
			}
		})
	}
}

// This test's process is no copilot, so the real ps check refuses it.
func TestRealProcessCheck(t *testing.T) {
	home := t.TempDir()
	place(t, home, "report-idle.jsonl", sessionID, t.TempDir())
	lock(t, home, sessionID, os.Getpid())
	if got := New(home).Discover(); len(got) != 0 {
		t.Fatalf("sessions: %+v", got)
	}
}

func TestWorkspaceGivesNameAndFolder(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-new.jsonl", sessionID, cwd)
	if err := os.WriteFile(filepath.Join(home, "session-state", sessionID, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("id: %s\ncwd: %s\nsummary: Long summary\nname: \"fix login\"\nnested:\n  name: not this\n", sessionID, cwd)
	if err := os.WriteFile(filepath.Join(home, "session-state", sessionID, "workspace.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	lock(t, home, sessionID, os.Getpid())
	got := NewWithLook(home, isCopilot).Discover()
	if len(got) != 1 || got[0].Name != "fix login" || got[0].Cwd != cwd {
		t.Fatalf("sessions: %+v", got)
	}
}

// A folder change in the log beats workspace.yaml's older folder.
func TestContextChangeMovesTheSession(t *testing.T) {
	home := t.TempDir()
	path := place(t, home, "report-new.jsonl", sessionID, "/first")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"session.context_changed","data":{"cwd":"/second"},"id":"x","timestamp":"2026-11-05T10:00:09.000Z"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	lock(t, home, sessionID, os.Getpid())
	if got := NewWithLook(home, isCopilot).Discover(); len(got) != 1 || got[0].Cwd != "/second" {
		t.Fatalf("sessions: %+v", got)
	}
}

// A subagent's inner calls are no steps of the session, an ephemeral
// record changes nothing.
func TestSubagentCallsAndEphemeralRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	body := `{"type":"session.start","data":{"context":{"cwd":"/r"}},"timestamp":"2026-11-05T10:00:00Z"}
{"type":"user.message","data":{"content":"go"},"timestamp":"2026-11-05T10:00:01Z"}
{"type":"assistant.turn_start","data":{"turnId":"0"},"timestamp":"2026-11-05T10:00:02Z"}
{"type":"tool.execution_start","data":{"toolCallId":"outer","toolName":"task","arguments":{"description":"look"}},"timestamp":"2026-11-05T10:00:03Z"}
{"type":"tool.execution_start","data":{"toolCallId":"inner","parentToolCallId":"outer","toolName":"bash","arguments":{"command":"ls"}},"timestamp":"2026-11-05T10:00:04Z"}
{"type":"assistant.message","data":{"content":"inner note","parentToolCallId":"outer","toolRequests":[]},"timestamp":"2026-11-05T10:00:05Z"}
{"type":"assistant.turn_end","data":{"turnId":"0"},"ephemeral":true,"timestamp":"2026-11-05T10:00:06Z"}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := readTranscript(path)
	turns, act := tr.log.Result(4)
	if act.Tools != 1 || act.Pending == nil || act.Pending.ID != "outer" || tr.turn != "" {
		t.Fatalf("activity %+v state %q", act, tr.turn)
	}
	if got, _ := harness.LastAnswer(turns); got != "" {
		t.Fatalf("answer %q", got)
	}
}

func TestStampAndCheck(t *testing.T) {
	home := t.TempDir()
	p := NewWithLook(home, isCopilot)
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("empty: %s", st)
	}
	if p.Stamp(sessionID) != "" || p.Read(sessionID, 4).OK || p.Read("../../x", 4).OK {
		t.Fatal("a stamp or a reading with no log")
	}
	path := place(t, home, "report-working.jsonl", sessionID, "/r")
	p = NewWithLook(home, isCopilot) // a missing log is looked for again only after 30 s
	if st, d := p.Check(); st != "ok" {
		t.Fatalf("fixture: %s %s", st, d)
	}
	first := p.Stamp(sessionID)
	if first == "" {
		t.Fatal("no stamp")
	}
	if err := os.WriteFile(path, []byte(`{"kind":"a_new_format"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.Stamp(sessionID) == first {
		t.Fatal("the stamp stayed")
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("changed file: %s", st)
	}
}
