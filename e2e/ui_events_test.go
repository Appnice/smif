package e2e

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/ui"
)

// eventLine reports whether one line of v has every sub.
func eventLine(v string, subs ...string) bool {
	for _, l := range strings.Split(v, "\n") {
		if has(subs...)(l) {
			return true
		}
	}
	return false
}

// UI-4: the EVENTS panel follows the audit table live (audit.since, human
// only, ADR-0008); enter on an event opens its thread; the digest screen
// lists unread handled items and marks them read (digest.get advance,
// bounded by until).
func TestUIEventsDigestLive(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe"})
	e.Start()
	first := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "before the UI"})
	e.WaitState(first.ID, 15*time.Second, "done")

	// audit.since is for the human only (D4) and pages with a cursor.
	ac := e.AgentClient("cafe")
	defer ac.Close()
	if err := ac.Call("audit.since", api.AuditParams{}, nil, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("audit.since by an agent: %v", err)
	}
	var pg api.AuditPage
	e.Call("audit.since", api.AuditParams{After: 0, Limit: 2}, &pg)
	if len(pg.Events) != 2 || !pg.More || pg.Cursor != pg.Events[1].Seq || pg.Head <= pg.Cursor {
		t.Fatalf("page of 2: %d events, more %v, cursor %d, head %d", len(pg.Events), pg.More, pg.Cursor, pg.Head)
	}
	var end api.AuditPage
	e.Call("audit.since", api.AuditParams{FromEnd: true}, &end)
	if len(end.Events) != 0 || end.Cursor != end.Head || end.Head < pg.Head {
		t.Fatalf("from the end: %d events, cursor %d, head %d", len(end.Events), end.Cursor, end.Head)
	}

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{Dashboard: true}))
	u.until("the overview with events", 10*time.Second, has("EVENTS (", "run.started"))
	u.press("tab", "tab", "tab", "tab", "tab") // focus EVENTS

	// A message sent while the UI runs shows up in the feed by itself.
	second := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y",
		Subject: "while the UI watches", Body: "#fake:cost=0.01"})
	e.WaitState(second.ID, 15*time.Second, "done")
	short := ui.Short(second.ID)
	v := u.until("the live events of the second message", 10*time.Second, func(v string) bool {
		return eventLine(v, short, "message.accepted", "tell x.y from observability") && eventLine(v, short, "run.started") &&
			eventLine(v, short, "message.transition", "running→done")
	})
	t.Logf("live EVENTS panel at 120x40:\n%s", v)

	// enter on the newest event opens the thread of its message.
	sel := u.m.Selected()
	if sel == "" {
		t.Fatal("no event under the cursor")
	}
	u.press("enter")
	u.until("the thread of the event", 10*time.Second, func(v string) bool { return u.m.ItemOpen() == sel && strings.Contains(v, "THREAD ") })
	u.press("esc")

	// The digest: both handled items, then mark read.
	u.press("d")
	v = u.until("the digest", 10*time.Second, has("DIGEST · 2 unread handled items", ui.Short(first.ID), short))
	t.Logf("digest screen at 120x40:\n%s", v)
	u.press("m")
	u.until("mark read", 10*time.Second, func(v string) bool {
		return strings.HasPrefix(u.m.Status(), "Marked 2 items read") && strings.Contains(v, "Nothing new since the read cursor.")
	})
	t.Logf("status after mark read: %s", u.m.Status())
	if out, err := e.CLI("", "digest", "--peek"); err != nil || !strings.Contains(out, "nothing new") {
		t.Fatalf("digest after mark read: %v\n%s", err, out)
	}
	u.press("esc")
	u.until("the digest.read event", 10*time.Second, func(v string) bool { return eventLine(v, "digest.read", "cafe") })

	// until bounds the advance: an item handled after the peek stays unread.
	third := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "third"})
	e.WaitState(third.ID, 15*time.Second, "done")
	var peek api.DigestResult
	e.Call("digest.get", api.DigestParams{Agent: "cafe"}, &peek)
	if len(peek.Items) != 1 || peek.Items[0].MessageID != third.ID {
		t.Fatalf("peek: %+v", peek.Items)
	}
	fourth := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "fourth"})
	e.WaitState(fourth.ID, 15*time.Second, "done")
	var marked api.DigestResult
	e.Call("digest.get", api.DigestParams{Agent: "cafe", Advance: true, Until: peek.Cursor}, &marked)
	if len(marked.Items) != 1 || marked.Items[0].MessageID != third.ID {
		t.Fatalf("advance with until: %+v", marked.Items)
	}
	var rest api.DigestResult
	e.Call("digest.get", api.DigestParams{Agent: "cafe"}, &rest)
	if len(rest.Items) != 1 || rest.Items[0].MessageID != fourth.ID {
		t.Fatalf("after the bounded advance: %+v (want only the fourth)", rest.Items)
	}
	if u.m.Down() {
		t.Fatal("the UI reports the daemon down")
	}
}

