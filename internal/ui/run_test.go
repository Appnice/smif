package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
	"github.com/appnice/smif/internal/store"
)

// fakeInspector also serves runs, diffs, transcripts, lists and apply.
type fakeInspector struct {
	fakeActions
	runs        map[string]*api.RunView
	diffs       map[string]*DiffView
	transcripts map[string]string
	lists       []api.ListParams
	applies     []string
	applyErr    error
	runN, diffN int
}

func (f *fakeInspector) Messages(ctx context.Context, p api.ListParams) ([]api.MessageView, error) {
	f.lists = append(f.lists, p)
	var out []api.MessageView
	for _, x := range sample().Recent {
		if (p.Agent == "" || x.To == p.Agent || x.From.Agent == p.Agent) && (p.State == "" || x.State == p.State) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (f *fakeInspector) Run(ctx context.Context, id string) (*api.RunView, error) {
	f.runN++
	if r, ok := f.runs[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, &api.Error{Code: api.CodeNotFound, Message: "run " + id + " not found"}
}

func (f *fakeInspector) Diff(ctx context.Context, id string) (*DiffView, error) {
	f.diffN++
	if d, ok := f.diffs[id]; ok {
		c := *d
		return &c, nil
	}
	return nil, &api.Error{Code: api.CodeRejected, Message: "no branch"}
}

func (f *fakeInspector) Transcript(ctx context.Context, r api.RunView) (*TranscriptTail, error) {
	s, ok := f.transcripts[r.ID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &TranscriptTail{Path: r.Transcript, Size: int64(len(s)), Data: []byte(s)}, nil
}

func (f *fakeInspector) Apply(ctx context.Context, id string) (*ApplyResult, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	f.applies = append(f.applies, id)
	d := f.diffs[id]
	d.Diff = "" // merged: nothing left against HEAD
	return &ApplyResult{RunID: id, Branch: d.Branch, Repo: d.Repo, Head: "4f1c2d3e4b5a69788796a5b4c3d2e1f0a9b8c7d6"}, nil
}

// fakeTranscript is stream JSON as `claude -p --output-format stream-json`
// writes it: init, thinking, a tool use and its result, text, the result.
const fakeTranscript = `{"type":"system","subtype":"init","session_id":"5d1e7a40-0000-4000-8000-000000000001","model":"claude-sonnet-4-5","claude_code_version":"2.1.316","tools":["Read","Edit","Bash"]}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The NPE is in onboarding."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git grep -n onboardingStep src/"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"src/onboarding.kt:42: val step = onboardingStep!!\nsrc/onboarding.kt:57: onboardingStep = null"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"src/onboarding.kt","old_string":"!!","new_string":"?: return"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[{"type":"text","text":"old_string is not unique"}]}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the null check at onboarding.kt:42."}]}}
not json at all
{"type":"result","subtype":"success","is_error":false,"num_turns":4,"total_cost_usd":0.0123,"duration_ms":5400,"usage":{"input_tokens":1200,"output_tokens":340},"result":"{\"status\":\"done\",\"summary\":\"null check fixed\"}"}
`

const fakeDiff = `diff --git a/src/fix.txt b/src/fix.txt
new file mode 100644
index 0000000..3b18e51
--- /dev/null
+++ b/src/fix.txt
@@ -0,0 +1 @@
+edited by fake 01JRUN000000000000AAAAAAAA
diff --git a/README.md b/README.md
index 1111111..2222222 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
 # puzzle
-old line
+new line
`

const runID = "01JRUN000000000000AAAAAAAA"

func newInspModel(t *testing.T) (*Model, *fakeInspector) {
	t.Helper()
	ended := t0.Add(-1 * time.Minute)
	res, _ := json.Marshal(map[string]any{"status": "done", "summary": "null check fixed"})
	f := &fakeInspector{
		fakeActions: fakeActions{fakeSource: fakeSource{snaps: []Snapshot{sample()}}, threads: sampleThreads()},
		runs: map[string]*api.RunView{runID: {ID: runID, MessageID: "01JMSG0000000000000000RUN1", Agent: "cafe", Handler: "worker",
			Profile: "fix", Attempt: 1, State: "done", SessionID: "5d1e7a40-0000-4000-8000-000000000001",
			Workspace: "/home/p/.local/share/smif/worktrees/cafe", Branch: "smif/cafe/00thread01", HeadSHA: "9a8b7c6d5e4f3a2b1c0d",
			Diffstat: " src/fix.txt | 1 +\n 1 file changed, 1 insertion(+)", Result: res, CostUSD: 0.0123, InputTokens: 1200,
			OutputTokens: 340, Transcript: "/home/p/.local/share/smif/transcripts/" + runID + ".jsonl",
			StartedAt: t0.Add(-65 * time.Second), EndedAt: &ended}},
		diffs:       map[string]*DiffView{runID: {RunID: runID, Branch: "smif/cafe/00thread01", Repo: "/home/p/cafe", Diff: fakeDiff}},
		transcripts: map[string]string{runID: fakeTranscript},
	}
	m := New(f, Options{Dashboard: true, Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	return m, f
}

func TestRunScreenTabs(t *testing.T) {
	m, f := newInspModel(t)
	key(m, "tab") // RUNNING
	if !strings.Contains(view(m), "enter run · c cancel") {
		t.Fatalf("RUNNING footer:\n%s", view(m))
	}
	key(m, "enter")
	if m.RunOpen() != runID || m.RunTab() != "RECORD" || f.runN != 1 || f.diffN != 1 {
		t.Fatalf("run = %q tab = %q runN = %d diffN = %d", m.RunOpen(), m.RunTab(), f.runN, f.diffN)
	}
	out := view(m)
	for _, want := range []string{
		"RUN · " + runID, "agent cafe · worker/fix · attempt 1 · message 000000RUN1 · started 13:09:00 · took 5s",
		"1 RECORD", "2 TRANSCRIPT", "3 DIFF", "cost $0.0123 · tokens in 1200 out 340",
		"session 5d1e7a40-0000-4000-8000-000000000001 (open it with: claude --resume",
		"smif/cafe/00thread01 · head 9a8b7c6d5e4f · a applies it (asks y/N)", "1 file changed, 1 insertion(+)",
		`"summary": "null check fixed"`, "1 record · 2 transcript · 3 diff · ↑/↓ scroll · a apply · r refresh · esc back",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("record view lacks %q", want)
		}
	}
	checkSize(t, out, 120, 40)
	golden(t, "run_record_120x40.golden", out)

	key(m, "2")
	out = view(m)
	for _, want := range []string{
		"init · session 5d1e7a40-0000-4000-8000-000000000001 · model claude-sonnet-4-5 · claude 2.1.316 · 3 tools",
		"thinking (25 chars)", "tool Bash  git grep -n onboardingStep src/",
		"→ src/onboarding.kt:42: val step = onboardingStep!!  (2 lines)", "tool Edit  src/onboarding.kt",
		"→ ERROR old_string is not unique", "assistant", "Fixed the null check at onboarding.kt:42.", "not json at all",
		"result success · 4 turns · $0.0123 · in 1200 / out 340 tokens · 5400ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript view lacks %q", want)
		}
	}
	checkSize(t, out, 120, 40)
	golden(t, "run_transcript_120x40.golden", out)

	key(m, "3")
	out = view(m)
	for _, want := range []string{"smif/cafe/00thread01 against HEAD of /home/p/cafe", "+++ b/src/fix.txt",
		"+edited by fake " + runID, "-old line", "+new line", "@@ -1,2 +1,2 @@"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff view lacks %q", want)
		}
	}
	checkSize(t, out, 120, 40)
	golden(t, "run_diff_120x40.golden", out)

	key(m, "tab")
	if m.RunTab() != "RECORD" {
		t.Fatalf("tab after DIFF = %s", m.RunTab())
	}
	// A poll reloads the run record only (the run ended: no new transcript or diff).
	_, cmd := m.Update(tickMsg(t0))
	run(m, cmd)
	if f.runN != 2 || f.diffN != 1 {
		t.Fatalf("after a poll: runN = %d diffN = %d", f.runN, f.diffN)
	}
	key(m, "esc")
	if m.RunOpen() != "" || !strings.Contains(view(m), "AGENTS (3)") {
		t.Fatalf("esc did not go back:\n%s", view(m))
	}
}

func TestApplyAsksAndShowsRefusal(t *testing.T) {
	m, f := newInspModel(t)
	key(m, "tab", "enter", "3", "a")
	out := view(m)
	if !strings.Contains(out, "Apply run 00AAAAAAAA? [y/N] It merges branch smif/cafe/00thread01") ||
		!strings.Contains(out, "into the current branch of /home/p/cafe (git merge --no-ff in your main checkout).") ||
		!strings.Contains(out, "y yes · any other key: no") {
		t.Fatalf("apply confirmation:\n%s", out)
	}
	checkSize(t, out, 120, 40)
	golden(t, "confirm_apply_120x40.golden", out)
	key(m, "n")
	if len(f.applies) != 0 || m.Status() != "Not done: apply 00AAAAAAAA." {
		t.Fatalf("n applied: %v %q", f.applies, m.Status())
	}
	// Enter is not a yes either.
	key(m, "a", "enter")
	if len(f.applies) != 0 {
		t.Fatalf("enter applied: %v", f.applies)
	}

	f.applyErr = &api.Error{Code: api.CodeRejected, Message: "the main checkout has uncommitted changes; commit or stash them first"}
	key(m, "a", "y")
	want := "Apply 00AAAAAAAA refused: the main checkout has uncommitted changes; commit or stash them first"
	if m.Status() != want || !m.statusBad {
		t.Fatalf("refusal status = %q", m.Status())
	}
	if out := view(m); !strings.Contains(out, "LAST APPLY: "+want) || !strings.Contains(out, "+edited by fake") {
		t.Fatalf("refusal is not on the diff tab:\n%s", out)
	}

	f.applyErr = nil
	key(m, "a", "y")
	if len(f.applies) != 1 || m.Status() != "Applied smif/cafe/00thread01 to /home/p/cafe: HEAD 4f1c2d3e4b5a." {
		t.Fatalf("apply: %v %q", f.applies, m.Status())
	}
	if out := view(m); !strings.Contains(out, "(no changes against HEAD: the branch is merged or empty)") {
		t.Fatalf("the diff did not reload after apply:\n%s", out)
	}
}

func TestApplyWithoutBranch(t *testing.T) {
	m, f := newInspModel(t)
	f.runs[runID].Branch = ""
	key(m, "tab", "enter", "a")
	if m.confirm != nil || !strings.Contains(m.Status(), "has no worker branch") {
		t.Fatalf("apply without a branch: %q", m.Status())
	}
	key(m, "3")
	if !strings.Contains(view(m), "This run has no worker branch") || f.diffN != 0 {
		t.Fatalf("diff tab without a branch (diffN %d):\n%s", f.diffN, view(m))
	}
}

func TestThreadScreenOpensRun(t *testing.T) {
	m, f := newInspModel(t)
	f.threads["01JMSG0000000000000000DLQ1"].Runs[1].ID = runID // the newest run is the one with a record
	key(m, "tab", "tab", "tab", "enter")
	if m.ItemOpen() != "01JMSG0000000000000000DLQ1" {
		t.Fatalf("item = %q", m.ItemOpen())
	}
	// The newest run is selected; tab moves to the first.
	if !strings.Contains(view(m), "> 00AAAAAAAA") {
		t.Fatalf("newest run not selected:\n%s", view(m))
	}
	key(m, "tab")
	if !strings.Contains(view(m), "> 00CCCCCCC1") {
		t.Fatalf("tab did not move the run cursor:\n%s", view(m))
	}
	key(m, "shift+tab", "enter")
	if m.RunOpen() != runID {
		t.Fatalf("enter opened %q", m.RunOpen())
	}
	key(m, "esc")
	if m.RunOpen() != "" || m.ItemOpen() != "01JMSG0000000000000000DLQ1" {
		t.Fatalf("esc from the run: run %q item %q", m.RunOpen(), m.ItemOpen())
	}
	key(m, "esc")
	if m.ItemOpen() != "" {
		t.Fatal("second esc did not close the thread")
	}
}

func TestThreadShowsAllBodies(t *testing.T) {
	m, f := newInspModel(t)
	tv := f.threads["01JMSG000000000000000NEED1"]
	long := strings.Repeat("line\n", 6) + "last line of the reply"
	tv.Messages = append(tv.Messages, api.MessageView{ID: "01JMSG00000000000000REPLY1", ThreadID: tv.ThreadID,
		From: envelope.From{Agent: "human"}, To: "cafe", Kind: "reply", Intent: "store.release", State: "done", Body: long,
		Attachments: []envelope.Attachment{{Name: "crash.log", Size: 2048, MIME: "text/plain", SHA256: "abcdef0123456789abcdef"}},
		CreatedAt:   t0})
	// From RECENT ACTIVITY: enter opens the thread of any message.
	key(m, "shift+tab", "shift+tab", "enter")
	if m.ItemOpen() != "01JMSG000000000000000NEED1" {
		t.Fatalf("item = %q", m.ItemOpen())
	}
	out := view(m)
	for _, want := range []string{"2 messages · 1 runs", "human → cafe  reply store.release  done", "last line of the reply",
		"attachment: crash.log (2048 bytes, text/plain) sha256 abcdef012345"} {
		if !strings.Contains(out, want) {
			t.Errorf("thread view lacks %q\n%s", want, out)
		}
	}
}

func TestInboxFilters(t *testing.T) {
	m, f := newInspModel(t)
	key(m, "i")
	if open, a, s := m.InboxOpen(); !open || a != "" || s != "" {
		t.Fatalf("inbox open=%v agent=%q state=%q", open, a, s)
	}
	out := view(m)
	for _, want := range []string{"INBOX · agent: all · state: all · 2 messages", "MESSAGE", "00000NEED1", "000000DONE",
		"f/F agent · s/S state · x clear"} {
		if !strings.Contains(out, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	checkSize(t, out, 120, 40)
	golden(t, "inbox_120x40.golden", out)

	key(m, "f") // first agent: cafe
	if _, a, _ := m.InboxOpen(); a != "cafe" || f.lists[len(f.lists)-1].Agent != "cafe" {
		t.Fatalf("agent filter = %q, last list %+v", a, f.lists[len(f.lists)-1])
	}
	key(m, "s", "s", "s", "s", "s", "s") // accepted … done
	if _, _, s := m.InboxOpen(); s != "done" {
		t.Fatalf("state filter = %q", s)
	}
	out = view(m)
	if !strings.Contains(out, "agent: cafe · state: done · 1 messages") || strings.Contains(out, "00000NEED1") {
		t.Fatalf("filtered inbox:\n%s", out)
	}
	key(m, "S", "F")
	if _, a, s := m.InboxOpen(); a != "" || s != "needs_human" {
		t.Fatalf("S/F: agent %q state %q", a, s)
	}
	key(m, "x", "enter")
	if m.ItemOpen() != "01JMSG000000000000000NEED1" {
		t.Fatalf("enter in the inbox opened %q", m.ItemOpen())
	}
	key(m, "esc")
	if open, _, _ := m.InboxOpen(); !open || m.ItemOpen() != "" {
		t.Fatal("esc from the thread did not return to the inbox")
	}
	key(m, "esc")
	if open, _, _ := m.InboxOpen(); open {
		t.Fatal("esc did not close the inbox")
	}
	// enter on AGENTS opens the inbox of that agent.
	key(m, "down", "enter")
	if open, a, _ := m.InboxOpen(); !open || a != "observability" {
		t.Fatalf("AGENTS enter: open %v agent %q", open, a)
	}
}

func TestMessageStatesMatchStore(t *testing.T) {
	var want []string
	for _, s := range store.AllStates {
		want = append(want, string(s))
	}
	if strings.Join(want, ",") != strings.Join(MessageStates, ",") {
		t.Fatalf("MessageStates = %v, store has %v", MessageStates, want)
	}
}

func TestRenderTranscriptWraps(t *testing.T) {
	long := `{"type":"assistant","message":{"content":"` + strings.Repeat("word ", 40) + `"}}`
	lines := RenderTranscript([]byte(long), 60)
	if len(lines) < 4 {
		t.Fatalf("not wrapped: %q", lines)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Fatalf("line of %d cells: %q", w, l)
		}
	}
	if got := RenderTranscript(nil, 60); len(got) != 1 || !strings.Contains(got[0], "empty") {
		t.Fatalf("empty transcript: %q", got)
	}
}

func TestReadTranscriptTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString(`{"type":"system","subtype":"tick"}` + "\n")
	}
	os.WriteFile(p, []byte(b.String()), 0o644)
	tt, err := ReadTranscriptTail(p, 100)
	if err != nil || !tt.Cut || tt.Size != int64(b.Len()) {
		t.Fatalf("tail: %v %+v", err, tt)
	}
	if !strings.HasPrefix(string(tt.Data), `{"type"`) || len(tt.Data) > 100 {
		t.Fatalf("tail does not start at a line: %q", tt.Data)
	}
	full, err := ReadTranscriptTail(p, 1<<20)
	if err != nil || full.Cut || len(full.Data) != b.Len() {
		t.Fatalf("full: %v cut=%v len=%d", err, full.Cut, len(full.Data))
	}
	if _, err := ReadTranscriptTail("", 10); err == nil {
		t.Fatal("no path: no error")
	}
}

func TestDaemonSourceInspect(t *testing.T) {
	var cur *recCaller
	src := &DaemonSource{Dial: func() (Caller, error) { cur = &recCaller{}; return cur, nil }}
	ctx := context.Background()
	if _, err := src.Messages(ctx, api.ListParams{Agent: "cafe", State: "done", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Run(ctx, "R1"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Diff(ctx, "R1"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Apply(ctx, "R1"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cur.methods, ","); got != "thread.list,run.get,run.diff,run.apply" {
		t.Fatalf("methods = %s", got)
	}
	if p := cur.params[0].(api.ListParams); p.Agent != "cafe" || p.State != "done" || p.Limit != 5 {
		t.Fatalf("list params = %+v", p)
	}
	if p := cur.params[3].(api.IDParams); p.ID != "R1" {
		t.Fatalf("apply params = %+v", p)
	}
}
