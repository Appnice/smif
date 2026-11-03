//go:build darwin || linux

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
	"github.com/appnice/smif/internal/ui"
)

// tourDump writes each screen of the tour to SMIF_TOUR_DIR (when set)
// as NN-name.txt, and logs it.
type tourDump struct {
	t   *testing.T
	dir string
	n   int
}

func (d *tourDump) save(name, text string) {
	d.t.Helper()
	d.n++
	d.t.Logf("screen %02d %s:\n%s", d.n, name, text)
	if d.dir == "" {
		return
	}
	p := filepath.Join(d.dir, fmt.Sprintf("%02d-%s.txt", d.n, name))
	if err := os.WriteFile(p, []byte(text+"\n"), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

// focus presses tab until the overview focus is on p.
func (u *liveUI) focus(p ui.Panel) {
	u.t.Helper()
	for i := 0; i < 7 && u.m.Focus() != p; i++ {
		u.press("tab")
	}
	if u.m.Focus() != p {
		u.t.Fatalf("focus never reached %s (at %s)", p, u.m.Focus())
	}
}

// untilFresh is until, but the screen must also show a poll that started
// after the call: the header time ("updated HH:MM:SS") is at or after the
// next full second. A screen saved after an action then shows the daemon
// state after the action, not an older snapshot.
func (u *liveUI) untilFresh(what string, timeout time.Duration, cond func(v string) bool) string {
	u.t.Helper()
	t0 := time.Now().Truncate(time.Second).Add(time.Second)
	return u.until(what, timeout, func(v string) bool {
		head, _, _ := strings.Cut(v, "\n")
		_, at, ok := strings.Cut(head, "updated ")
		if !ok || len(at) < 8 {
			return false
		}
		hms, err := time.ParseInLocation("15:04:05", at[:8], time.Local)
		if err != nil {
			return false
		}
		y, mo, dd := t0.Date()
		polled := time.Date(y, mo, dd, hms.Hour(), hms.Minute(), hms.Second(), 0, time.Local)
		return !polled.Before(t0) && cond(v)
	})
}

// UI-6: a tour of every screen and action of `smif ui` against one
// daemon with five agents and mixed traffic: an ask answered by a command
// handler, crash events triaged by workers, a needs_human item, a dead
// letter, a fix branch, a long run with an item queued behind it and an
// interactive session. The real binary also starts on a terminal, and
// `smif watch` prints both of its modes. Set SMIF_TOUR_DIR to keep
// the screens as text files.
func TestUITourLive(t *testing.T) {
	d := &tourDump{t: t, dir: os.Getenv("SMIF_TOUR_DIR")}
	if d.dir != "" {
		if err := os.MkdirAll(d.dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cafeRules := `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/smif/store-status.sh"]
[[agents.AGENT.rules]]
match = {}
handler = "worker"
profile = "triage"
`
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe", Extra: "budget_usd = 2", Rules: cafeRules, Before: withScript("store-status.sh", storeStatusScript)},
		AgentSpec{ID: "puzzle", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"fix\"\n"},
		AgentSpec{ID: "web", Extra: "max_attempts = 1"},
		AgentSpec{ID: "lab"})
	e.Start()

	// Traffic. 1: an ask that a command handler answers (no claude).
	c := e.AgentClient("observability")
	var askRes api.SendResult
	if err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "cafe", Kind: "ask", Intent: "store.status",
		Subject: "Is 1.0.1 live on Play?"}, WaitS: 5}, &askRes, 10*time.Second); err != nil || askRes.Reply == nil {
		t.Fatalf("ask: %v %+v", err, askRes)
	}
	c.Close()
	t.Logf("command handler reply: %s", askRes.Reply.Text)
	// 2: crash events that a worker triages (with cost).
	crash1 := e.Send("observability", "message.emit", envelope.Draft{To: "cafe", Kind: "event", Intent: "crash.report",
		Subject: "NullPointerException in onboarding step 3", Body: "Sentry issue CAFE-41, 12 users.\n#fake:cost=0.12"})
	crash2 := e.Send("observability", "message.emit", envelope.Draft{To: "cafe", Kind: "event", Intent: "crash.report",
		Subject: "ANR on the paywall", Body: "Play vitals, 3 users.\n#fake:cost=0.12"})
	e.WaitState(crash1.ID, 15*time.Second, "done")
	e.WaitState(crash2.ID, 15*time.Second, "done")
	// 3: one needs_human item.
	need := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "release.decide",
		Subject: "Ship 1.0.2 now or wait for review?", Body: "Store review passed for 1.0.1.\n#fake:result=needs_human"})
	e.WaitState(need.ID, 15*time.Second, "needs_human")
	// 4: one dead letter (max_attempts = 1, the worker exits 3).
	dead := e.Send("observability", "message.emit", envelope.Draft{To: "web", Kind: "event", Intent: "crash.report",
		Subject: "500 on /pricing", Body: "Caddy log, 40 errors.\n#fake:exit=3"})
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	// 5: one fix branch.
	fix := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report",
		Subject: "Card deck crash on shuffle", Body: "IndexOutOfRange in Deck.shuffle.\n#fake:edit=src/fix.txt"})
	e.WaitState(fix.ID, 15*time.Second, "done")
	fixRun := e.Runs(fix.ID)[0]
	if fixRun.Branch == "" {
		t.Fatalf("fix run has no branch: %+v", fixRun)
	}
	// 6: a long run with one item queued behind it, and an interactive session.
	long := e.Send("observability", "message.send", envelope.Draft{To: "lab", Kind: "tell", Intent: "report.build",
		Subject: "Build the weekly balance report", Body: "This takes a while.\n#fake:sleep=60"})
	e.WaitState(long.ID, 15*time.Second, "running")
	waits := e.Send("observability", "message.send", envelope.Draft{To: "lab", Kind: "tell", Intent: "x.y", Subject: "waits for the long run"})
	e.WaitState(waits.ID, 5*time.Second, "queued")
	var pr map[string]any
	e.Call("presence.update", api.PresenceParams{Event: "start", Cwd: e.Repos["lab"], SessionID: "sess-tour-1"}, &pr)
	var longRun string
	e.WaitFor("the long run", 10*time.Second, func() bool {
		for _, r := range e.Runs(long.ID) {
			if r.State == "running" {
				longRun = r.ID
				return true
			}
		}
		return false
	})

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{Dashboard: true}))

	// Overview.
	v := u.until("the overview", 10*time.Second, has("AGENTS (5)", "RUNNING (1)", "NEEDS YOU (1)", "DEAD LETTERS (1)", "RECENT ACTIVITY", "EVENTS ("))
	for agent, want := range map[string]string{
		"cafe": "cafe 0 0 1 0 $0.24 $1.76 -",
		"lab":  "lab 1 1 0 0 $0.00 - interactive",
		"web":  "web 0 0 0 1 $0.00 - -",
	} {
		if got := strings.Join(agentRow(v, agent), " "); got != want {
			t.Errorf("agent row %s = %q, want %q", agent, got, want)
		}
	}
	d.save("overview", v)

	// Help.
	u.press("?")
	d.save("help", u.until("the help", 5*time.Second, has("KEYS: HOME", "HELP · lines")))
	u.press("esc")
	u.until("the overview after help", 5*time.Second, has("AGENTS (5)"))

	// AGENTS: enter opens the inbox of the agent under the cursor (cafe).
	u.press("enter")
	d.save("agent-inbox", u.until("the inbox of cafe", 10*time.Second, has("INBOX · agent: cafe", ui.Short(need.ID))))
	u.press("esc")

	// RUNNING: enter opens the live run; c then y cancels it.
	u.focus(ui.PanelRunning)
	u.press("enter")
	d.save("run-live-record", u.until("the live run", 10*time.Second, has("RUN · "+longRun)))
	u.press("2")
	d.save("run-live-transcript", u.until("the live transcript", 10*time.Second, func(v string) bool {
		return strings.Contains(v, "TRANSCRIPT") && !strings.Contains(v, "loading…")
	}))
	u.press("esc")
	u.until("the overview from the run", 10*time.Second, has("AGENTS (5)"))
	u.focus(ui.PanelRunning)
	u.press("c")
	d.save("confirm-cancel-running", u.until("the cancel question", 5*time.Second, has("Cancel "+ui.Short(long.ID), "y yes · any other key: no")))
	u.press("y")
	wantCancel := "Cancelled " + ui.Short(long.ID) + ": run " + ui.Short(longRun) + " stopped (cancelled) and the item is rejected."
	u.until("the cancel result", 20*time.Second, func(string) bool { return u.m.Status() == wantCancel })
	e.WaitState(long.ID, 5*time.Second, "rejected")
	e.WaitState(waits.ID, 15*time.Second, "done") // the queued item runs next
	d.save("after-cancel", u.untilFresh("RUNNING empty", 10*time.Second, has("RUNNING (0)", wantCancel)))

	// NEEDS YOU: open, reply inline, the worker continues the thread.
	u.focus(ui.PanelNeedsYou)
	u.press("enter")
	// The first frame comes from the list snapshot; thread.get fills in the body and the thread.
	d.save("needs-you-item-first-frame", u.view())
	d.save("needs-you-item", u.until("the needs-you item", 10*time.Second, has("NEEDS YOU · "+need.ID, "QUESTION FROM THE WORKER",
		"Store review passed for 1.0.1.", "THREAD "+need.ThreadID, "a reply · c cancel")))
	u.press("a", "Wait for the review, then ship.")
	d.save("reply-box", u.view())
	u.press("enter")
	u.until("the reply result", 10*time.Second, func(string) bool { return strings.HasPrefix(u.m.Status(), "Reply sent to "+ui.Short(need.ID)) })
	e.WaitState(need.ID, 15*time.Second, "done")
	d.save("thread-after-reply", u.untilFresh("the continued thread", 15*time.Second, has("2 messages · 2 runs", "human → cafe  reply release.decide  done")))
	u.press("esc")
	u.until("needs you empty", 10*time.Second, has("NEEDS YOU (0)"))

	// DEAD LETTERS: open, then retry (y); exit=3 fails again.
	u.focus(ui.PanelDLQ)
	u.press("enter")
	d.save("dead-letter-item", u.until("the dead letter", 10*time.Second, has("DEAD LETTER · "+dead.ID, "REASON", "Caddy log, 40 errors.", "THREAD "+dead.ThreadID, "t retry")))
	u.press("esc")
	u.until("the overview from the dead letter", 10*time.Second, has("DEAD LETTERS (1)"))
	u.focus(ui.PanelDLQ)
	u.press("t")
	d.save("confirm-retry", u.until("the retry question", 5*time.Second, has("Retry dead letter "+ui.Short(dead.ID))))
	u.press("y")
	u.until("the retry result", 10*time.Second, func(string) bool { return u.m.Status() == "Retried "+ui.Short(dead.ID)+": it is back in the queue." })
	e.WaitFor("a second run of the dead letter", 15*time.Second, func() bool { return len(e.Runs(dead.ID)) == 2 })
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	d.save("after-retry", u.untilFresh("the dead letter back", 10*time.Second, has("DEAD LETTERS (1)", "Retried "+ui.Short(dead.ID))))

	// RECENT ACTIVITY: enter opens the thread of the newest message.
	u.focus(ui.PanelRecent)
	u.press("enter")
	d.save("recent-thread", u.until("a thread from RECENT ACTIVITY", 10*time.Second, has("MESSAGE · ", "THREAD ")))
	u.press("esc")

	// EVENTS: the live audit feed; enter opens the thread of the event.
	u.until("the overview from the thread", 10*time.Second, has("AGENTS (5)"))
	u.focus(ui.PanelEvents)
	d.save("events-focused", u.untilFresh("the events panel", 10*time.Second, has("▶ EVENTS (")))
	if u.m.Selected() == "" {
		t.Fatal("no event under the cursor")
	}
	u.press("enter")
	d.save("event-thread", u.until("the thread of the event", 10*time.Second, func(v string) bool { return u.m.ItemOpen() != "" && strings.Contains(v, "THREAD ") }))
	u.press("esc")

	// Inbox with filters, then the fix thread, its run, transcript, diff and apply.
	u.press("i")
	u.until("the inbox", 10*time.Second, has("INBOX · agent: all · state: all"))
	for i := 0; i < 6; i++ {
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
	d.save("inbox-filtered", u.until("the filtered inbox", 10*time.Second, has("INBOX · agent: puzzle · state: done · 1 messages", ui.Short(fix.ID))))
	u.press("enter")
	d.save("fix-thread", u.until("the fix thread", 10*time.Second, has("MESSAGE · "+fix.ID, "branch "+fixRun.Branch)))
	u.press("enter")
	d.save("run-record", u.until("the run record", 10*time.Second, has("RUN · "+fixRun.ID, "src/fix.txt", "a apply")))
	u.press("2")
	d.save("run-transcript", u.until("the transcript", 10*time.Second, has("TRANSCRIPT", "result success")))
	u.press("3")
	d.save("run-diff", u.until("the diff", 10*time.Second, has("+++ b/src/fix.txt")))
	u.press("a")
	d.save("confirm-apply", u.until("the apply question", 5*time.Second, has("Apply run "+ui.Short(fixRun.ID)+"? [y/N]")))
	u.press("y")
	d.save("after-apply", u.until("the merge", 15*time.Second, func(v string) bool {
		return strings.HasPrefix(u.m.Status(), "Applied "+fixRun.Branch) && strings.Contains(v, "(no changes against HEAD: the branch is merged or empty)")
	}))
	if b, err := os.ReadFile(filepath.Join(e.Repos["puzzle"], "src", "fix.txt")); err != nil || !strings.Contains(string(b), "edited by fake") {
		t.Fatalf("apply did not merge: %v", err)
	}
	u.press("esc", "esc", "esc")
	u.until("the overview from the inbox", 10*time.Second, has("AGENTS (5)"))

	// Digest: list, then mark read.
	u.press("d")
	d.save("digest", u.untilFresh("the digest", 10*time.Second, has("DIGEST · ", "HANDLED  AGENT")))
	u.press("m")
	d.save("digest-read", u.until("mark read", 10*time.Second, func(v string) bool {
		return strings.HasPrefix(u.m.Status(), "Marked ") && strings.Contains(v, "Nothing new since the read cursor.")
	}))
	u.press("esc")

	// watch: the summary and the event list, from the real binary.
	out, err := e.CLI("", "watch", "--once", "--last", "5")
	if err != nil || !strings.Contains(out, "   lab ") || !strings.Contains(out, "dead letters 1") {
		t.Fatalf("watch --once: %v\n%s", err, out)
	}
	d.save("watch-once", strings.TrimRight(out, "\n"))
	out, err = e.CLI("", "watch", "--events", "--once", "--last", "12")
	if err != nil || len(strings.Split(strings.TrimSpace(out), "\n")) != 12 {
		t.Fatalf("watch --events --once: %v\n%s", err, out)
	}
	d.save("watch-events-once", strings.TrimRight(out, "\n"))

	// The real binary on a terminal (80x24): it draws the home screen of
	// this daemon and quits on q with exit 0.
	raw, took, err := runInSilentPTY(t, e.env, "q", 2500*time.Millisecond, 10*time.Second, "ui")
	plain := ansi.Strip(raw)
	t.Logf("real smif ui on a PTY: exit %v after %v, %d bytes", err, took.Round(time.Millisecond), len(raw))
	if err != nil || !strings.Contains(plain, "DONE TODAY") || !strings.Contains(plain, "DEAD · 1") || strings.Contains(raw, "\x1b]11;?") {
		t.Fatalf("real smif ui: %v\n%q", err, plain)
	}

	// Daemon down: the UI keeps the last data and says so; it recovers.
	e.Stop()
	u.press("r")
	d.save("daemon-down", u.until("the down notice", 10*time.Second, has("DAEMON DOWN", "showing stale data")))
	e.Start()
	u.press("r")
	u.until("the recovery", 15*time.Second, func(v string) bool { return !u.m.Down() && strings.Contains(v, "AGENTS (5)") })
}
