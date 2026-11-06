package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
)

// fakeAudit serves audit.since from rows with the daemon's semantics.
type fakeAudit struct {
	rows  []api.AuditEvent
	calls []api.AuditParams
	err   error
}

func (f *fakeAudit) add(n int) {
	for i := 0; i < n; i++ {
		seq := int64(len(f.rows) + 1)
		if len(f.rows) > 0 {
			seq = f.rows[len(f.rows)-1].Seq + 1
		}
		f.rows = append(f.rows, api.AuditEvent{Seq: seq, TS: t0, Event: "message.transition", Agent: "cafe",
			Detail: map[string]any{"from_state": "queued", "to_state": "running"}})
	}
}

func (f *fakeAudit) call(method string, params, out any) error {
	if method != "audit.since" {
		return fmt.Errorf("unexpected %s", method)
	}
	if f.err != nil {
		return f.err
	}
	p := params.(api.AuditParams)
	f.calls = append(f.calls, p)
	pg := out.(*api.AuditPage)
	var head int64
	if n := len(f.rows); n > 0 {
		head = f.rows[n-1].Seq
	}
	pg.Head, pg.Cursor, pg.More, pg.Events = head, p.After, false, nil
	if p.FromEnd {
		n := min(p.Limit, len(f.rows))
		pg.Events, pg.Cursor = append(pg.Events, f.rows[len(f.rows)-n:]...), head
		return nil
	}
	for _, r := range f.rows {
		if r.Seq > p.After {
			pg.Events = append(pg.Events, r)
		}
	}
	if len(pg.Events) > p.Limit {
		pg.Events, pg.More = pg.Events[:p.Limit], true
	}
	if n := len(pg.Events); n > 0 {
		pg.Cursor = pg.Events[n-1].Seq
	}
	return nil
}

func TestEventFeedPolls(t *testing.T) {
	a := &fakeAudit{}
	a.add(10)
	var f EventFeed
	evs, reset, err := f.Poll(a.call, 3)
	if err != nil || reset || len(evs) != 3 || evs[0].Seq != 8 || evs[2].Seq != 10 || f.Cursor() != 10 {
		t.Fatalf("first poll: %d events, reset %v, cursor %d, err %v", len(evs), reset, f.Cursor(), err)
	}
	if !a.calls[0].FromEnd || a.calls[0].Limit != 3 {
		t.Fatalf("first call: %+v", a.calls[0])
	}
	// Nothing new: one call, nothing returned, the cursor stays.
	if evs, _, err := f.Poll(a.call, 3); err != nil || len(evs) != 0 || f.Cursor() != 10 {
		t.Fatalf("idle poll: %d %v cursor %d", len(evs), err, f.Cursor())
	}
	// 1203 new rows: three pages in one poll, oldest first, none lost.
	a.add(1203)
	evs, _, err = f.Poll(a.call, 3)
	if err != nil || len(evs) != 1203 || evs[0].Seq != 11 || evs[1202].Seq != 1213 || f.Cursor() != 1213 {
		t.Fatalf("paged poll: %d events, cursor %d, err %v", len(evs), f.Cursor(), err)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq != evs[i-1].Seq+1 {
			t.Fatalf("gap at %d: %d after %d", i, evs[i].Seq, evs[i-1].Seq)
		}
	}
	// A new database (head behind the cursor): the feed starts again.
	a.rows = nil
	a.add(5)
	evs, reset, err = f.Poll(a.call, 3)
	if err != nil || !reset || len(evs) != 3 || evs[2].Seq != 5 || f.Cursor() != 5 {
		t.Fatalf("reset poll: %d events, reset %v, cursor %d, err %v", len(evs), reset, f.Cursor(), err)
	}
}

