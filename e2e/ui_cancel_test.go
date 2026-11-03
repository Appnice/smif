package e2e

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/ui"
)

// panel returns the lines of one overview panel: its title line up to
// the next blank line.
func panel(v, name string) string {
	var out []string
	in := false
	for _, l := range strings.Split(v, "\n") {
		t := strings.TrimLeft(l, " ▶")
		switch {
		case strings.HasPrefix(t, name+" ("):
			in = true
		case in && strings.TrimSpace(l) == "":
			return strings.Join(out, "\n")
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// UI-2b: the human cancels a RUNNING row from the overview (c, then y).
// message.cancel only starts the stop ("cancelling"); the daemon stops the
// worker, the message goes to rejected, the run row leaves RUNNING and the
// status line names that result, not only "sent".
func TestUICancelRunningLive(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability"}, AgentSpec{ID: "cafe"})
	e.Start()

	long := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "report.build",
		Subject: "Build the long report", Body: "This takes a while.\n#fake:sleep=60"})
	e.WaitState(long.ID, 15*time.Second, "running")
	var runID string
	var pid int
	// The fake records its call once it has read its input; wait for
	// that too, or a fast cancel stops it before it writes the record.
	e.WaitFor("a live run", 10*time.Second, func() bool {
		if len(e.WorkerCalls()) != 1 {
			return false
		}
		for _, r := range e.Runs(long.ID) {
			if r.State == "running" {
				runID, pid = r.ID, r.PID
				return true
			}
		}
		return false
	})

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{Dashboard: true}))
	v := u.until("the running row", 10*time.Second, func(v string) bool {
		return has("RUNNING (1)")(v) && strings.Contains(panel(v, "RUNNING"), ui.Short(runID))
	})
	t.Logf("overview with the run at 120x40:\n%s", v)

	u.press("tab")
	if u.m.Selected() != runID {
		t.Fatalf("cursor is on %q, want the run %s", u.m.Selected(), runID)
	}
	u.press("c")
	v = u.until("the cancel question", 5*time.Second, has("Cancel "+ui.Short(long.ID), "A running worker is stopped.", "y yes · any other key: no"))
	t.Logf("cancel confirmation on the RUNNING row:\n%s", v)
	if s := e.Message(long.ID).State; s != "running" {
		t.Fatalf("the question alone changed the item: %s", s)
	}

	start := time.Now()
	u.press("y")
	want := "Cancelled " + ui.Short(long.ID) + ": run " + ui.Short(runID) + " stopped (cancelled) and the item is rejected."
	u.until("the real cancel result", 15*time.Second, func(string) bool { return u.m.Status() == want })
	took := time.Since(start)
	t.Logf("status %v after y: %s", took.Round(time.Millisecond), u.m.Status())

	// The daemon state agrees with the status line.
	m := e.WaitState(long.ID, 15*time.Second-took, "rejected")
	if m.Reason != "cancelled" {
		t.Fatalf("reason = %q, want cancelled", m.Reason)
	}
	if took > 15*time.Second {
		t.Fatalf("the message left running after %v, want under 15 s", took)
	}
	rs := e.Runs(long.ID)
	if len(rs) != 1 || rs[0].ID != runID || rs[0].State != "cancelled" || rs[0].EndedAt == nil {
		t.Fatalf("runs after the cancel: %+v", rs)
	}
	if d := rs[0].EndedAt.Sub(rs[0].StartedAt); d >= 60*time.Second {
		t.Fatalf("the run lasted %v: the cancel did not stop the 60 s sleep", d)
	}
	if pid > 0 {
		e.WaitFor("the worker process gone", 5*time.Second, func() bool {
			return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		})
	}
	if n := len(e.WorkerCalls()); n != 1 {
		t.Fatalf("worker calls = %d, want 1 (no restart after the cancel)", n)
	}

	// The RUNNING panel no longer lists the run.
	v = u.until("the run gone from RUNNING", 10*time.Second, func(v string) bool {
		return has("RUNNING (0)")(v) && !strings.Contains(panel(v, "RUNNING"), ui.Short(runID))
	})
	t.Logf("RUNNING panel after the cancel:\n%s", panel(v, "RUNNING"))
	t.Logf("overview after the cancel at 120x40:\n%s", v)
	if !strings.Contains(v, want) {
		t.Fatalf("the status line is not on screen:\n%s", v)
	}
	if u.m.Down() {
		t.Fatal("the UI reports the daemon down")
	}
}
