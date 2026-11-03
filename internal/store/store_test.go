package store

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/envelope"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newMsg(t *testing.T, s *Store) string {
	t.Helper()
	id := envelope.NewID()
	e := &envelope.Envelope{ID: id, ThreadID: id, From: envelope.From{Agent: "a", Origin: "cli"}, To: "b",
		Kind: "tell", Intent: "x.y", Priority: "normal", CreatedAt: time.Now()}
	if err := s.Update(func(tx *Tx) error { return tx.InsertMessage(e, false) }); err != nil {
		t.Fatal(err)
	}
	return id
}

// pathTo finds a legal path from accepted to target (BFS).
func pathTo(target State) []State {
	type node struct {
		s    State
		path []State
	}
	seen := map[State]bool{Accepted: true}
	q := []node{{Accepted, nil}}
	for len(q) > 0 {
		n := q[0]
		q = q[1:]
		if n.s == target {
			return n.path
		}
		for _, nx := range transitions[n.s] {
			if !seen[nx] {
				seen[nx] = true
				q = append(q, node{nx, append(append([]State{}, n.path...), nx)})
			}
		}
	}
	return nil
}

func TestEveryStateReachableAndOnlyLegalMovesSucceed(t *testing.T) {
	s := openTest(t)
	for _, from := range AllStates {
		p := pathTo(from)
		if from != Accepted && p == nil {
			t.Fatalf("state %s unreachable", from)
		}
		for _, to := range AllStates {
			id := newMsg(t, s)
			err := s.Update(func(tx *Tx) error {
				for _, st := range p {
					if _, err := tx.Transition(id, st, Fields{}, nil); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("path to %s: %v", from, err)
			}
			err = s.Update(func(tx *Tx) error { _, err := tx.Transition(id, to, Fields{}, nil); return err })
			if Allowed(from, to) && err != nil {
				t.Errorf("%s -> %s should succeed: %v", from, to, err)
			}
			if !Allowed(from, to) && !errors.Is(err, ErrIllegal) {
				t.Errorf("%s -> %s should be illegal, got %v", from, to, err)
			}
		}
	}
}

// Property: under random operation sequences, every transition recorded in
// the audit log is a legal edge, terminal states other than dead_letter
// never change, and the stored state always equals the last audited to_state.
func TestStateMachineProperty(t *testing.T) {
	s := openTest(t)
	rng := rand.New(rand.NewSource(42))
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = newMsg(t, s)
	}
	for i := 0; i < 3000; i++ {
		id := ids[rng.Intn(len(ids))]
		to := AllStates[rng.Intn(len(AllStates))]
		_ = s.Update(func(tx *Tx) error { _, err := tx.Transition(id, to, Fields{}, nil); return err })
	}
	rows, err := s.AuditWhere(`event='message.transition'`)
	if err != nil {
		t.Fatal(err)
	}
	last := map[string]State{}
	for _, r := range rows {
		from, to := State(r.Detail["from_state"].(string)), State(r.Detail["to_state"].(string))
		if !Allowed(from, to) {
			t.Fatalf("illegal transition reached: %s -> %s", from, to)
		}
		if prev, ok := last[r.MessageID]; ok && prev != from {
			t.Fatalf("audit chain broken for %s: %s then from %s", r.MessageID, prev, from)
		}
		if from.Terminal() && from != DeadLetter {
			t.Fatalf("terminal state %s changed", from)
		}
		last[r.MessageID] = to
	}
	for id, st := range last {
		m, err := s.GetMessage(id)
		if err != nil {
			t.Fatal(err)
		}
		if m.State != st {
			t.Fatalf("stored state %s != audited %s", m.State, st)
		}
	}
	if len(rows) < 50 {
		t.Fatalf("too few transitions exercised: %d", len(rows))
	}
}

func TestDedupeAndPairCounts(t *testing.T) {
	s := openTest(t)
	id := envelope.NewID()
	e := &envelope.Envelope{ID: id, ThreadID: id, From: envelope.From{Agent: "a", Origin: "cli"}, To: "b",
		Kind: "event", Intent: "crash.report", Priority: "high", DedupeKey: "k", CreatedAt: time.Now()}
	if err := s.Update(func(tx *Tx) error { return tx.InsertMessage(e, false) }); err != nil {
		t.Fatal(err)
	}
	err := s.Update(func(tx *Tx) error {
		m, err := tx.OpenByDedupe("b", "k")
		if err != nil || m == nil {
			t.Fatalf("open by dedupe: %v %v", m, err)
		}
		if err := tx.Coalesce(m, e); err != nil {
			return err
		}
		n, err := tx.CountPair(id, "a", "b", time.Now().Add(-time.Minute))
		if n != 1 {
			t.Fatalf("pair count %d", n)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMessage(id)
	if m.Coalesced != 1 {
		t.Fatalf("coalesced = %d", m.Coalesced)
	}
	// Waiting on the human still counts as open; done does not.
	for _, c := range []struct {
		to   State
		open bool
	}{{NeedsHuman, true}, {Done, false}} {
		err := s.Update(func(tx *Tx) error {
			if _, err := tx.Transition(id, c.to, Fields{}, nil); err != nil {
				return err
			}
			m, err := tx.OpenByDedupe("b", "k")
			if (m != nil) != c.open {
				t.Fatalf("%s: open by dedupe = %v, want %v", c.to, m != nil, c.open)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditTailAndHead(t *testing.T) {
	s := openTest(t)
	if h, err := s.AuditHead(); err != nil || h != 0 {
		t.Fatalf("empty head: %d %v", h, err)
	}
	if rs, err := s.AuditTail(5); err != nil || len(rs) != 0 {
		t.Fatalf("empty tail: %v %v", rs, err)
	}
	for i := 0; i < 4; i++ {
		newMsg(t, s)
	}
	all, err := s.AuditSince(0, 1000)
	if err != nil || len(all) < 4 {
		t.Fatalf("rows: %d %v", len(all), err)
	}
	head, err := s.AuditHead()
	if err != nil || head != all[len(all)-1].Seq {
		t.Fatalf("head %d, want %d (%v)", head, all[len(all)-1].Seq, err)
	}
	tail, err := s.AuditTail(3)
	if err != nil || len(tail) != 3 {
		t.Fatalf("tail: %d %v", len(tail), err)
	}
	for i, r := range tail {
		if want := all[len(all)-3+i].Seq; r.Seq != want {
			t.Fatalf("tail[%d] seq %d, want %d (oldest first)", i, r.Seq, want)
		}
	}
	if rs, _ := s.AuditTail(0); len(rs) != 0 {
		t.Fatalf("tail 0: %d rows", len(rs))
	}
}
