package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// fakeActions is a source that also acts. It records every action.
type fakeActions struct {
	fakeSource
	threads  map[string]*api.ThreadView
	replies  []string
	cancels  []string
	retries  []string
	threadN  int
	replyErr error
	// cancelTo sets the message state that thread.get shows after a
	// cancel, like the daemon does for an item that is not running.
	cancelTo map[string]string
	// cancelRet sets the state that Cancel returns without a change in
	// the thread, so a test can tell the returned state from a thread read.
	cancelRet map[string]string
}

// stateOf is the state of message id in the fake threads.
func (f *fakeActions) stateOf(id string) string {
	for _, tv := range f.threads {
		for _, x := range tv.Messages {
			if x.ID == id {
				return x.State
			}
		}
	}
	return ""
}

// setState changes the state of message id in the fake threads.
func (f *fakeActions) setState(id, state string) {
	for _, tv := range f.threads {
		for i := range tv.Messages {
			if tv.Messages[i].ID == id {
				tv.Messages[i].State = state
			}
		}
	}
}

func (f *fakeActions) Thread(ctx context.Context, id string) (*api.ThreadView, error) {
	f.threadN++
	if tv, ok := f.threads[id]; ok {
		return tv, nil
	}
	return nil, &api.Error{Code: api.CodeNotFound, Message: "thread " + id + " not found"}
}

func (f *fakeActions) Reply(ctx context.Context, id, text string) (*api.SendResult, error) {
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	f.replies = append(f.replies, id+": "+text)
	return &api.SendResult{ID: "01JMSG00000000000000REPLY1", ThreadID: "01JTHREAD0000000000000001", State: "queued"}, nil
}

// Cancel answers like the daemon: cancelling for a running item, the new
// state for an item it rejects at once.
func (f *fakeActions) Cancel(ctx context.Context, id string) (*api.CancelResult, error) {
	f.cancels = append(f.cancels, id)
	if st, ok := f.cancelRet[id]; ok {
		return &api.CancelResult{ID: id, State: st}, nil
	}
	if f.stateOf(id) == "running" {
		return &api.CancelResult{ID: id, State: api.CancelCancelling}, nil
	}
	st, ok := f.cancelTo[id]
	if ok {
		f.setState(id, st)
	}
	return &api.CancelResult{ID: id, State: st}, nil
}

func (f *fakeActions) Retry(ctx context.Context, id string) error {
	f.retries = append(f.retries, id)
	return nil
}

func sampleThreads() map[string]*api.ThreadView {
	s := sample()
	need := s.NeedsYou[0]
	need.ThreadID = "01JTHREAD0000000000000001"
	need.Body = "Store review passed for 1.0.2. Decide if we ship now.\n#fake:result=needs_human"
	need.Priority = "normal"
	need.Attempt = 1
	need.Handler = "worker"
	need.CreatedAt = t0.Add(-5 * time.Minute)
	ended := t0.Add(-4 * time.Minute)
	res, _ := json.Marshal(map[string]any{"status": "needs_human", "summary": "fake handled store.release"})
	dead := s.DLQ[0]
	dead.ThreadID = "01JTHREAD0000000000000002"
	dead.Body = "panic in onboarding at step 3"
	dead.Attempt = 2
	dead.Handler = "worker"
	dead.CreatedAt = t0.Add(-12 * time.Minute)
	return map[string]*api.ThreadView{
		need.ID: {ThreadID: need.ThreadID, Messages: []api.MessageView{need}, Runs: []api.RunView{{
			ID: "01JRUN000000000000BBBBBBBB", MessageID: need.ID, Agent: "cafe", Handler: "worker", Profile: "triage", Attempt: 1,
			State: "done", CostUSD: 0.25, Result: res, StartedAt: t0.Add(-5 * time.Minute), EndedAt: &ended}}},
		dead.ID: {ThreadID: dead.ThreadID, Messages: []api.MessageView{dead}, Runs: []api.RunView{
			{ID: "01JRUN000000000000CCCCCCC1", MessageID: dead.ID, Agent: "observability", Handler: "worker", Attempt: 1,
				State: "failed", Error: "timeout after 600s", StartedAt: t0.Add(-12 * time.Minute), EndedAt: &ended},
			{ID: "01JRUN000000000000CCCCCCC2", MessageID: dead.ID, Agent: "observability", Handler: "worker", Attempt: 2,
				State: "failed", Error: "timeout after 600s", StartedAt: t0.Add(-11 * time.Minute), EndedAt: &ended}}},
	}
}

