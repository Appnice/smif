package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/envelope"
	"github.com/appnice/smif/internal/ui"
)

// UI-3: the human finds a fix-profile item in the inbox (agent and state
// filters), opens its thread and its run, reads the record, the
// transcript and the branch diff, and applies the branch from the UI. The
// apply asks y/N first, the daemon refuses it on a dirty tree, and it
// merges on a clean one.
func TestUIRunDiffApplyLive(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "puzzle", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"fix\"\n"})
	e.Start()
	repo := e.Repos["puzzle"]
	r := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report",
		Subject: "fix the NPE", Body: "NPE in onboarding step 3.\n#fake:edit=src/fix.txt"})
	e.WaitState(r.ID, 15*time.Second, "done")
	runs := e.Runs(r.ID)
	if len(runs) != 1 || runs[0].Branch == "" {
		t.Fatalf("runs = %+v", runs)
	}
	run := runs[0]

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{Dashboard: true}))
	u.until("the overview", 10*time.Second, has("AGENTS (2)", "RECENT ACTIVITY"))

	// Inbox: filter by agent, then by state; the item is there.
	u.press("i")
	u.until("the inbox", 10*time.Second, has("INBOX · agent: all · state: all", ui.Short(r.ID)))
	for i := 0; i < 3; i++ {
		if _, a, _ := u.m.InboxOpen(); a == "puzzle" {
			break
		}
		u.press("f")
	}
	for i := 0; i < len(ui.MessageStates)+1; i++ {
		if _, _, s := u.m.InboxOpen(); s == "done" {
			break
		}
		u.press("s")
	}
	v := u.until("the filtered inbox", 10*time.Second, has("INBOX · agent: puzzle · state: done · 1 messages", ui.Short(r.ID), "fake handled crash.report"))
	t.Logf("inbox, filtered, at 120x40:\n%s", v)
	u.press("s") // rejected: nothing
	u.until("an empty filter", 10*time.Second, has("state: rejected · 0 messages", "No messages match the filter (agent: puzzle, state: rejected)."))
	u.press("S")
	u.until("the item again", 10*time.Second, has("state: done · 1 messages", "> "+ui.Short(r.ID)))

	// Thread: body, run with its branch.
	u.press("enter")
	v = u.until("the thread", 10*time.Second, has("MESSAGE · "+r.ID, "subject: fix the NPE", "NPE in onboarding step 3.", "THREAD "+r.ThreadID,
		"1 messages · 1 runs", "> "+ui.Short(run.ID), "branch "+run.Branch, "tab/enter run"))
	t.Logf("thread at 120x40:\n%s", v)

	// Run: record, transcript, diff.
	u.press("enter")
	v = u.until("the run record", 10*time.Second, has("RUN · "+run.ID, "agent puzzle · worker/fix · attempt 1",
		"cost $0.0010 · tokens in 1000 out 50", "session "+run.SessionID, run.Branch+" · head ", "src/fix.txt",
		`"summary": "fake handled crash.report"`, "a apply"))
	t.Logf("run record at 120x40:\n%s", v)
	u.press("2")
	v = u.until("the transcript", 10*time.Second, has("TRANSCRIPT", "init · session "+run.SessionID, "claude 2.1.316 · 2 tools",
		"assistant", "Done.", "result success · 2 turns · $0.0010 · in 1000 / out 50 tokens"))
	t.Logf("run transcript at 120x40:\n%s", v)
	u.press("3")
	v = u.until("the diff", 10*time.Second, has(run.Branch+" against HEAD of "+repo, "+++ b/src/fix.txt", "+edited by fake "+run.ID))
	t.Logf("run diff at 120x40:\n%s", v)

	// Apply asks first; n keeps the checkout as it is.
	u.press("a")
	v = u.until("the apply question", 5*time.Second, has("Apply run "+ui.Short(run.ID)+"? [y/N] It merges branch "+run.Branch, "into the current branch of "+repo))
	t.Logf("apply confirmation:\n%s", v)
	u.press("n")
	if u.m.Status() != "Not done: apply "+ui.Short(run.ID)+"." {
		t.Fatalf("status after n: %q", u.m.Status())
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(repo, "src", "fix.txt")); err == nil {
		t.Fatal("n merged the branch")
	}

	// Dirty tree: the daemon refuses and the UI shows why.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u.press("a", "y")
	v = u.until("the refusal", 15*time.Second, func(v string) bool {
		return strings.Contains(u.m.Status(), "refused") && strings.Contains(v, "LAST APPLY: Apply "+ui.Short(run.ID)+" refused")
	})
	if !strings.Contains(u.m.Status(), "uncommitted changes") {
		t.Fatalf("refusal reason: %q", u.m.Status())
	}
	t.Logf("refused apply (dirty tree):\n%s", v)
	if _, err := os.Stat(filepath.Join(repo, "src", "fix.txt")); err == nil {
		t.Fatal("a refused apply merged the branch")
	}

	// Clean tree: y merges.
	e.Git(repo, "checkout", "README.md")
	u.press("a", "y")
	v = u.until("the merge", 15*time.Second, func(v string) bool {
		return strings.HasPrefix(u.m.Status(), "Applied "+run.Branch+" to "+repo+": HEAD ") &&
			strings.Contains(v, "(no changes against HEAD: the branch is merged or empty)")
	})
	t.Logf("after apply:\n%s", v)
	if b, err := os.ReadFile(filepath.Join(repo, "src", "fix.txt")); err != nil || !strings.Contains(string(b), "edited by fake") {
		t.Fatalf("apply did not merge: %v", err)
	}
	if msg := e.Git(repo, "log", "-1", "--format=%s"); !strings.Contains(msg, "Merge "+run.Branch+" (smif run "+run.ID+")") {
		t.Fatalf("merge commit = %q", msg)
	}
	applies := 0
	for _, a := range e.Audit() {
		if a["event"] == "human.apply" {
			applies++
		}
	}
	if applies != 2 {
		t.Fatalf("human.apply audit rows = %d, want 2 (refused + merged)", applies)
	}

	// A merged branch is not offered again, and the daemon refuses it too.
	if strings.Contains(v, "a apply") {
		t.Fatalf("footer still offers apply after the merge:\n%s", v)
	}
	u.press("a")
	if !strings.HasPrefix(u.m.Status(), "Nothing to apply") || strings.Contains(u.view(), "[y/N]") {
		t.Fatalf("a on a merged branch: status %q\n%s", u.m.Status(), u.view())
	}
	if out, err := e.CLI("", "apply", run.ID); err == nil || !strings.Contains(out+err.Error(), "already merged") {
		t.Fatalf("second apply via the CLI: err %v, out %q", err, out)
	}

	// esc walks back: run, thread, inbox, overview.
	u.press("esc", "esc", "esc")
	u.until("the overview again", 10*time.Second, has("AGENTS (2)", "RECENT ACTIVITY"))
	if u.m.Down() {
		t.Fatal("the UI reports the daemon down")
	}
}