func TestFormatEvent(t *testing.T) {
	evs := sampleEvents()
	want := []string{
		"13:05:05 #40 message.accepted cafe msg 00000NEED1 ask store.release from web hop=0 origin=interactive",
		`13:06:05 #41 message.transition cafe msg 00000NEED1 running→needs_human human_question="Ship 1.0.2 now or wait for review?"`,
		"13:09:00 #42 run.started cafe msg 000000RUN1 run 00AAAAAAAA attempt=1 handler=worker profile=triage",
	}
	for i, ev := range evs {
		if got := FormatEvent(ev); got != want[i] {
			t.Errorf("event %d:\n got %s\nwant %s", i, got, want[i])
		}
	}
	long := api.AuditEvent{Seq: 7, TS: t0, Event: "notify.needs_human", Detail: map[string]any{"summary": strings.Repeat("x", 200) + "\nnext",
		"cost_usd": 0.0123, "empty": "", "cursor": float64(1762090205000)}}
	got := FormatEvent(long)
	if !strings.Contains(got, "cost_usd=0.0123 cursor=1762090205000 summary=xxx") || strings.Contains(got, "empty") ||
		strings.Contains(got, "\n") || !strings.HasSuffix(got, "x…") {
		t.Fatalf("long event: %s", got)
	}
}

// eventCaller serves agent.status, thread.list and audit.since.
type eventCaller struct {
	audit *fakeAudit
}

func (c *eventCaller) Call(method string, params, out any, timeout time.Duration) error {
	if method == "audit.since" {
		return c.audit.call(method, params, out)
	}
	return nil
}

func (c *eventCaller) Close() error { return nil }

func TestDaemonSourceKeepsEventRing(t *testing.T) {
	a := &fakeAudit{}
	a.add(250)
	src := &DaemonSource{Dial: func() (Caller, error) { return &eventCaller{audit: a}, nil }}
	s := src.Fetch(context.Background())
	if s.Err != nil || len(s.Events) != EventKeep || s.Events[EventKeep-1].Seq != 250 {
		t.Fatalf("first fetch: err %v, %d events", s.Err, len(s.Events))
	}
	a.add(3)
	s = src.Fetch(context.Background())
	if len(s.Events) != EventKeep || s.Events[0].Seq != 54 || s.Events[EventKeep-1].Seq != 253 {
		t.Fatalf("second fetch: %d events, %d..%d", len(s.Events), s.Events[0].Seq, s.Events[len(s.Events)-1].Seq)
	}
	// An older daemon without audit.since: the overview still works.
	a.err = &api.Error{Code: -32601, Message: "method not found: audit.since"}
	s = src.Fetch(context.Background())
	if s.Err != nil || s.EventsErr == nil || len(s.Events) != EventKeep {
		t.Fatalf("rpc error: err %v, events err %v, %d events", s.Err, s.EventsErr, len(s.Events))
	}
}

func TestEventsPanel(t *testing.T) {
	m, f := newInspModel(t)
	key(m, "shift+tab")
	if m.Focus() != PanelEvents || m.Selected() != "01JMSG0000000000000000RUN1" {
		t.Fatalf("focus %v, selected %q", m.Focus(), m.Selected())
	}
	golden(t, "events_focus_120x40.golden", view(m))
	// enter on an event with a message opens its thread.
	key(m, "enter")
	if m.ItemOpen() != "01JMSG0000000000000000RUN1" {
		t.Fatalf("item = %q", m.ItemOpen())
	}
	key(m, "esc")

	// New events arrive: a cursor below the top stays on its event.
	key(m, "j")
	if m.Selected() != "01JMSG000000000000000NEED1" {
		t.Fatalf("row 2: %q", m.Selected())
	}
	s := sample()
	s.Events = append(s.Events,
		api.AuditEvent{Seq: 43, TS: t0, Event: "run.finished", Agent: "cafe", RunID: runID, Detail: map[string]any{"state": "succeeded"}},
		api.AuditEvent{Seq: 44, TS: t0, Event: "config.reloaded", Detail: map[string]any{"agents": []any{"cafe", "web"}}})
	f.snaps = []Snapshot{s}
	run(m, m.fetch())
	if m.Selected() != "01JMSG000000000000000NEED1" {
		t.Fatalf("cursor moved off its event: %q", m.Selected())
	}
	out := view(m)
	if !strings.Contains(out, "EVENTS (5)") || !strings.Contains(out, "#44  config.reloaded") || !strings.Contains(out, "agents=\"[cafe web]\"") {
		t.Fatalf("new events not drawn:\n%s", out)
	}
	// An event without a message: nothing to open, the footer says so.
	key(m, "g", "enter")
	if m.ItemOpen() != "" || m.RunOpen() != "" || !strings.Contains(m.Status(), "#44 (config.reloaded) has no message or run") {
		t.Fatalf("config event: item %q run %q status %q", m.ItemOpen(), m.RunOpen(), m.Status())
	}
	// An event with only a run opens the run screen.
	key(m, "j", "enter")
	if m.RunOpen() != runID {
		t.Fatalf("run = %q, status %q", m.RunOpen(), m.Status())
	}
}

