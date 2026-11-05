package droid

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The transcripts in testdata are built from the research report (Droid
// 1.3.0 bundle and Entire's adapter), not captured from a real session: see
// testdata/README.md. A green test here proves only this parser.

const sessionID = "7c1e0000-0000-4000-8000-000000000001"

// place copies a fixture into a Droid home as session id, with cwd in it.
func place(t *testing.T, home, fixture, id, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "CWD", cwd), sessionID, id)
	dir := filepath.Join(home, "sessions", "-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func proc(cwd string, argv ...string) harness.Proc {
	return harness.Proc{PID: os.Getpid(), TTY: "/dev/ttys900", Argv: append([]string{"droid"}, argv...), Start: time.Now().Add(-time.Hour), Cwd: cwd}
}

func provider(home string, procs ...harness.Proc) *Provider {
	return NewWithProcs(home, func() []harness.Proc { return procs })
}

func TestUnverifiedAndNoWriteRoute(t *testing.T) {
	var p harness.Provider = New(t.TempDir())
	if p.Info().Verified || p.Info().Name != "droid" {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("an unverified provider must have no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("an unverified provider must have no hooks")
	}
	if why := harness.ReadOnly("droid", p); !strings.Contains(why, "unverified") {
		t.Fatalf("read only: %q", why)
	}
}

func TestReadFixtures(t *testing.T) {
	for _, c := range []struct {
		fixture, turn, answer, pending string
		tools                          int
	}{
		{"report-idle.jsonl", "idle", "The test passes now.", "", 1},
		{"report-working.jsonl", "busy", "", "", 1},
		{"report-prompt.jsonl", "busy", "", "", 0},
		{"report-pending.jsonl", "", "", "Execute: go test ./...", 1}, // a tool or an approval: unknown
		{"report-hidden.jsonl", "idle", "The test passes now.", "", 0},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			place(t, home, c.fixture, sessionID, cwd)
			p := provider(home, proc(cwd))
			found := p.Discover()
			if len(found) != 1 || found[0].SessionID != sessionID || found[0].Cwd != cwd || found[0].Name != "Fix the test" {
				t.Fatalf("discover: %+v", found)
			}
			turn, at := p.State(found[0])
			if turn != c.turn {
				t.Fatalf("state %q, want %q", turn, c.turn)
			}
			if at.IsZero() && turn != "" {
				t.Fatal("no state time")
			}
			r := p.Read(sessionID, 4)
			if !r.OK || r.WroteAt.IsZero() || r.Activity.Prompt != "fix the test" || r.Activity.Tools != c.tools {
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

func TestMatchNeedsFolderAndWriteSinceStart(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := place(t, home, "report-idle.jsonl", sessionID, cwd)
	if got := provider(home, proc(t.TempDir())).Discover(); len(got) != 0 {
		t.Fatalf("other folder: %+v", got)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := provider(home, proc(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("written before the start: %+v", got)
	}
	// --resume names the session, however old it is.
	if got := provider(home, proc(t.TempDir(), "--resume", sessionID)).Discover(); len(got) != 1 || got[0].SessionID != sessionID {
		t.Fatalf("resumed: %+v", got)
	}
	if got := provider(home, proc(t.TempDir(), "--resume="+sessionID)).Discover(); len(got) != 1 {
		t.Fatalf("resumed with =: %+v", got)
	}
}

func TestCwdFlagBeatsTheProcessFolder(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-idle.jsonl", sessionID, cwd)
	if got := provider(home, proc("/somewhere", "--cwd", cwd)).Discover(); len(got) != 1 {
		t.Fatalf("sessions: %+v", got)
	}
}

func TestDeadProcessShowsNoSession(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-idle.jsonl", sessionID, cwd)
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	pr := proc(cwd)
	pr.PID = c.Process.Pid
	if got := provider(home, pr).Discover(); len(got) != 0 {
		t.Fatalf("dead: %+v", got)
	}
}

func TestSubagentSessionIsSkipped(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-subagent.jsonl", sessionID, cwd)
	if got := provider(home, proc(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("subagent: %+v", got)
	}
}

func TestSessionProcFilter(t *testing.T) {
	for _, c := range []struct {
		tty  string
		argv []string
		want bool
	}{
		{"/dev/ttys1", []string{"droid"}, true},
		{"/dev/ttys1", []string{"/x/.local/bin/droid", "--resume", "abc"}, true},
		{"/dev/ttys1", []string{"droid", "--cwd", "/x"}, true},
		{"/dev/ttys1", []string{"droid", "exec", "go"}, false},
		{"/dev/ttys1", []string{"droid", "--cwd", "/x", "exec", "go"}, false},
		{"", []string{"droid"}, false},
		{"/dev/ttys1", []string{"vim", "droid"}, false},
	} {
		if got := sessionProc(harness.Proc{TTY: c.tty, Argv: c.argv}); got != c.want {
			t.Errorf("%v on %q: %v", c.argv, c.tty, got)
		}
	}
}

// A time is a string, a number or absent: none of them loses the record.
func TestTimestampForms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"type":"session_start","id":"7c1e0000-0000-4000-8000-000000000001"}
{"type":"message","timestamp":1793000000000,"message":{"role":"user","content":[{"type":"text","text":"one"}]}}
{"type":"message","timestamp":"2026-11-05T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"two"}]}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"three"}]}}
{"type":"message","timestamp":{"odd":1},"message":{"role":"assistant","content":[{"type":"text","text":"four"}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := readTranscript(path)
	turns, _ := tr.log.Result(10)
	if len(turns) != 4 || !turns[0].At.Equal(time.UnixMilli(1793000000000)) || turns[1].At.IsZero() || turns[3].Text != "four" || tr.turn != "idle" {
		t.Fatalf("turns %+v state %q", turns, tr.turn)
	}
}

func TestStampAndCheck(t *testing.T) {
	home := t.TempDir()
	p := provider(home)
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("empty: %s", st)
	}
	if p.Discover() != nil || p.Read("../../x", 4).OK {
		t.Fatal("a session with no folder")
	}
	path := place(t, home, "report-working.jsonl", sessionID, "/r")
	p = provider(home) // a missing transcript is looked for again only after 30 s
	if st, d := p.Check(); st != "ok" {
		t.Fatalf("fixture: %s %s", st, d)
	}
	first := p.Stamp(sessionID)
	if first == "" {
		t.Fatal("no stamp")
	}
	b, _ := os.ReadFile(filepath.Join("testdata", "report-idle.jsonl"))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if p.Stamp(sessionID) == first {
		t.Fatal("the stamp stayed")
	}
	if st, _ := p.State(harness.Session{SessionID: sessionID}); st != "idle" {
		t.Fatalf("state %q", st)
	}
	if err := os.WriteFile(path, []byte(`{"kind":"a_new_format"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("no first line: %s", st)
	}
	if err := os.WriteFile(path, []byte(`{"type":"session_start","id":"`+sessionID+`"}`+"\n"+`{"type":"turn"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("no message: %s", st)
	}
}