func newActModel(t *testing.T) (*Model, *fakeActions) {
	t.Helper()
	f := &fakeActions{fakeSource: fakeSource{snaps: []Snapshot{sample()}}, threads: sampleThreads()}
	m := New(f, Options{Dashboard: true, Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	return m, f
}

// run executes a command like the Bubble Tea loop does and feeds every
// message back, except ticks (the test drives polls itself).
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil, tickMsg, tea.QuitMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	default:
		_, next := m.Update(msg)
		run(m, next)
	}
}

func key(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEsc}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "ctrl+s":
			msg = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
		case "ctrl+o":
			msg = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
		case "ctrl+y":
			msg = tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
		case "ctrl+v":
			msg = tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}
		case "ctrl+p":
			msg = tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
		case "ctrl+f":
			msg = tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}
		case "ctrl+q":
			msg = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
		default:
			// Literal text is typed one key at a time, as a terminal sends it.
			for _, r := range k {
				_, cmd := m.Update(keyText(string(r), false))
				run(m, cmd)
			}
			continue
		}
		_, cmd := m.Update(msg)
		run(m, cmd)
	}
}

// keyText is a printable key, or with alt the alt+key chord.
func keyText(s string, alt bool) tea.KeyPressMsg {
	k := tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	if len([]rune(s)) > 1 {
		k.Code = tea.KeyExtended
	}
	if alt {
		k.Mod, k.Text = tea.ModAlt, ""
	}
	return k
}

func checkSize(t *testing.T, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Errorf("view has %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Errorf("line %d is %d cells wide: %q", i, ansi.StringWidth(l), l)
		}
	}
}

func TestNeedsYouItemScreen(t *testing.T) {
	m, f := newActModel(t)
	key(m, "tab", "tab")
	if m.Focus() != PanelNeedsYou {
		t.Fatalf("focus = %v", m.Focus())
	}
	if !strings.Contains(view(m), "enter open · a reply · c cancel") {
		t.Fatalf("overview footer lacks the NEEDS YOU keys:\n%s", view(m))
	}
	key(m, "enter")
	if m.ItemOpen() != "01JMSG000000000000000NEED1" || f.threadN != 1 {
		t.Fatalf("item = %q, thread loads = %d", m.ItemOpen(), f.threadN)
	}
	out := view(m)
	for _, want := range []string{
		"NEEDS YOU · 01JMSG000000000000000NEED1", "web → cafe · ask store.release · priority normal · attempt 1 · handler worker · updated 4m00s ago",
		"subject: Ship?", "QUESTION FROM THE WORKER", "Ship 1.0.2 now or wait for review?",
		"MESSAGE BODY", "Store review passed for 1.0.2. Decide if we ship now.",
		"THREAD 01JTHREAD0000000000000001 · 1 messages · 1 runs", "▸ ", "RUNS",
		"00BBBBBBBB  msg 00000NEED1  worker/triage  attempt 1  done  1m00s  $0.25  needs_human: fake handled store.release",
		"a reply · c cancel · tab/enter run · ↑/↓ scroll · r refresh · esc back · ? help · q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("item view lacks %q", want)
		}
	}
	checkSize(t, out, 120, 40)
	golden(t, "needs_you_item_120x40.golden", out)

	// Every poll also refreshes the thread.
	_, cmd := m.Update(tickMsg(t0))
	run(m, cmd)
	if f.threadN != 2 {
		t.Fatalf("thread loads after a poll = %d, want 2", f.threadN)
	}
	key(m, "esc")
	if m.ItemOpen() != "" || !strings.Contains(view(m), "AGENTS (3)") {
		t.Fatalf("esc did not go back:\n%s", view(m))
	}
}