// fakeDigest is a digest.get with read cursors per agent.
type fakeDigest struct {
	*fakeInspector
	items  []api.DigestItem
	seqs   []int64
	cursor int64
	calls  []api.DigestParams

	// The history screen (history_test.go).
	historyErr, resumeErr error
	noHistory             bool
	searched, resumed     []string
	reopened              int
}

func (f *fakeDigest) Digest(ctx context.Context, p api.DigestParams) (*api.DigestResult, error) {
	f.calls = append(f.calls, p)
	res := &api.DigestResult{Agents: []string{"cafe", "web"}, Items: []api.DigestItem{}, Cursor: f.cursor}
	for i, it := range f.items {
		if f.seqs[i] > f.cursor && (p.Until == 0 || f.seqs[i] <= p.Until) {
			res.Items = append(res.Items, it)
			res.Cursor = max(res.Cursor, f.seqs[i])
		}
	}
	if p.Advance {
		f.cursor = res.Cursor
	}
	return res, nil
}

func TestDigestScreenMarksRead(t *testing.T) {
	_, insp := newInspModel(t)
	f := &fakeDigest{fakeInspector: insp, cursor: 40,
		items: []api.DigestItem{
			{MessageID: "01JMSG0000000000000000DONE", ThreadID: "01JMSG0000000000000000DONE", Agent: "cafe", From: "web", Kind: "tell",
				Intent: "x.y", State: "done", Summary: "fake handled x.y", Coalesced: 2, At: t0.Add(-30 * time.Minute)},
			{MessageID: "01JMSG000000000000000NEED1", ThreadID: "01JMSG000000000000000NEED1", Agent: "cafe", From: "web", Kind: "ask",
				Intent: "store.release", State: "needs_human", Subject: "Ship?", HumanQuestion: "Ship 1.0.2 now or wait for review?",
				At: t0.Add(-4 * time.Minute)},
		},
		seqs: []int64{45, 57}}
	m := New(f, Options{Dashboard: true, Now: func() time.Time { return t0 }, Interval: time.Millisecond})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	run(m, m.Init())

	key(m, "d")
	if open, n := m.DigestOpen(); !open || n != 2 {
		t.Fatalf("digest open %v, %d items", open, n)
	}
	if p := f.calls[0]; p.Advance || p.Since != "last" {
		t.Fatalf("opening the digest moved the cursor: %+v", p)
	}
	golden(t, "digest_120x40.golden", view(m))

	// enter opens the thread; esc returns to the digest.
	key(m, "j", "enter")
	if m.ItemOpen() != "01JMSG000000000000000NEED1" {
		t.Fatalf("item = %q", m.ItemOpen())
	}
	key(m, "esc")

	// An item handled after the screen loaded stays unread.
	f.items = append(f.items, api.DigestItem{MessageID: "01JMSG0000000000000000LATE", Agent: "web", From: "cafe", Kind: "tell",
		Intent: "x.y", State: "done", Summary: "late", At: t0})
	f.seqs = append(f.seqs, 60)
	key(m, "m")
	last := f.calls[len(f.calls)-2] // the mark, then the reload
	if !last.Advance || last.Until != 57 {
		t.Fatalf("mark read call: %+v", last)
	}
	if !strings.Contains(m.Status(), "Marked 2 items read (read cursor 57).") {
		t.Fatalf("status %q", m.Status())
	}
	if open, n := m.DigestOpen(); !open || n != 1 {
		t.Fatalf("after mark read: open %v, %d items (want the late one)", open, n)
	}
	key(m, "m")
	key(m, "m")
	if open, n := m.DigestOpen(); !open || n != 0 || !strings.Contains(view(m), "Nothing new since the read cursor.") {
		t.Fatalf("after second mark: %d items\n%s", n, view(m))
	}
	if !strings.Contains(m.Status(), "Nothing unread to mark read.") {
		t.Fatalf("status %q", m.Status())
	}
	key(m, "esc")
	if open, _ := m.DigestOpen(); open {
		t.Fatal("esc did not close the digest")
	}
}
