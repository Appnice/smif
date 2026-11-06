package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

const (
	bgDone    = "aaaaaaaa-0000-4000-8000-000000000001"
	bgWorking = "bbbbbbbb-0000-4000-8000-000000000002"
)

// stubAgents writes a stand-in for `claude agents`: it counts its runs in
// a file next to it, checks its arguments and runs body.
func stubAgents(t *testing.T, body string) (command, runs string) {
	t.Helper()
	dir := t.TempDir()
	command, runs = filepath.Join(dir, "claude"), filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> " + runs + "\n" +
		"[ \"$*\" = 'agents --json --all' ] || exit 9\n" + body + "\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return command, runs
}

func runCount(runs string) int {
	b, _ := os.ReadFile(runs)
	return strings.Count(string(b), "run\n")
}

func listing(rows ...string) string {
	return "cat <<'JSON'\n[" + strings.Join(rows, ",") + "]\nJSON"
}

func bgJSON(id, sessionID, state string, startedAt time.Time) string {
	return fmt.Sprintf(`{"id":%q,"cwd":"/work","kind":"background","startedAt":%d,"sessionId":%q,"name":"nightly","state":%q}`,
		id, startedAt.UnixMilli(), sessionID, state)
}

// background waits for the first result of the command.
func background(t *testing.T, p *Provider) []harness.Session {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		p.Discover()
		if _, detail := p.CheckBackground(); !strings.Contains(detail, "has not run yet") {
			return p.Discover()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the command gave no result")
	return nil
}

func provider(t *testing.T, command string) *Provider {
	t.Helper()
	p := New(t.TempDir())
	p.Command = func() string { return command }
	return p
}

func TestBackgroundRowsAndStates(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	command, _ := stubAgents(t, listing(
		`{"pid":1,"cwd":"/work","kind":"interactive","startedAt":1,"sessionId":"cccccccc-0000-4000-8000-000000000003","name":"tab","status":"idle"}`,
		bgJSON("aaaaaaaa", bgDone, "done", start),
		bgJSON("bbbbbbbb", bgWorking, "working", start),
		bgJSON("dddddddd", "dddddddd-0000-4000-8000-000000000004", "blocked", start),
		bgJSON("eeeeeeee", "eeeeeeee-0000-4000-8000-000000000005", "idle", start),
		bgJSON("ffffffff", "ffffffff-0000-4000-8000-000000000006", "paused", start),
	))
	p := provider(t, command)
	rows := background(t, p)
	if len(rows) != 5 {
		t.Fatalf("rows = %+v", rows)
	}
	want := map[string]string{"done": "idle", "working": "busy", "blocked": "waiting", "idle": "idle", "paused": ""}
	for _, s := range rows {
		turn, at := p.State(s)
		if turn != want[s.Status] {
			t.Errorf("%s reads %q, want %q", s.Status, turn, want[s.Status])
		}
		if s.AttachID == "" || s.Name != "nightly" || s.Cwd != "/work" || at.IsZero() {
			t.Errorf("row %+v at %v", s, at)
		}
		if (s.Status == "done") != !s.EndedAt.IsZero() {
			t.Errorf("%s has EndedAt %v", s.Status, s.EndedAt)
		}
	}
	state, detail := p.CheckBackground()
	if state != "ok" || !strings.Contains(detail, "5 background sessions") || !strings.Contains(detail, "1 with a state") {
		t.Errorf("check: %s %q", state, detail)
	}
}

// The time of a done row comes from the last answer. Claude Code writes to
// a transcript long after the end, so the file's time says nothing. With
// no transcript the start time stands in.
func TestBackgroundDoneTime(t *testing.T) {
	start, answered := time.Now().Add(-90*time.Hour), time.Now().Add(-80*time.Hour)
	command, _ := stubAgents(t, listing(bgJSON("aaaaaaaa", bgDone, "done", start), bgJSON("bbbbbbbb", bgWorking, "done", start)))
	p := provider(t, command)
	dir := filepath.Join(p.dir, "projects", "-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := answered.UTC().Format(time.RFC3339)
	body := `{"type":"user","timestamp":"` + stamp + `","message":{"role":"user","content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + stamp + `","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, bgDone+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := background(t, p)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, s := range rows {
		want, answer := answered, answered
		if s.SessionID == bgWorking {
			want, answer = start, time.Time{}
		}
		if s.EndedAt.Unix() != want.Unix() || s.LastAnswerAt.Unix() != answer.Unix() {
			t.Errorf("%s: ended %v answer %v, want %v and %v", s.AttachID, s.EndedAt, s.LastAnswerAt, want, answer)
		}
		if turn, at := p.State(s); turn != "idle" || at.Unix() != want.Unix() {
			t.Errorf("%s: state %s at %v", s.AttachID, turn, at)
		}
	}
}

// A row's state time is when the state began: it holds from read to read,
// so the UI's "! 4m" counts up.
func TestBackgroundStateTimeHolds(t *testing.T) {
	command, runs := stubAgents(t, listing(bgJSON("bbbbbbbb", bgWorking, "blocked", time.Now())))
	p := provider(t, command)
	p.bg.every = 0
	first := background(t, p)[0].StatusAt
	for start := runCount(runs); runCount(runs) < start+2; time.Sleep(5 * time.Millisecond) {
		p.Discover()
	}
	if got := p.Discover()[0].StatusAt; !got.Equal(first) {
		t.Errorf("state time moved from %v to %v", first, got)
	}
}

func TestBackgroundBadRowsGiveNoRowAndNoArgv(t *testing.T) {
	good := bgJSON("aaaaaaaa", bgDone, "working", time.Now())
	for name, row := range map[string]string{
		"shell in id":     bgJSON("x; rm -rf /", bgWorking, "working", time.Now()),
		"option as id":    bgJSON("--dangerously", bgWorking, "working", time.Now()),
		"short id":        bgJSON("abc", bgWorking, "working", time.Now()),
		"no id":           bgJSON("", bgWorking, "working", time.Now()),
		"path as session": bgJSON("bbbbbbbb", "../../../etc/passwd", "working", time.Now()),
	} {
		t.Run(name, func(t *testing.T) {
			command, _ := stubAgents(t, listing(good, row))
			p := provider(t, command)
			rows := background(t, p)
			if len(rows) != 1 || rows[0].AttachID != "aaaaaaaa" {
				t.Fatalf("rows = %+v", rows)
			}
			if state, _ := p.CheckBackground(); state != "changed" {
				t.Errorf("check = %s", state)
			}
		})
	}
}

// A failed, missing or changed command gives no rows and no error, and a
// later good answer brings the rows back.
func TestBackgroundFailureGivesNoRows(t *testing.T) {
	for name, tc := range map[string]struct {
		command func(t *testing.T) string
		state   string
	}{
		"exit 1":   {func(t *testing.T) string { c, _ := stubAgents(t, "exit 1"); return c }, "none"},
		"missing":  {func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-claude") }, "none"},
		"empty":    {func(t *testing.T) string { return "" }, "none"},
		"not json": {func(t *testing.T) string { c, _ := stubAgents(t, "echo unknown command"); return c }, "changed"},
		"object":   {func(t *testing.T) string { c, _ := stubAgents(t, `echo '{"rows":[]}'`); return c }, "changed"},
	} {
		t.Run(name, func(t *testing.T) {
			p := provider(t, tc.command(t))
			if rows := background(t, p); len(rows) != 0 {
				t.Fatalf("rows = %+v", rows)
			}
			if state, detail := p.CheckBackground(); state != tc.state || detail == "" {
				t.Errorf("check: %s %q", state, detail)
			}
		})
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "ok")
	command, _ := stubAgents(t, "[ -f "+flag+" ] || exit 1\n"+listing(bgJSON("bbbbbbbb", bgWorking, "working", time.Now())))
	p := provider(t, command)
	p.bg.every = 10 * time.Millisecond
	if rows := background(t, p); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(5 * time.Second)
	for len(p.Discover()) != 1 && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(p.Discover()) != 1 {
		t.Error("the rows did not come back")
	}
}

// Discover runs on every session.list: it never waits for the command, and
// the command runs once while a thousand calls go by.
func TestBackgroundNeverWaitsAndRunsOnce(t *testing.T) {
	command, runs := stubAgents(t, "sleep 1\n"+listing(bgJSON("bbbbbbbb", bgWorking, "working", time.Now())))
	p := provider(t, command)
	start := time.Now()
	for range 1000 {
		if len(p.Discover()) != 0 {
			t.Fatal("rows before the command ended")
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("1000 calls took %v", d)
	}
	end := time.Now().Add(5 * time.Second)
	for len(p.Discover()) != 1 && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runCount(runs); n != 1 {
		t.Errorf("the command ran %d times", n)
	}
}

// A hung command ends at the timeout, also with a child that holds the
// pipe, and gives no rows.
func TestBackgroundHungCommandEnds(t *testing.T) {
	command, _ := stubAgents(t, "sleep 30 &\nsleep 30")
	p := provider(t, command)
	p.bg.timeout = 50 * time.Millisecond
	p.Discover()
	end := time.Now().Add(4 * time.Second)
	for {
		if state, detail := p.CheckBackground(); strings.Contains(detail, "did not answer") {
			if state != "none" || len(p.Discover()) != 0 {
				t.Errorf("check %s, rows %+v", state, p.Discover())
			}
			return
		}
		if time.Now().After(end) {
			t.Fatal("the hung command did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An interactive row keeps its session: it has a tab. No Command lists
// nothing and runs nothing.
func TestBackgroundInteractiveRowWins(t *testing.T) {
	command, _ := stubAgents(t, listing(bgJSON("aaaaaaaa", bgDone, "working", time.Now()), bgJSON("bbbbbbbb", bgWorking, "working", time.Now())))
	p := provider(t, command)
	dir := filepath.Join(p.dir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/work","kind":"interactive","status":"idle","statusUpdatedAt":1}`, os.Getpid(), bgDone)
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := background(t, p)
	if len(rows) != 2 || rows[0].SessionID != bgDone || rows[0].AttachID != "" || rows[1].SessionID != bgWorking {
		t.Fatalf("rows = %+v", rows)
	}
	off := New(t.TempDir())
	if off.Discover() != nil && len(off.Discover()) != 0 {
		t.Error("a provider with no Command listed rows")
	}
	if state, _ := off.CheckBackground(); state != "none" {
		t.Errorf("check = %s", state)
	}
}