func TestReplyFromTheUI(t *testing.T) {
	m, f := newActModel(t)
	key(m, "tab", "tab", "a") // a on the overview opens the item with the box
	if m.ItemOpen() == "" || !m.item.typing {
		t.Fatal("a did not open the reply box")
	}
	key(m, "enter")
	if len(f.replies) != 0 || !strings.Contains(m.Status(), "The reply is empty") {
		t.Fatalf("empty reply: %v %q", f.replies, m.Status())
	}
	// q and c are text in the box, not commands.
	key(m, "Wait", "space", "for", "space", "the", "space", "review", "space", "q", "c", "backspace", "backspace")
	out := view(m)
	if !strings.Contains(out, "REPLY to cafe on 00000NEED1") || !strings.Contains(out, "> Wait for the review") ||
		!strings.Contains(out, "enter send · alt+enter new line") {
		t.Fatalf("reply box:\n%s", out)
	}
	checkSize(t, out, 120, 40)
	golden(t, "reply_box_120x40.golden", out)

	f.replyErr = &api.Error{Code: api.CodeRejected, Message: "message 01JMSG000000000000000NEED1 is done, not needs_human"}
	key(m, "enter")
	if !strings.Contains(m.Status(), "Reply 00000NEED1 failed: message 01JMSG000000000000000NEED1 is done, not needs_human") ||
		!m.item.typing || m.item.draft.Value() != "Wait for the review " {
		t.Fatalf("after a failed reply: %q typing=%v draft=%q", m.Status(), m.item.typing, m.item.draft.Value())
	}
	f.replyErr = nil
	calls := f.calls
	key(m, "enter")
	if len(f.replies) != 1 || f.replies[0] != "01JMSG000000000000000NEED1: Wait for the review" {
		t.Fatalf("replies = %v", f.replies)
	}
	if m.Status() != "Reply sent to 00000NEED1. The thread continues as 0000REPLY1 (queued)." || m.item.typing || m.item.draft.Value() != "" {
		t.Fatalf("after the reply: %q typing=%v", m.Status(), m.item.typing)
	}
	if f.calls != calls+1 {
		t.Fatalf("no refresh after the reply: %d polls", f.calls-calls)
	}
	if !strings.Contains(view(m), "Reply sent to 00000NEED1.") {
		t.Fatalf("status not on screen:\n%s", view(m))
	}
}

func TestCancelNeedsConfirmation(t *testing.T) {
	m, f := newActModel(t)
	key(m, "tab", "tab", "c")
	out := view(m)
	if !strings.Contains(out, `Cancel 00000NEED1 (to cafe, ask store.release, "Ship?")?`) || !strings.Contains(out, "y yes · any other key: no") {
		t.Fatalf("confirmation:\n%s", out)
	}
	checkSize(t, out, 120, 40)
	golden(t, "confirm_cancel_120x40.golden", out)
	key(m, "n")
	if len(f.cancels) != 0 || m.Status() != "Not done: cancel 00000NEED1." {
		t.Fatalf("n cancelled: %v %q", f.cancels, m.Status())
	}
	f.cancelTo = map[string]string{"01JMSG000000000000000NEED1": "rejected"}
	key(m, "c", "y")
	if len(f.cancels) != 1 || f.cancels[0] != "01JMSG000000000000000NEED1" || m.Status() != "Cancelled 00000NEED1: the item is rejected." {
		t.Fatalf("y: %v %q", f.cancels, m.Status())
	}
	f.setState("01JMSG000000000000000NEED1", "needs_human") // the rest of the test cancels it again
	// Cancel on a running run cancels its message.
	key(m, "shift+tab", "c", "y")
	if len(f.cancels) != 2 || f.cancels[1] != "01JMSG0000000000000000RUN1" {
		t.Fatalf("run cancel: %v", f.cancels)
	}
	// Cancel from the item screen, also behind the confirmation.
	key(m, "tab", "enter", "c")
	if !strings.Contains(view(m), "Cancel 00000NEED1") {
		t.Fatalf("item cancel has no confirmation:\n%s", view(m))
	}
	key(m, "esc")
	if len(f.cancels) != 2 || m.ItemOpen() == "" {
		t.Fatalf("esc on the confirmation: %v item=%q", f.cancels, m.ItemOpen())
	}
}

func TestDeadLetterRetry(t *testing.T) {
	m, f := newActModel(t)
	key(m, "tab", "tab", "tab", "enter")
	out := view(m)
	for _, want := range []string{"DEAD LETTER · 01JMSG0000000000000000DLQ1", "REASON", "timeout after 2 attempts",
		"panic in onboarding at step 3", "2 runs", "error: timeout after 600s", "t retry · tab/enter run · ↑/↓ scroll"} {
		if !strings.Contains(out, want) {
			t.Errorf("dead letter view lacks %q", want)
		}
	}
	if strings.Contains(out, "a reply") || strings.Contains(out, "c cancel") {
		t.Errorf("dead letter offers reply or cancel:\n%s", out)
	}
	checkSize(t, out, 120, 40)
	golden(t, "dead_letter_item_120x40.golden", out)
	key(m, "a")
	if m.item.typing || !strings.Contains(m.Status(), "Reply is possible only on a needs_human item; this one is dead_letter.") {
		t.Fatalf("a on a dead letter: %q", m.Status())
	}
	key(m, "t")
	if !strings.Contains(view(m), "Retry dead letter 000000DLQ1 (to observability, tell crash.report") {
		t.Fatalf("retry confirmation:\n%s", view(m))
	}
	key(m, "y")
	if len(f.retries) != 1 || f.retries[0] != "01JMSG0000000000000000DLQ1" || m.Status() != "Retried 000000DLQ1: it is back in the queue." {
		t.Fatalf("retry: %v %q", f.retries, m.Status())
	}
	// From the overview: t on the DLQ panel.
	key(m, "esc", "t", "y")
	if len(f.retries) != 2 {
		t.Fatalf("overview retry: %v", f.retries)
	}
}

