package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The rollouts in testdata are built from the research report (Codex source
// at commit c9035417), not captured from a real session: see
// testdata/README.md. A green test here proves only this parser.

const threadID = "019a0000-0000-7000-8000-000000000001"

// place copies a fixture into a Codex home as session id, with cwd in it.
func place(t *testing.T, home, fixture, id, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "CWD", cwd)
	s = strings.ReplaceAll(s, threadID, id)
	dir := filepath.Join(home, "sessions", "2026", "11", "05")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-05T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func proc(cwd string, argv ...string) Proc {
	return Proc{PID: os.Getpid(), TTY: "/dev/ttys900", Argv: append([]string{"codex"}, argv...), Start: time.Now().Add(-time.Hour), Cwd: cwd}
}

func provider(home string, procs ...Proc) *Provider {
	return NewWithProcs(home, func() []Proc { return procs })
}

func TestVerifiedWithNoForkOrHooks(t *testing.T) {
	var p harness.Provider = New(t.TempDir())
	if !p.Info().Verified || p.Info().Name != "codex" {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("codex must have no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("codex must have no hooks")
	}
	if why := harness.ReadOnly("codex", p); why != "" {
		t.Fatalf("read only: %q", why)
	}
}

// Fixtures per state: the state from the transcript's end, the prompt, the
// final answer and the current step.
func TestReadFixtures(t *testing.T) {
	for _, c := range []struct {
		fixture, turn, answer, prompt string
		tools                         int
		pending                       string
	}{
		{"report-idle-paginated.jsonl", "idle", "The test passes now.", "fix the test", 1, ""},
		{"report-idle-legacy.jsonl", "idle", "Legacy answer.", "fix the test", 1, ""},
		{"report-working.jsonl", "busy", "", "fix the test", 1, ""},
		{"report-pending.jsonl", "", "", "fix the test", 1, "shell: go test ./..."}, // a tool or an approval: unknown
		{"report-pending-never.jsonl", "busy", "", "fix the test", 1, "shell: go test ./..."},
		{"report-aborted.jsonl", "idle", "", "fix the test", 1, "shell: go test ./..."},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			place(t, home, c.fixture, threadID, cwd)
			p := provider(home, proc(cwd))
			found := p.Discover()
			if len(found) != 1 || found[0].SessionID != threadID || found[0].Cwd != cwd || found[0].PID != os.Getpid() {
				t.Fatalf("discover: %+v", found)
			}
			turn, at := p.State(found[0])
			if turn != c.turn {
				t.Fatalf("state %q, want %q", turn, c.turn)
			}
			if at.IsZero() {
				t.Fatal("no state time")
			}
			r := p.Read(threadID, 4)
			if !r.OK || r.WroteAt.IsZero() || r.Activity.Prompt != c.prompt || r.Activity.Tools != c.tools {
				t.Fatalf("reading: %+v", r)
			}
			if len(r.Turns) < 2 || r.Turns[0].Role != harness.RoleHuman || r.Turns[0].Text != c.prompt || r.Turns[1].Role != harness.RoleAgent || r.Turns[1].Tools != c.tools {
				t.Fatalf("turns: %+v", r.Turns)
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
			if len(r.Activity.Steps) != c.tools {
				t.Fatalf("steps: %+v", r.Activity.Steps)
			}
		})
	}
}

// A note before a tool call is a step's comment, not the answer, and the
// injected context of a response_item is not a prompt.
func TestWorkingHasNoAnswerYet(t *testing.T) {
	tr := readTranscript(filepath.Join("testdata", "report-working.jsonl"))
	turns, act := tr.log.Result(10)
	if len(turns) != 2 || turns[1].Text != "" || act.Steps[0].Done != true || act.Pending != nil {
		t.Fatalf("turns %+v act %+v", turns, act)
	}
	if strings.Contains(turns[0].Text, "environment_context") {
		t.Fatalf("injected context as a prompt: %+v", turns)
	}
}

// A rollout that was not written since the process started, or that lies
// in another folder, belongs to no live process.
func TestMatchNeedsFolderAndWriteSinceStart(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := place(t, home, "report-idle-paginated.jsonl", threadID, cwd)
	if got := provider(home, proc(t.TempDir())).Discover(); len(got) != 0 {
		t.Fatalf("other folder: %+v", got)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := provider(home, proc(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("an old rollout: %+v", got)
	}
	if got := provider(home).Discover(); len(got) != 0 {
		t.Fatalf("no process: %+v", got)
	}
	noStart := proc(cwd)
	noStart.Start = time.Time{} // ps gave no start time: any rollout would pass
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := provider(home, noStart).Discover(); len(got) != 0 {
		t.Fatalf("no start time: %+v", got)
	}
}

// A dead process leaves no row, also while the last ps is still fresh, so a
// task_started left open by a crash does not show as busy.
func TestDeadProcessShowsNoSession(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-working.jsonl", threadID, cwd)
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	pr := proc(cwd)
	pr.PID = c.Process.Pid
	if got := provider(home, pr).Discover(); len(got) != 0 {
		t.Fatalf("dead process: %+v", got)
	}
}

// Two processes in one folder take the rollouts newest to newest.
func TestTwoProcessesOneFolder(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	first, second := "019a0000-0000-7000-8000-00000000000a", "019a0000-0000-7000-8000-00000000000b"
	place(t, home, "report-idle-paginated.jsonl", first, cwd)
	path := place(t, home, "report-idle-paginated.jsonl", second, cwd)
	b, _ := os.ReadFile(path)
	later := strings.ReplaceAll(string(b), `"timestamp":"2026-11-05T10:00:00.000Z"`, `"timestamp":"2026-11-05T11:00:00.000Z"`)
	if err := os.WriteFile(path, []byte(later), 0o600); err != nil {
		t.Fatal(err)
	}
	old, young := proc(cwd), proc(cwd)
	young.PID, young.Start = os.Getppid(), time.Now().Add(-time.Minute)
	got := provider(home, old, young).Discover()
	byPID := map[int]string{}
	for _, s := range got {
		byPID[s.PID] = s.SessionID
	}
	if byPID[old.PID] != first || byPID[young.PID] != second {
		t.Fatalf("sessions: %+v", got)
	}
}

// `codex resume <id>` names its session, whatever the folder and the age.
func TestResumeNamesItsSession(t *testing.T) {
	home := t.TempDir()
	path := place(t, home, "report-idle-paginated.jsonl", threadID, "/elsewhere")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	got := provider(home, proc(t.TempDir(), "resume", threadID)).Discover()
	if len(got) != 1 || got[0].SessionID != threadID {
		t.Fatalf("sessions: %+v", got)
	}
}

// A subagent's rollout in the same folder is no session of its own.
func TestSubagentRolloutIsSkipped(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-subagent.jsonl", "019a0000-0000-7000-8000-00000000000f", cwd)
	if got := provider(home, proc(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("sessions: %+v", got)
	}
}

// A compressed rollout is skipped, with no error: no zstd dependency.
func TestCompressedRolloutIsSkipped(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := place(t, home, "report-idle-paginated.jsonl", threadID, cwd)
	if err := os.Rename(path, path+".zst"); err != nil {
		t.Fatal(err)
	}
	p := provider(home, proc(cwd))
	if got := p.Discover(); len(got) != 0 {
		t.Fatalf("sessions: %+v", got)
	}
	if r := p.Read(threadID, 4); r.OK {
		t.Fatalf("reading: %+v", r)
	}
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("check: %s", st)
	}
}

// session_index.jsonl gives the name, its newest line wins.
func TestNameFromIndex(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	place(t, home, "report-idle-paginated.jsonl", threadID, cwd)
	idx := `{"id":"` + threadID + `","thread_name":"old","updated_at":"x"}` + "\n" + `{"id":"` + threadID + `","thread_name":"payments","updated_at":"y"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := provider(home, proc(cwd)).Discover(); len(got) != 1 || got[0].Name != "payments" {
		t.Fatalf("sessions: %+v", got)
	}
}

func TestStampChangesWithTheFile(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := place(t, home, "report-working.jsonl", threadID, cwd)
	p := provider(home, proc(cwd))
	if p.Stamp("019a0000-0000-7000-8000-0000000000ff") != "" {
		t.Fatal("a stamp for no rollout")
	}
	first := p.Stamp(threadID)
	if first == "" {
		t.Fatal("no stamp")
	}
	if st, _ := p.State(harness.Session{SessionID: threadID}); st != "busy" {
		t.Fatalf("state %q", st)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"timestamp":"2026-11-05T10:01:00.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"Later."}}` + "\n")
	f.Close()
	if p.Stamp(threadID) == first {
		t.Fatal("the stamp stayed")
	}
	if st, _ := p.State(harness.Session{SessionID: threadID}); st != "idle" {
		t.Fatalf("state %q", st)
	}
}

func TestCheckStates(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	p := provider(home)
	if st, _ := p.Check(); st != "none" {
		t.Fatalf("empty: %s", st)
	}
	path := place(t, home, "report-idle-paginated.jsonl", threadID, cwd)
	if st, d := p.Check(); st != "ok" {
		t.Fatalf("fixture: %s %s", st, d)
	}
	if err := os.WriteFile(path, []byte(`{"type":"something_new","payload":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("changed file: %s", st)
	}
}

func TestSessionProcFilter(t *testing.T) {
	for _, c := range []struct {
		tty  string
		argv []string
		want bool
	}{
		{"/dev/ttys1", []string{"codex"}, true},
		{"/dev/ttys1", []string{"/opt/lib/vendor/bin/codex", "--no-alt-screen"}, true},
		{"/dev/ttys1", []string{"codex", "fix the test"}, true},
		{"/dev/ttys1", []string{"codex", "resume", "--last"}, true},
		{"/dev/ttys1", []string{"node", "/usr/lib/node_modules/.bin/codex"}, true},
		{"/dev/ttys1", []string{"codex", "exec", "go"}, false},
		{"/dev/ttys1", []string{"codex", "app-server"}, false},
		{"/dev/ttys1", []string{"node", "/usr/lib/.bin/codex", "app-server"}, false},
		{"/dev/ttys1", []string{"node", "/x/@openai/codex/bin/codex.js"}, false}, // the wrapper, its child is the session
		{"/dev/ttys1", []string{"apply_patch"}, false},
		{"", []string{"codex"}, false},
	} {
		if got := sessionProc(Proc{TTY: c.tty, Argv: c.argv}); got != c.want {
			t.Errorf("%v on %q: %v", c.argv, c.tty, got)
		}
	}
}

func TestParsePS(t *testing.T) {
	got := harness.ParsePS("  101 ??       Thu Nov  5 10:00:00 2026     /usr/sbin/cfprefsd agent\n" +
		"  102 ttys003  Thu Nov  5 10:00:01 2026     codex resume abc\n" +
		"  103 pts/2    Mon Oct 12 09:08:07 2026     node /a/bin/codex\n  bad\n")
	if len(got) != 3 || got[0].TTY != "" || got[1].TTY != "/dev/ttys003" || got[2].TTY != "/dev/pts/2" {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got[1].Argv, []string{"codex", "resume", "abc"}) {
		t.Fatalf("%+v", got[1])
	}
	want := time.Date(2026, 11, 5, 10, 0, 1, 0, time.Local)
	if !got[1].Start.Equal(want) {
		t.Fatalf("start %v, want %v", got[1].Start, want)
	}
}