// lineReader collects the stdout lines of a child process.
type lineReader struct {
	lines []string
	ch    chan string
}

func watchLines(t *testing.T, cmd *exec.Cmd) *lineReader {
	t.Helper()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	r := &lineReader{ch: make(chan string, 1024)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			r.ch <- sc.Text()
		}
		close(r.ch)
	}()
	return r
}

// wait reads lines until n lines (counted from the start) match subs.
func (r *lineReader) wait(t *testing.T, what string, n int, timeout time.Duration, subs ...string) {
	t.Helper()
	count := func() int {
		c := 0
		for _, l := range r.lines {
			if has(subs...)(l) {
				c++
			}
		}
		return c
	}
	deadline := time.After(timeout)
	for count() < n {
		select {
		case l, ok := <-r.ch:
			if !ok {
				t.Fatalf("watch ended while waiting for %s:\n%s", what, strings.Join(r.lines, "\n"))
			}
			r.lines = append(r.lines, l)
		case <-deadline:
			t.Fatalf("timed out waiting for %s:\n%s", what, strings.Join(r.lines, "\n"))
		}
	}
}

// UI-5: `mailroom watch --events` starts while the daemon is down, says
// so, streams one line per audit event once the daemon is up (events of a
// sent message appear), keeps its cursor over a daemon restart and exits
// 0 on SIGINT. `mailroom watch --once` prints the summary.
func TestWatchEventsLive(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe"})
	cmd := exec.Command(mailroomBin, "watch", "--events", "--interval", "200ms", "--last", "0")
	cmd.Env = e.env
	r := watchLines(t, cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	r.wait(t, "the down notice", 1, 10*time.Second, "daemon down:", "retrying every 200ms")
	e.Start()
	r.wait(t, "the up notice", 1, 10*time.Second, "daemon is up again")

	m := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "watched"})
	e.WaitState(m.ID, 15*time.Second, "done")
	short := ui.Short(m.ID)
	r.wait(t, "message.accepted", 1, 10*time.Second, "message.accepted cafe msg "+short, "tell x.y from observability")
	r.wait(t, "run.started", 1, 10*time.Second, "run.started cafe msg "+short)
	r.wait(t, "running→done", 1, 10*time.Second, "message.transition cafe msg "+short, "running→done")

	// A daemon restart: down, up, and the events made while it was down
	// or starting are not skipped (the cursor is in the watcher).
	e.Stop()
	r.wait(t, "the second down notice", 2, 15*time.Second, "daemon down:")
	e.Start()
	m2 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "after restart"})
	e.WaitState(m2.ID, 15*time.Second, "done")
	r.wait(t, "the second up notice", 2, 10*time.Second, "daemon is up again")
	r.wait(t, "events after the restart", 1, 10*time.Second, "message.transition cafe msg "+ui.Short(m2.ID), "running→done")
	// Every line after the up notice is one event: "HH:MM:SS #seq event".
	var seqs []string
	for _, l := range r.lines {
		if f := strings.Fields(l); len(f) >= 3 && strings.HasPrefix(f[1], "#") {
			seqs = append(seqs, f[1])
		}
	}
	seen := map[string]bool{}
	for _, s := range seqs {
		if seen[s] {
			t.Fatalf("event %s printed twice:\n%s", s, strings.Join(r.lines, "\n"))
		}
		seen[s] = true
	}
	t.Logf("watch --events output (%d lines):\n%s", len(r.lines), strings.Join(r.lines, "\n"))

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("watch exit after SIGINT: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not exit on SIGINT")
	}

	out, err := e.CLI("", "watch", "--once", "--last", "3")
	if err != nil || !strings.HasPrefix(out, "== ") || !strings.Contains(out, "   cafe ") ||
		strings.Count(out, "   event ") != 3 || !strings.Contains(out, "needs you 0 · dead letters 0") {
		t.Fatalf("watch --once: %v\n%s", err, out)
	}
	t.Logf("watch --once:\n%s", out)
	out, err = e.CLI("", "watch", "--events", "--once", "--last", "4")
	if err != nil || len(strings.Split(strings.TrimSpace(out), "\n")) != 4 {
		t.Fatalf("watch --events --once: %v\n%s", err, out)
	}
}
