package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanTailDropsTheCutLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"first":"` + strings.Repeat("x", TailBytes) + `"}` + "\n" + `{"second":1}` + "\nnot json\n" + `{"third":1}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := ScanTail(path, func(l []byte) { got = append(got, string(l)) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != `{"second":1}|{"third":1}` {
		t.Fatalf("%q", got)
	}
}

func TestTurnLog(t *testing.T) {
	at := time.Now()
	var l TurnLog
	l.Human("go", at)
	l.Human("go", at) // the same prompt in a second record
	l.Answer("first note", at)
	l.Call(Step{ID: "a", Tool: "shell", At: at})
	l.Call(Step{ID: "b", Tool: "edit", At: at})
	l.Done("a")
	turns, act := l.Result(10)
	if len(turns) != 2 || turns[1].Text != "" || turns[1].Tools != 2 {
		t.Fatalf("turns %+v", turns)
	}
	if act.Prompt != "go" || act.Tools != 2 || act.Pending == nil || act.Pending.ID != "b" || !act.Steps[0].Done {
		t.Fatalf("activity %+v", act)
	}
	l.Done("b")
	l.Answer("final", at)
	l.Human("next", at)
	turns, act = l.Result(2)
	if len(turns) != 2 || turns[0].Text != "final" || turns[1].Text != "next" || act.Tools != 0 {
		t.Fatalf("turns %+v act %+v", turns, act)
	}
}

func TestTargetIsOneLine(t *testing.T) {
	got := Target(map[string]any{"path": 3, "command": "go test\nmore"}, "path", "command")
	if got != "go test …" {
		t.Fatalf("%q", got)
	}
}

func TestProcStartOfThisProcess(t *testing.T) {
	got := ProcStart(os.Getpid())
	if got.IsZero() || got.After(time.Now()) || time.Since(got) > 24*time.Hour {
		t.Fatalf("start %v", got)
	}
	if !ProcStart(0).IsZero() {
		t.Fatal("start of no process")
	}
}

func TestProcLookOfThisProcess(t *testing.T) {
	p, ok := ProcLook(os.Getpid())
	if !ok || p.PID != os.Getpid() || p.Start.IsZero() || len(p.Argv) == 0 {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := ProcLook(0); ok {
		t.Fatal("found pid 0")
	}
}

func TestMatchProcs(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	now := time.Now()
	old, young := Proc{PID: 1, Start: now.Add(-time.Hour), Cwd: dir}, Proc{PID: 2, Start: now.Add(-time.Minute), Cwd: dir}
	named := Proc{PID: 3, Start: now, Cwd: other, Argv: []string{"agent", "ancient-id"}}
	never := Proc{PID: 4, Cwd: dir}
	found := []Candidate{
		{ID: "ancient-id", Cwd: dir, Created: now.Add(-48 * time.Hour), Wrote: now.Add(-24 * time.Hour)},
		{ID: "stale-one", Cwd: dir, Created: now.Add(-3 * time.Hour), Wrote: now.Add(-2 * time.Hour)},
		{ID: "first-one", Cwd: dir, Created: now.Add(-50 * time.Minute), Wrote: now},
		{ID: "second-one", Cwd: dir, Created: now.Add(-30 * time.Second), Wrote: now},
		{ID: "elsewhere", Cwd: other, Created: now, Wrote: now},
	}
	got := map[int]string{}
	for _, o := range MatchProcs([]Proc{old, young, named, never}, found, func(p Proc) string {
		if len(p.Argv) == 2 {
			return p.Argv[1]
		}
		return ""
	}) {
		got[o.Proc.PID] = o.Session.ID
	}
	if len(got) != 3 || got[1] != "first-one" || got[2] != "second-one" || got[3] != "ancient-id" {
		t.Fatalf("%v", got)
	}
}
