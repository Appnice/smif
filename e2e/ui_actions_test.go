package e2e

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/ui"
)

// liveUI runs a ui.Model like the Bubble Tea loop does, without a
// terminal: commands run in goroutines (ticks included, so the model polls
// the daemon every second) and their messages reach Update on the test
// goroutine only.
type liveUI struct {
	t    *testing.T
	m    *ui.Model
	msgs chan tea.Msg
	done chan struct{}
}

func startUI(t *testing.T, m *ui.Model) *liveUI {
	u := &liveUI{t: t, m: m, msgs: make(chan tea.Msg, 64), done: make(chan struct{})}
	t.Cleanup(func() { close(u.done) })
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	u.exec(m.Init())
	return u
}

func (u *liveUI) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case u.msgs <- msg:
		case <-u.done:
		}
	}()
}

func (u *liveUI) handle(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil, tea.QuitMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			u.exec(c)
		}
	default:
		_, c := u.m.Update(msg)
		u.exec(c)
	}
}

// press sends keys: "enter", "esc", "tab", "down" or literal text.
func (u *liveUI) press(keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEsc}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		default:
			msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		}
		u.handle(msg)
	}
}

func (u *liveUI) view() string { return ansi.Strip(u.m.Render()) }

// until handles messages until cond holds on the plain-text view.
func (u *liveUI) until(what string, timeout time.Duration, cond func(v string) bool) string {
	u.t.Helper()
	deadline := time.After(timeout)
	for {
		if v := u.view(); cond(v) {
			return v
		}
		select {
		case msg := <-u.msgs:
			u.handle(msg)
		case <-deadline:
			u.t.Fatalf("UI: timed out waiting for %s\nstatus: %q\n%s", what, u.m.Status(), u.view())
		}
	}
}

func has(subs ...string) func(string) bool {
	return func(v string) bool {
		for _, s := range subs {
			if !strings.Contains(v, s) {
				return false
			}
		}
		return true
	}
}

// UI-2: from the UI the human reads a needs_human item with its thread,
// replies inline (message.reply) and the worker continues the thread;
// cancel asks first (message.cancel); a dead letter is retried
// (message.retry). Every action shows its result and the screen refreshes.
func TestUIActionsLive(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe"},
		AgentSpec{ID: "web", Extra: "max_attempts = 1"})
	e.Start()

	ask := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "release.decide",
		Subject: "Ship 1.0.2 now or wait for review?", Body: "Store review passed.\n#fake:result=needs_human"})
	e.WaitState(ask.ID, 15*time.Second, "needs_human")
	other := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "app.rename",
		Subject: "Rename the app?", Body: "#fake:result=needs_human"})
	e.WaitState(other.ID, 15*time.Second, "needs_human")
	dead := e.Send("observability", "message.send", envelope.Draft{To: "web", Kind: "tell", Intent: "crash.report", Subject: "boom", Body: "#fake:exit=3"})
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	if n := len(e.Runs(dead.ID)); n != 1 {
		t.Fatalf("dead letter runs = %d, want 1", n)
	}

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{Dashboard: true}))
	u.until("the overview", 10*time.Second, has("NEEDS YOU (2)", "DEAD LETTERS (1)"))

	// Open the needs_human item: question, body, thread, run.
	u.press("tab", "tab")
	for i := 0; i < 2 && u.m.Selected() != ask.ID; i++ {
		u.press("down")
	}
	if u.m.Selected() != ask.ID {
		t.Fatalf("cursor never reached %s (at %q)", ask.ID, u.m.Selected())
	}
	u.press("enter")
	v := u.until("the item screen", 10*time.Second, has("NEEDS YOU · "+ask.ID, "QUESTION FROM THE WORKER",
		"fake needs a decision on Ship 1.0.2 now or wait for review?", "Store review passed.", "THREAD "+ask.ThreadID,
		"1 messages · 1 runs", "attempt 1  needs_human", "a reply · c cancel"))
	t.Logf("needs-you item at 120x40:\n%s", v)

	// Reply inline. The daemon gets message.reply; the worker resumes.
	u.press("a", "Wait for the review, then ship.")
	t.Logf("reply box at 120x40:\n%s", u.view())
	u.press("enter")
	u.until("the reply result", 10*time.Second, func(string) bool { return strings.HasPrefix(u.m.Status(), "Reply sent to "+ui.Short(ask.ID)) })
	t.Logf("status after the reply: %s", u.m.Status())
	e.WaitState(ask.ID, 15*time.Second, "done")
	e.WaitFor("continuation done", 15*time.Second, func() bool {
		for _, x := range e.Thread(ask.ThreadID).Messages {
			if x.Kind == "reply" && x.From.Agent == "human" && x.State == "done" {
				return true
			}
		}
		return false
	})
	last := e.WorkerCalls()[len(e.WorkerCalls())-1]
	if !strings.Contains(last.Stdin, "Wait for the review, then ship.") || argValue(last.Argv, "--resume") == "" {
		t.Fatalf("the continuation did not resume the worker with the answer:\n%v", last.Argv)
	}
	v = u.until("the continued thread", 10*time.Second, func(v string) bool {
		return has("MESSAGE · "+ask.ID, "2 messages · 2 runs", "human → cafe  reply release.decide  done")(v) && !strings.Contains(v, "a reply")
	})
	t.Logf("item after the reply (thread continued):\n%s", v)

	// Cancel the other item: n keeps it, y cancels it.
	u.press("esc")
	u.until("one item left", 10*time.Second, has("NEEDS YOU (1)", "> "+ui.Short(other.ID)))
	u.press("c")
	v = u.until("the cancel question", 5*time.Second, has("Cancel "+ui.Short(other.ID), "y yes · any other key: no"))
	t.Logf("cancel confirmation:\n%s", v)
	u.press("n")
	if u.m.Status() != "Not done: cancel "+ui.Short(other.ID)+"." {
		t.Fatalf("status after n: %q", u.m.Status())
	}
	time.Sleep(200 * time.Millisecond)
	if s := e.Message(other.ID).State; s != "needs_human" {
		t.Fatalf("n changed the item: %s", s)
	}
	u.press("c", "y")
	u.until("the cancel result", 10*time.Second, func(string) bool { return u.m.Status() == "Cancelled "+ui.Short(other.ID)+": the item is rejected." })
	e.WaitState(other.ID, 5*time.Second, "rejected")
	u.until("needs you empty", 10*time.Second, has("NEEDS YOU (0)"))

	// Retry the dead letter from the DEAD LETTERS panel.
	u.press("tab", "t")
	v = u.until("the retry question", 5*time.Second, has("Retry dead letter "+ui.Short(dead.ID)))
	t.Logf("retry confirmation:\n%s", v)
	u.press("y")
	u.until("the retry result", 10*time.Second, func(string) bool {
		return u.m.Status() == "Retried "+ui.Short(dead.ID)+": it is back in the queue."
	})
	e.WaitFor("a second run of the dead letter", 15*time.Second, func() bool { return len(e.Runs(dead.ID)) == 2 })
	// exit=3 fails again: it lands in the DLQ again with a fresh attempt.
	m := e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	if m.Attempt != 1 {
		t.Fatalf("attempt after the retry = %d, want 1 (reset)", m.Attempt)
	}
	v = u.until("the retried dead letter", 10*time.Second, has("DEAD LETTERS (1)", "dead letters 1"))
	t.Logf("overview at the end:\n%s", v)
	if u.m.Down() {
		t.Fatal("the UI reports the daemon down")
	}
}
