package store

import (
	"errors"
	"testing"
	"time"

	"github.com/appnice/smif/internal/envelope"
)

func TestRunsLifecycle(t *testing.T) {
	s := openTest(t)
	id := newMsg(t, s)
	r := &Run{ID: envelope.NewID(), MessageID: id, Agent: "b", Handler: "worker", Profile: "triage", Attempt: 1, Workspace: "/w", Branch: "smif/b/x", BaseSHA: "abc"}
	if err := s.Update(func(tx *Tx) error { return tx.InsertRun(r) }); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunProcess(r.ID, 123, 123, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunSession(r.ID, "sess"); err != nil {
		t.Fatal(err)
	}
	running, _ := s.Runs(`state=?`, RunRunning)
	if len(running) != 1 || running[0].PGID != 123 || running[0].SessionID != "sess" {
		t.Fatalf("running: %+v", running)
	}
	err := s.Update(func(tx *Tx) error {
		return tx.FinishRun(r.ID, RunEnd{State: RunSucceeded, HeadSHA: "def", Diffstat: "1 file", ResultJSON: `{"status":"done"}`,
			CostUSD: 0.5, InputTokens: 10, OutputTokens: 5, Stderr: string(make([]byte, 70<<10))})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(r.ID)
	if err != nil || got.State != RunSucceeded || got.SessionID != "sess" || got.Branch != "smif/b/x" || got.CostUSD != 0.5 || got.EndedAt.IsZero() {
		t.Fatalf("run: %+v %v", got, err)
	}
	if len(got.Stderr) > 64<<10 {
		t.Fatal("stderr not truncated")
	}
	if g2, err := s.GetRun(r.ID[len(r.ID)-8:]); err != nil || g2.ID != r.ID {
		t.Fatalf("suffix lookup: %v", err)
	}
	if _, err := s.GetRun("nope"); err == nil {
		t.Fatal("missing run")
	}
	_ = s.Update(func(tx *Tx) error {
		rs, err := tx.Runs(`message_id=?`, id)
		if err != nil || len(rs) != 1 {
			t.Fatalf("tx runs: %v", err)
		}
		return nil
	})
}

func TestPresenceCursorsBudgetsKV(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Update(func(tx *Tx) error { return tx.UpsertPresence("/r", "s1", "active", PresenceUpdate{}) }))
	if p, _ := s.ActivePresence("/r", time.Minute); len(p) != 1 {
		t.Fatal("presence")
	}
	now = now.Add(2 * time.Minute)
	if p, _ := s.ActivePresence("/r", time.Minute); len(p) != 0 {
		t.Fatal("presence ttl")
	}
	must(s.Update(func(tx *Tx) error { return tx.UpsertPresence("/r", "s1", "active", PresenceUpdate{}) }))
	must(s.Update(func(tx *Tx) error { return tx.UpsertPresence("/r", "s1", "ended", PresenceUpdate{}) }))
	if p, _ := s.ActivePresence("/r", time.Hour); len(p) != 0 {
		t.Fatal("ended presence")
	}
	must(s.Update(func(tx *Tx) error { return tx.SetDigestCursor("a", 10) }))
	must(s.Update(func(tx *Tx) error { return tx.SetDigestCursor("a", 5) }))
	if s.DigestCursor("a") != 10 {
		t.Fatal("cursor must not go backwards")
	}
	must(s.Update(func(tx *Tx) error { return tx.AddUsage("a", "2026-11-02", 0.25, 100) }))
	must(s.Update(func(tx *Tx) error { return tx.AddUsage("a", "2026-11-02", 0.25, 50) }))
	if c, tk := s.Usage("a", "2026-11-02"); c != 0.5 || tk != 150 {
		t.Fatalf("usage %v %v", c, tk)
	}
	must(s.Update(func(tx *Tx) error { return tx.SetKV("k", "v") }))
	if s.GetKV("k") != "v" || s.GetKV("missing") != "" {
		t.Fatal("kv")
	}
	if s.Version() != SchemaVersion || SchemaVersion < 1 {
		t.Fatal("schema version")
	}
	if s.Now() != now {
		t.Fatal("clock")
	}
}

func TestBlobsRefcountAndThreads(t *testing.T) {
	s := openTest(t)
	sha := "aa00000000000000000000000000000000000000000000000000000000000000"
	if err := s.Update(func(tx *Tx) error { return tx.PutBlob(sha, 3, "text/plain") }); err != nil {
		t.Fatal(err)
	}
	if size, mime, err := s.BlobInfo(sha); err != nil || size != 3 || mime != "text/plain" {
		t.Fatal("blob info")
	}
	if _, _, err := s.BlobInfo("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing blob")
	}
	id := envelope.NewID()
	e := &envelope.Envelope{ID: id, ThreadID: id, From: envelope.From{Agent: "a", Origin: "cli"}, To: "b", Kind: "tell", Intent: "x",
		Priority: "urgent", CreatedAt: time.Now(), Attachments: []envelope.Attachment{{Name: "f", SHA256: sha, Size: 3}}}
	if err := s.Update(func(tx *Tx) error { return tx.InsertMessage(e, true) }); err != nil {
		t.Fatal(err)
	}
	if exp, _ := s.ExpiredBlobs(time.Now().Add(time.Hour)); len(exp) != 0 {
		t.Fatal("referenced blob listed as expired")
	}
	if err := s.Update(func(tx *Tx) error {
		if _, err := tx.Transition(id, Done, Fields{Summary: Str("ok")}, nil, Accepted); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	exp, _ := s.ExpiredBlobs(time.Now().Add(time.Hour))
	if len(exp) != 1 {
		t.Fatal("unreferenced blob not expired")
	}
	_ = s.Update(func(tx *Tx) error { return tx.DeleteBlob(sha) })
	m, _ := s.GetMessage(id)
	if !m.Recurrence || m.Env.Priority != "urgent" || m.Summary != "ok" || m.HandledSeq == 0 {
		t.Fatalf("message: %+v", m)
	}
	if fm, err := s.FindMessage(id[len(id)-6:]); err != nil || fm.Env.ID != id {
		t.Fatal("find by suffix")
	}
	if _, err := s.FindMessage("zzzz"); err == nil {
		t.Fatal("find missing")
	}
	err := s.Update(func(tx *Tx) error {
		if err := tx.FreezeThread(id, "test"); err != nil {
			return err
		}
		th, err := tx.GetThread(id)
		if err != nil || !th.Frozen || th.FrozenReason != "test" {
			t.Fatalf("thread %+v", th)
		}
		if th, _ := tx.GetThread("other"); th.Frozen {
			t.Fatal("unknown thread frozen")
		}
		n, _ := tx.CountThread(id)
		if n != 1 {
			t.Fatal("count thread")
		}
		if err := tx.SetThreadBranch(id, "b", "smif/b/x"); err != nil {
			return err
		}
		if err := tx.MarkSeen(id); err != nil {
			return err
		}
		if err := tx.SetFields(id, Fields{NotBefore: Time(time.Now()), HoldReason: Str("h"), Reason: Str("r"), AnsweredBy: Str("x"),
			Summary: Str("s"), Attempt: Int(2), ExtraAttempts: Int(1)}); err != nil {
			return err
		}
		return tx.ResetCoalesced(id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ThreadBranch(id, "b") != "smif/b/x" {
		t.Fatal("thread branch")
	}
	m, _ = s.GetMessage(id)
	if !m.Seen || m.AnsweredBy != "x" || m.Attempt != 2 || m.ExtraAttempts != 1 || m.HoldReason != "h" {
		t.Fatalf("fields: %+v", m)
	}
	rows, _ := s.AuditSince(0, 100)
	if len(rows) < 3 {
		t.Fatal("audit since")
	}
	if fr, _ := s.AuditFor(id); len(fr) < 2 {
		t.Fatal("audit for")
	}
}

func TestTransitionGuardsAndCallbacks(t *testing.T) {
	s := openTest(t)
	var seen int
	s.OnAudit = func(rows []AuditRow) { seen += len(rows) }
	id := newMsg(t, s)
	err := s.Update(func(tx *Tx) error {
		_, err := tx.Transition(id, Queued, Fields{RuleID: Str("r"), Handler: Str("worker"), Profile: Str("triage"), Attempt: Int(1),
			NotBefore: Time(time.Now()), HoldReason: Str(""), Reason: Str("why"), Summary: Str("s"), HumanQuestion: Str("q"), AnsweredBy: Str("")}, nil, Accepted)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(tx *Tx) error { _, err := tx.Transition(id, Running, Fields{}, nil, Accepted); return err }); err == nil {
		t.Fatal("from-state mismatch must fail")
	}
	if err := s.Update(func(tx *Tx) error { _, err := tx.Transition("missing", Running, Fields{}, nil); return err }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message: %v", err)
	}
	if seen < 2 {
		t.Fatal("OnAudit not called")
	}
	ms, err := s.Messages(`state=?`, "queued")
	if err != nil || len(ms) != 1 || ms[0].Handler != "worker" || ms[0].HumanQuestion != "q" {
		t.Fatalf("messages: %+v %v", ms, err)
	}
	if !Queued.Open() || Done.Open() || NeedsHuman.Open() || !DeadLetter.Terminal() {
		t.Fatal("state predicates")
	}
	_ = s.Update(func(tx *Tx) error {
		if m, _ := tx.LastDoneByDedupe("b", "k", time.Now().Add(-time.Hour)); m != nil {
			t.Fatal("no done message expected")
		}
		if m, _ := tx.OpenByDedupe("b", "nope"); m != nil {
			t.Fatal("no open message expected")
		}
		_, err := tx.Exec(`SELECT 1`)
		return err
	})
}

// TestPresenceTurnAndTerminal: a report with a turn and a terminal stores
// them; a bare heartbeat keeps them; a new turn replaces the turn only.
func TestPresenceTurnAndTerminal(t *testing.T) {
	s := openTest(t)
	up := func(state string, u PresenceUpdate) {
		t.Helper()
		if err := s.Update(func(tx *Tx) error { return tx.UpsertPresence("/r", "s1", state, u) }); err != nil {
			t.Fatal(err)
		}
	}
	get := func() Presence {
		t.Helper()
		ps, err := s.AllPresence()
		if err != nil || len(ps) != 1 {
			t.Fatalf("presence %v %v", ps, err)
		}
		return ps[0]
	}
	up("active", PresenceUpdate{Turn: "busy", TTY: "/dev/ttys003", PID: 42, Term: "Apple_Terminal"})
	if p := get(); p.Turn != "busy" || p.TTY != "/dev/ttys003" || p.PID != 42 || p.Term != "Apple_Terminal" || p.TurnAt.IsZero() {
		t.Fatalf("first report: %+v", p)
	}
	up("active", PresenceUpdate{})
	if p := get(); p.Turn != "busy" || p.TTY != "/dev/ttys003" || p.PID != 42 {
		t.Fatalf("a bare heartbeat changed the session: %+v", p)
	}
	up("active", PresenceUpdate{Turn: "idle", Title: "rfc", LastPrompt: "run the tests"})
	if p := get(); p.Turn != "idle" || p.TTY != "/dev/ttys003" || p.Title != "rfc" || p.LastPrompt != "run the tests" {
		t.Fatalf("idle: %+v", p)
	}
	up("active", PresenceUpdate{})
	if p := get(); p.Title != "rfc" || p.LastPrompt != "run the tests" {
		t.Fatalf("a bare heartbeat dropped the title: %+v", p)
	}
	if err := s.Update(func(tx *Tx) error { return tx.SetPresenceTurn("s1", "busy") }); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.Turn != "busy" {
		t.Fatalf("SetPresenceTurn: %+v", p)
	}
}