func TestReadOnlySourceCannotAct(t *testing.T) {
	m := newModel(&fakeSource{snaps: []Snapshot{sample()}}, 120, 40)
	poll(t, m)
	key(m, "tab", "tab", "c", "y")
	if !strings.Contains(m.Status(), "no daemon connection") {
		t.Fatalf("status = %q", m.Status())
	}
	key(m, "enter")
	if !strings.Contains(view(m), "not available (no daemon connection)") {
		t.Fatalf("item without actions:\n%s", view(m))
	}
}

func TestLineInput(t *testing.T) {
	var in lineInput
	in.edit(keyText("hello world", false))
	in.edit(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if in.Value() != "hello " {
		t.Fatalf("ctrl+w: %q", in.Value())
	}
	in.edit(tea.KeyPressMsg{Code: tea.KeyHome})
	in.edit(keyText(">", false))
	in.edit(tea.KeyPressMsg{Code: tea.KeyEnd})
	in.edit(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	in.insert([]rune("x\r\ty\x07")) // a paste
	if in.Value() != ">hello \nx\n y" {
		t.Fatalf("value: %q", in.Value())
	}
	if got := ansi.Strip(in.view(40)); got != ">hello ↵x↵ y " {
		t.Fatalf("view: %q", got)
	}
	if in.edit(tea.KeyPressMsg{Code: tea.KeyEnter}) || in.edit(tea.KeyPressMsg{Code: tea.KeyEsc}) {
		t.Fatal("enter or esc taken by the input")
	}
	in.edit(tea.KeyPressMsg{Code: tea.KeyLeft})
	in.edit(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	in.edit(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	in.edit(tea.KeyPressMsg{Code: tea.KeyDelete})
	if in.Value() != "hello \nx\n " {
		t.Fatalf("after ctrl+k, delete: %q", in.Value())
	}
	long := lineInput{}
	long.insert([]rune(strings.Repeat("abcdefghij", 10)))
	if v := ansi.Strip(long.view(20)); ansi.StringWidth(v) > 20 || !strings.HasSuffix(v, "hij ") {
		t.Fatalf("long view shows %q", v)
	}
}

// recCaller records calls; it can fail with a transport or an RPC error.
type recCaller struct {
	methods []string
	params  []any
	err     error
	closed  bool
}

func (c *recCaller) Call(method string, params, out any, timeout time.Duration) error {
	c.methods = append(c.methods, method)
	c.params = append(c.params, params)
	if c.err != nil {
		return c.err
	}
	if r, ok := out.(*api.SendResult); ok {
		r.ID, r.State = "01JREPLY", "queued"
	}
	if r, ok := out.(*api.CancelResult); ok {
		r.ID, r.State = params.(api.IDParams).ID, api.CancelCancelling
	}
	if tv, ok := out.(*api.ThreadView); ok {
		tv.ThreadID = "T1"
		tv.Messages = []api.MessageView{{ID: "M1", From: envelope.From{Agent: "web"}}}
	}
	return nil
}

func (c *recCaller) Close() error { c.closed = true; return nil }

func TestDaemonSourceActions(t *testing.T) {
	var dials int
	var cur *recCaller
	src := &DaemonSource{Dial: func() (Caller, error) { dials++; cur = &recCaller{}; return cur, nil }}
	ctx := context.Background()
	tv, err := src.Thread(ctx, "M1")
	if err != nil || tv.ThreadID != "T1" {
		t.Fatalf("thread: %v %+v", err, tv)
	}
	res, err := src.Reply(ctx, "M1", "yes")
	if err != nil || res.ID != "01JREPLY" {
		t.Fatalf("reply: %v %+v", err, res)
	}
	if cr, err := src.Cancel(ctx, "M2"); err != nil || cr.ID != "M2" || cr.State != api.CancelCancelling {
		t.Fatalf("cancel: %v %+v", err, cr)
	}
	if err := src.Retry(ctx, "M3"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cur.methods, ","); got != "thread.get,message.reply,message.cancel,message.retry" || dials != 1 {
		t.Fatalf("methods = %s, dials = %d", got, dials)
	}
	if p := cur.params[1].(api.HumanReplyParams); p.MessageID != "M1" || p.Text != "yes" {
		t.Fatalf("reply params = %+v", p)
	}
	// An RPC error keeps the connection.
	cur.err = &api.Error{Code: api.CodeRejected, Message: "not a dead letter"}
	if err := src.Retry(ctx, "M3"); !api.IsCode(err, api.CodeRejected) || cur.closed {
		t.Fatalf("rpc error: %v closed=%v", err, cur.closed)
	}
	// A transport error drops it and is not sent again.
	cur.err = errors.New("broken pipe")
	old := cur
	if _, err := src.Cancel(ctx, "M2"); err == nil || !old.closed || len(old.methods) != 6 || dials != 1 {
		t.Fatalf("transport error: %v closed=%v calls=%d dials=%d", err, old.closed, len(old.methods), dials)
	}
	if _, err := src.Cancel(ctx, "M2"); err != nil || dials != 2 {
		t.Fatalf("redial: %v dials=%d", err, dials)
	}
}

// runningThread is the thread of the sample RUNNING row, in progress.
func runningThread() *api.ThreadView {
	msg := api.MessageView{ID: "01JMSG0000000000000000RUN1", ThreadID: "01JTHREAD0000000000000003", To: "cafe",
		Kind: "tell", Intent: "x.y", Subject: "Long job", State: "running", Attempt: 1, CreatedAt: t0.Add(-70 * time.Second)}
	return &api.ThreadView{ThreadID: msg.ThreadID, Messages: []api.MessageView{msg}, Runs: []api.RunView{{
		ID: "01JRUN000000000000AAAAAAAA", MessageID: msg.ID, Agent: "cafe", Handler: "worker", Attempt: 1,
		State: "running", StartedAt: t0.Add(-65 * time.Second)}}}
}

// A cancel on a running item first says that the run is stopping. The
// status names the real result only after a poll shows that the item left
// running.
func TestCancelRunningShowsRealResult(t *testing.T) {
	m, f := newActModel(t)
	tv := runningThread()
	f.threads[tv.Messages[0].ID] = tv
	key(m, "tab", "c")
	if !strings.Contains(view(m), "Cancel 000000RUN1") {
		t.Fatalf("no confirmation on the RUNNING row:\n%s", view(m))
	}
	key(m, "y")
	if len(f.cancels) != 1 || f.cancels[0] != tv.Messages[0].ID {
		t.Fatalf("cancels: %v", f.cancels)
	}
	if got := m.Status(); got != "Cancelling 000000RUN1: run 00AAAAAAAA is stopping; the item is still running." {
		t.Fatalf("status while running: %q", got)
	}
	// A poll while the run still runs keeps the status.
	run(m, func() tea.Msg { return SnapshotMsg(sample()) })
	if !strings.HasPrefix(m.Status(), "Cancelling 000000RUN1") {
		t.Fatalf("status after a poll: %q", m.Status())
	}
	// The daemon stops the run: the item goes to rejected.
	tv.Messages[0].State = "rejected"
	tv.Runs[0].State = "cancelled"
	run(m, func() tea.Msg { return SnapshotMsg(sample()) })
	want := "Cancelled 000000RUN1: run 00AAAAAAAA stopped (cancelled) and the item is rejected."
	if m.Status() != want || m.statusBad {
		t.Fatalf("status after the stop: %q (bad %v), want %q", m.Status(), m.statusBad, want)
	}
	if !strings.Contains(view(m), want) {
		t.Fatalf("result not on screen:\n%s", view(m))
	}
	if len(m.cancels) != 0 {
		t.Fatalf("the model still follows %d cancels", len(m.cancels))
	}
}

// A cancel that the daemon answers with "rejected" is final: the status
// tells it at once and the model follows no thread. The fake keeps the
// thread at needs_human, so a follow-up read would show a wrong result.
func TestCancelRejectedUsesReturnedState(t *testing.T) {
	m, f := newActModel(t)
	f.cancelRet = map[string]string{"01JMSG000000000000000NEED1": api.CancelRejected}
	key(m, "tab", "tab", "c", "y")
	if len(f.cancels) != 1 || f.cancels[0] != "01JMSG000000000000000NEED1" {
		t.Fatalf("cancels: %v", f.cancels)
	}
	if got := m.Status(); got != "Cancelled 00000NEED1: the item is rejected." || m.statusBad {
		t.Fatalf("status: %q (bad %v)", got, m.statusBad)
	}
	if len(m.cancels) != 0 {
		t.Fatalf("the model follows %d cancels after a rejected answer", len(m.cancels))
	}
	run(m, func() tea.Msg { return SnapshotMsg(sample()) })
	if got := m.Status(); got != "Cancelled 00000NEED1: the item is rejected." {
		t.Fatalf("status after a poll: %q", got)
	}
	// An answer without a state (an older daemon) falls back to the thread read.
	f.cancelRet = map[string]string{"01JMSG000000000000000NEED1": ""}
	key(m, "c", "y")
	if len(f.cancels) != 2 || m.Status() != "Cancel of 00000NEED1 came too late: run 00BBBBBBBB ended done and the item is needs_human." || !m.statusBad {
		t.Fatalf("no state: %v %q (bad %v)", f.cancels, m.Status(), m.statusBad)
	}
}

// When the item stays running past the wait, the status says so.
func TestCancelRunningTimesOut(t *testing.T) {
	now := t0
	f := &fakeActions{fakeSource: fakeSource{snaps: []Snapshot{sample()}}, threads: sampleThreads()}
	tv := runningThread()
	f.threads[tv.Messages[0].ID] = tv
	m := New(f, Options{Dashboard: true, Now: func() time.Time { return now }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())
	key(m, "tab", "c", "y")
	now = now.Add(cancelWaitMax)
	run(m, func() tea.Msg { return SnapshotMsg(sample()) })
	if got := m.Status(); got != "Cancel of 000000RUN1 sent, but after 30s the item is still running." || !m.statusBad {
		t.Fatalf("status after the wait: %q (bad %v)", got, m.statusBad)
	}
}

func TestCancelOutcome(t *testing.T) {
	r := &api.RunView{ID: "01JRUN000000000000AAAAAAAA", State: "succeeded"}
	for _, c := range []struct {
		state string
		run   *api.RunView
		want  string
	}{
		{"rejected", nil, "Cancelled 000000RUN1: the item is rejected."},
		{"rejected", &api.RunView{ID: r.ID, State: "done"}, "Cancelled 000000RUN1: the item is rejected."},
		{"rejected", &api.RunView{ID: r.ID, State: "cancelled"}, "Cancelled 000000RUN1: run 00AAAAAAAA stopped (cancelled) and the item is rejected."},
		{"done", r, "Cancel of 000000RUN1 came too late: run 00AAAAAAAA ended succeeded and the item is done."},
		{"needs_human", nil, "Cancel of 000000RUN1 did not apply: the item is needs_human."},
	} {
		if got := cancelOutcome("000000RUN1", c.state, c.run); got != c.want {
			t.Errorf("cancelOutcome(%s, %v) = %q, want %q", c.state, c.run, got, c.want)
		}
	}
}

// A paste goes into the reply box as text and never acts as keys
// elsewhere; a y/N question needs its own key press.
func TestPasteAndConfirm(t *testing.T) {
	m, f := newActModel(t)
	m.Update(tea.PasteMsg{Content: "ty"}) // on the overview: ignored
	if m.ItemOpen() != "" || len(f.retries) != 0 {
		t.Fatalf("a paste acted as keys: item %q retries %q", m.ItemOpen(), f.retries)
	}
	key(m, "tab", "tab", "enter", "a")
	m.Update(tea.PasteMsg{Content: "ship it"})
	key(m, "enter")
	if len(f.replies) != 1 || f.replies[0] != "01JMSG000000000000000NEED1: ship it" {
		t.Fatalf("replies = %q", f.replies)
	}

	m, f = newActModel(t)
	key(m, "tab", "tab", "tab", "t")
	if len(f.retries) != 0 || !strings.Contains(view(m), "Retry dead letter") {
		t.Fatalf("retry without a question:\n%s", view(m))
	}
	key(m, "y")
	if len(f.retries) != 1 {
		t.Fatalf("retries after y: %q", f.retries)
	}
}
