package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The transcripts in testdata are built from the research report (Qwen Code
// source at commit cf6b4270), not captured from a real session: see
// testdata/README.md. A green test here proves only this parser.

const (
	sessionID = "5f0d0000-0000-4000-8000-000000000001"
	secret    = "sentinel-ipc-token-value"
)

// registry writes a registry file as the report describes it, ipcToken too.
func registry(t *testing.T, home, file string, pid int, kind string, started time.Time, id string) {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"schemaVersion":1,"pid":%d,"procStart":"x","pidNs":null,"sessionId":%q,"cwd":"/repo","name":"repo","startedAt":%d,"qwenVersion":"0.26.7","kind":%q,"ipcPath":"/run/sock","ipcToken":%q}`,
		pid, id, started.UnixMilli(), kind, secret)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func transcriptFile(t *testing.T, root, fixture, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "projects", "-repo", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), sessionID, id)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnverifiedAndNoWriteRoute(t *testing.T) {
	dir := t.TempDir()
	var p harness.Provider = New(dir, dir)
	if p.Info().Verified || p.Info().Name != "qwen" {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("an unverified provider must have no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("an unverified provider must have no hooks")
	}
	if why := harness.ReadOnly("qwen", p); !strings.Contains(why, "unverified") {
		t.Fatalf("read only: %q", why)
	}
}

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	registry(t, home, fmt.Sprintf("%d.json", os.Getpid()), os.Getpid(), "tui", now, sessionID)
	registry(t, home, "1-acp.json", os.Getpid(), "headless", now, "5f0d0000-0000-4000-8000-000000000002")
	registry(t, home, "2-serve.json", os.Getpid(), "serve", now, "5f0d0000-0000-4000-8000-000000000003")
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	registry(t, home, "3.json", c.Process.Pid, "tui", now, "5f0d0000-0000-4000-8000-000000000004") // dead
	registry(t, home, "4.json", os.Getpid(), "tui", time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), "5f0d0000-0000-4000-8000-000000000005")
	got := New(home, home).Discover()
	if len(got) != 1 || got[0].SessionID != sessionID || got[0].PID != os.Getpid() || got[0].Cwd != "/repo" || got[0].Name != "repo" {
		t.Fatalf("sessions: %+v", got)
	}
}

// The runtime folder can lie apart from the home folder.
func TestRuntimeFolderApart(t *testing.T) {
	home, run := t.TempDir(), t.TempDir()
	transcriptFile(t, run, "report-idle.jsonl", sessionID)
	if r := New(home, run).Read(sessionID, 4); !r.OK {
		t.Fatal("no transcript in the runtime folder")
	}
}

func TestReadFixtures(t *testing.T) {
	for _, c := range []struct {
		fixture, turn, answer, prompt string
		tools                         int
		pending                       string
	}{
		{"report-idle.jsonl", "idle", "Done.", "fix the test", 1, ""},
		{"report-working.jsonl", "busy", "", "fix the test", 1, ""},
		{"report-pending.jsonl", "", "", "fix the test", 1, "run_shell_command: go test ./..."}, // a tool or an approval: unknown
		{"report-acp-ended.jsonl", "idle", "", "fix the test", 1, "run_shell_command: go test ./..."},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			home := t.TempDir()
			transcriptFile(t, home, c.fixture, sessionID)
			registry(t, home, "1.json", os.Getpid(), "tui", time.Now(), sessionID)
			p := New(home, home)
			found := p.Discover()
			if len(found) != 1 {
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
			if !r.OK || r.WroteAt.IsZero() || r.Activity.Prompt != c.prompt || r.Activity.Tools != c.tools {
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

// A rewind leaves a dead branch: the walk from the newest record skips it,
// and the subagent's record, which line order alone would show.
func TestRewindFollowsTheLiveBranch(t *testing.T) {
	tr := readTranscript(filepath.Join("testdata", "report-rewind.jsonl"))
	turns, act := tr.log.Result(10)
	var got []string
	for _, x := range turns {
		got = append(got, x.Text)
	}
	if strings.Join(got, "|") != "fix the test|try again|The live branch." || act.Prompt != "try again" || tr.turn != "idle" {
		t.Fatalf("turns %q prompt %q state %q", got, act.Prompt, tr.turn)
	}
}

// Thoughts and the text before a tool call are no answer.
func TestThoughtsAreNoAnswer(t *testing.T) {
	tr := readTranscript(filepath.Join("testdata", "report-working.jsonl"))
	turns, _ := tr.log.Result(10)
	if len(turns) != 2 || turns[1].Text != "" || turns[1].Tools != 1 {
		t.Fatalf("turns %+v", turns)
	}
}

// ipcToken stays out of everything the provider gives or keeps.
func TestTokenIsNeverRead(t *testing.T) {
	home := t.TempDir()
	transcriptFile(t, home, "report-idle.jsonl", sessionID)
	registry(t, home, "1.json", os.Getpid(), "tui", time.Now(), sessionID)
	p := New(home, home)
	r, ok := readRecord(filepath.Join(home, "sessions", "1.json"))
	if !ok {
		t.Fatal("no record")
	}
	found := p.Discover()
	st, detail := p.Check()
	b, err := json.Marshal(struct {
		R any
		F any
		S string
		D string
		G any
	}{r, found, st, detail, p.Read(sessionID, 4)})
	if err != nil {
		t.Fatal(err)
	}
	if out := fmt.Sprintf("%s %+v %+v", b, r, found); strings.Contains(out, secret) || strings.Contains(out, "/run/sock") {
		t.Fatalf("the token got out: %s", out)
	}
}

func TestStampAndCheck(t *testing.T) {
	home := t.TempDir()
	p := New(home, home)
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("empty: %s", st)
	}
	path := transcriptFile(t, home, "report-working.jsonl", sessionID)
	registry(t, home, "1.json", os.Getpid(), "tui", time.Now(), sessionID)
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
	if err := os.WriteFile(filepath.Join(home, "sessions", "1.json"), []byte(`{"schemaVersion":2,"processId":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("changed file: %s", st)
	}
}
