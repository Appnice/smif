package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// gateTerminal counts the foreground checks, and refuses while ready or
// typeErr is set.
type gateTerminal struct {
	terminal
	ready   error
	typeErr error
	checks  int
}

func (g *gateTerminal) Ready(p store.Presence) error {
	g.checks++
	if g.ready != nil {
		return g.ready
	}
	return g.terminal.Ready(p)
}

func (g *gateTerminal) Type(ctx context.Context, p store.Presence, text string) error {
	if g.typeErr != nil {
		return g.typeErr
	}
	return g.terminal.Type(ctx, p, text)
}

func setTurn(t *testing.T, d *Daemon, id, repo, turn string) {
	t.Helper()
	if err := d.store.Update(func(tx *store.Tx) error {
		return tx.UpsertPresence(repo, id, "active", store.PresenceUpdate{Turn: turn, TTY: "/dev/ttys999", Term: termTmux, PID: os.Getpid(), Harness: "claude"})
	}); err != nil {
		t.Fatal(err)
	}
}

func queueOne(t *testing.T, d *Daemon, id, text string) int64 {
	t.Helper()
	res, err := d.sessionQueue(api.SessionQueueParams{SessionID: id, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return res.ID
}

func itemOf(t *testing.T, d *Daemon, id int64) store.QueueItem {
	t.Helper()
	it, err := d.store.QueueItem(id)
	if err != nil || it == nil {
		t.Fatalf("item %d: %v", id, err)
	}
	return *it
}

// auditCount counts the audit rows of an event.
func auditCount(t *testing.T, d *Daemon, event string) int {
	t.Helper()
	page, err := d.store.AuditSince(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range page {
		if r.Event == event {
			n++
		}
	}
	return n
}

// A refused item stays with its reason, and the gates run at most once in
// 30 s. Once they pass, it is typed once.
func TestQueueGateRefusalRetries(t *testing.T) {
	d, typed := testDaemon(t)
	g := &gateTerminal{terminal: d.terminal, ready: errors.New("zsh is in the foreground of its tab, not claude")}
	d.terminal = g
	setTurn(t, d, "q1", "/r/a", "busy")
	id := queueOne(t, d, "q1", "run the tests")
	w, now := newQueueWatch(), time.Now()

	// Busy: nothing tries.
	d.watchQueue(w, now)
	if g.checks != 0 {
		t.Fatalf("%d checks on a busy session", g.checks)
	}
	setTurn(t, d, "q1", "/r/a", "idle")
	d.watchQueue(w, now)                                     // reads idle
	d.watchQueue(w, now.Add(queueIdleWait-time.Millisecond)) // too soon
	if g.checks != 0 {
		t.Fatalf("%d checks before 2 s of idle", g.checks)
	}
	for s := 2; s <= 31; s++ {
		d.watchQueue(w, now.Add(time.Duration(s)*time.Second))
	}
	it := itemOf(t, d, id)
	if g.checks != 1 || it.State != store.QueueQueued || !strings.Contains(it.Error, "zsh is in the foreground") {
		t.Fatalf("checks %d, item %+v", g.checks, it)
	}
	if v := viewOf(t, d, "q1"); len(v.Queue) != 1 || !strings.Contains(v.Queue[0].Error, "zsh") {
		t.Fatalf("the reason does not show: %+v", v.Queue)
	}
	d.watchQueue(w, now.Add(32*time.Second))
	if g.checks != 2 {
		t.Fatalf("no second try after 30 s: %d", g.checks)
	}
	if n := auditCount(t, d, "session.queue.refused"); n != 1 {
		t.Fatalf("%d refused rows for one reason", n)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed while refused: %s", got)
	}
	g.ready = nil
	d.watchQueue(w, now.Add(61*time.Second))
	d.watchQueue(w, now.Add(62*time.Second))
	if it := itemOf(t, d, id); it.State != store.QueueSent || it.Error != "" {
		t.Fatalf("item: %+v", it)
	}
	if got := typedText(t, typed); strings.Count(got, "run the tests") != 1 {
		t.Fatalf("typed: %s", got)
	}
}

// Two items go one per idle, oldest first, and a waiting session gets none.
func TestQueueOnePerIdleOldestFirst(t *testing.T) {
	d, typed := testDaemon(t)
	setTurn(t, d, "q1", "/r/a", "waiting")
	queueOne(t, d, "q1", "first")
	queueOne(t, d, "q1", "second")
	w, now := newQueueWatch(), time.Now()
	look := func(s int) { d.watchQueue(w, now.Add(time.Duration(s)*time.Second)) }
	for s := range 5 {
		look(s)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed into a waiting session: %s", got)
	}
	setTurn(t, d, "q1", "/r/a", "idle")
	look(10)
	look(12)
	if got := typedText(t, typed); !strings.Contains(got, `"text":"first"`) || strings.Contains(got, "second") {
		t.Fatalf("after one idle: %s", got)
	}
	v := viewOf(t, d, "q1")
	if v.Turn != "busy" || len(v.Queue) != 1 || v.Queue[0].Text != "second" {
		t.Fatalf("after one idle: %+v", v)
	}
	// It stays busy: the second item waits, however long.
	look(14)
	look(50)
	if got := typedText(t, typed); strings.Contains(got, "second") {
		t.Fatalf("second item went in a busy session: %s", got)
	}
	// A new idle takes the second item, with the same 2 s.
	setTurn(t, d, "q1", "/r/a", "idle")
	look(60)
	if got := typedText(t, typed); strings.Contains(got, "second") {
		t.Fatalf("second item went at once: %s", got)
	}
	look(62)
	if got := typedText(t, typed); strings.Count(got, `"text"`) != 2 || !strings.Contains(got, `"text":"second"`) {
		t.Fatalf("after two idles: %s", got)
	}
	if n := auditCount(t, d, "session.queue.sent"); n != 2 {
		t.Fatalf("%d sent rows", n)
	}
}

// A terminal that fails types nothing, so the item goes back with the
// reason. A prompt that may sit unsent holds the next one back.
func TestQueueTerminalFailureAndUnsent(t *testing.T) {
	d, typed := testDaemon(t)
	g := &gateTerminal{terminal: d.terminal, typeErr: errors.New("osascript: not allowed")}
	d.terminal = g
	setTurn(t, d, "q1", "/r/a", "idle")
	id := queueOne(t, d, "q1", "first")
	w, now := newQueueWatch(), time.Now()
	d.watchQueue(w, now)
	d.watchQueue(w, now.Add(2*time.Second))
	it := itemOf(t, d, id)
	if it.State != store.QueueQueued || it.Error != "osascript: not allowed" {
		t.Fatalf("item: %+v", it)
	}
	if v := viewOf(t, d, "q1"); v.Turn != "idle" {
		t.Fatalf("a failed try leaves the session %s", v.Turn)
	}
	g.typeErr = nil
	d.prompts.start("q1")
	d.prompts.fail("q1", "claude started no turn")
	d.watchQueue(w, now.Add(40*time.Second))
	d.watchQueue(w, now.Add(42*time.Second))
	if it := itemOf(t, d, id); it.State != store.QueueQueued || !strings.Contains(it.Error, "sit unsent") {
		t.Fatalf("item: %+v", it)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed behind an unsent prompt: %s", got)
	}
}

func TestQueueExpiresAfter24h(t *testing.T) {
	d, typed := testDaemon(t)
	setTurn(t, d, "q1", "/r/a", "busy")
	id := queueOne(t, d, "q1", "too late")
	w, now := newQueueWatch(), time.Now()
	d.watchQueue(w, now.Add(queueKept-time.Minute))
	if it := itemOf(t, d, id); it.State != store.QueueQueued {
		t.Fatalf("expired early: %+v", it)
	}
	setTurn(t, d, "q1", "/r/a", "idle")
	d.watchQueue(w, now.Add(queueKept))
	d.watchQueue(w, now.Add(queueKept+10*time.Second))
	if it := itemOf(t, d, id); it.State != store.QueueExpired {
		t.Fatalf("item: %+v", it)
	}
	if n := auditCount(t, d, "session.queue.expired"); n != 1 {
		t.Fatalf("%d expired rows", n)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed an expired item: %s", got)
	}
}

// A session that ended turns its items gone with one notification, which
// quotes no prompt. A session missing for one look keeps them.
func TestQueueGone(t *testing.T) {
	d, typed := testDaemon(t)
	notes := &watchNotes{}
	d.notifier = notes
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Update(func(tx *store.Tx) error {
		return tx.UpsertPresence("/r/a", "q1", "active", store.PresenceUpdate{Turn: "busy", TTY: "/dev/ttys999", Term: termTmux, PID: sleep.Process.Pid, Harness: "claude"})
	}); err != nil {
		t.Fatal(err)
	}
	a := queueOne(t, d, "q1", "secret plan")
	b := queueOne(t, d, "q1", "other plan")
	w, now := newQueueWatch(), time.Now()
	d.watchQueue(w, now)
	if err := sleep.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = sleep.Wait()
	d.watchQueue(w, now.Add(time.Second))
	if it := itemOf(t, d, a); it.State != store.QueueQueued {
		t.Fatalf("gone after one look: %+v", it)
	}
	d.watchQueue(w, now.Add(time.Second+queueGoneAfter))
	for _, id := range []int64{a, b} {
		if it := itemOf(t, d, id); it.State != store.QueueGone {
			t.Fatalf("item: %+v", it)
		}
	}
	if len(notes.kinds) != 1 || notes.kinds[0] != "session_queue_gone" {
		t.Fatalf("notifications: %v", notes.kinds)
	}
	if n := auditCount(t, d, "session.queue.gone"); n != 2 {
		t.Fatalf("%d gone rows", n)
	}
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed: %s", got)
	}
}

// newScan makes the next look read a scan that ended, so a test need not
// wait for one.
func newScan(d *Daemon) {
	wait := func() {
		d.scan.mu.Lock()
		running := d.scan.running
		d.scan.mu.Unlock()
		if running != nil {
			<-running
		}
	}
	wait()
	d.scan.expire()
	d.scan.get()
	wait()
}

// herdrRow is a fake scan's row: an idle pi agent on process pid.
func herdrRow(f *fakeHerdr, pid int) scanRow {
	return scanRow{level: harness.LevelHerdr, harness: "pi", id: scanID("h", "smifh-test", "term_1"), cwd: f.cwd,
		pid: pid, pids: []int{pid}, term: termHerdr, turn: "idle"}
}

// A herdr outage drops the row while the agent runs. The item stays queued
// with no notification, and it goes in once the row is back and reads idle
// for 2 s again. The typing takes the herdr route.
func TestQueueHerdrOutage(t *testing.T) {
	for _, c := range []struct {
		name string
		// start sets the scan and gives what ends and starts the outage.
		start func(*testing.T, *Daemon, *fakeHerdr) (down, up func())
	}{
		{"fake scan drops the row", func(_ *testing.T, d *Daemon, f *fakeHerdr) (func(), func()) {
			row, gone := herdrRow(f, f.pid), atomic.Bool{}
			d.scan.run = func(context.Context) []scanRow {
				if gone.Load() {
					return nil
				}
				return []scanRow{row}
			}
			return func() { gone.Store(true) }, func() { gone.Store(false) }
		}},
		{"herdr calls fail", func(t *testing.T, _ *Daemon, f *fakeHerdr) (func(), func()) {
			list := filepath.Join(f.dir, "sessions.json")
			body, err := os.ReadFile(list)
			if err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Remove(list); err != nil {
					t.Fatal(err)
				}
			}, func() { f.write(t, "sessions.json", string(body)) }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := scanDaemon(t)
			d.ctx = context.Background() // Start sets it, and herdr calls need it
			f := startFakeHerdr(t, "idle")
			notes := &watchNotes{}
			d.notifier = notes
			down, up := c.start(t, d, f)
			newScan(d)
			v := agentRow(t, d)
			if v.Level != harness.LevelHerdr || !v.CanPrompt {
				t.Fatalf("row %+v", v)
			}
			id := queueOne(t, d, v.SessionID, "run the tests")
			w, now := newQueueWatch(), time.Now()
			look := func(s int) {
				newScan(d)
				d.watchQueue(w, now.Add(time.Duration(s)*time.Second))
			}
			look(0)
			down()
			for s := 1; s <= 10; s++ {
				look(s)
				if it := itemOf(t, d, id); it.State != store.QueueQueued {
					t.Fatalf("at %d s: %+v", s, it)
				}
			}
			if len(notes.kinds) != 0 {
				t.Fatalf("notifications: %v", notes.kinds)
			}
			up()
			look(11)
			if got := f.prompts(t); got != "" {
				t.Fatalf("typed before 2 s of idle: %q", got)
			}
			look(13)
			look(15)
			look(50)
			if it := itemOf(t, d, id); it.State != store.QueueSent {
				t.Fatalf("item: %+v", it)
			}
			if got := f.prompts(t); got != "w1:p1 run the tests\n" {
				t.Fatalf("prompts %q", got)
			}
			if n := auditCount(t, d, "session.queue.gone"); n != 0 {
				t.Fatalf("%d gone rows", n)
			}
			if len(notes.kinds) != 0 {
				t.Fatalf("notifications: %v", notes.kinds)
			}
		})
	}
}

// A herdr row whose process ended turns its items gone 5 s later, with or
// without an outage before it. It takes no typing.
func TestQueueHerdrSessionEnded(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprintf("outage %v", outage), func(t *testing.T) {
			d := scanDaemon(t)
			f := startFakeHerdr(t, "busy")
			notes := &watchNotes{}
			d.notifier = notes
			agent := exec.Command("sleep", "60")
			if err := agent.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = agent.Process.Kill()
				_ = agent.Wait()
			})
			row, gone := herdrRow(f, agent.Process.Pid), atomic.Bool{}
			d.scan.run = func(context.Context) []scanRow {
				if gone.Load() {
					return nil
				}
				return []scanRow{row}
			}
			newScan(d)
			id := queueOne(t, d, agentRow(t, d).SessionID, "secret plan")
			w, now := newQueueWatch(), time.Now()
			look := func(s int) {
				newScan(d)
				d.watchQueue(w, now.Add(time.Duration(s)*time.Second))
			}
			look(0)
			start := 1
			if outage {
				gone.Store(true)
				for ; start <= 10; start++ {
					look(start)
				}
			}
			if err := agent.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = agent.Wait()
			look(start)
			if it := itemOf(t, d, id); it.State != store.QueueQueued {
				t.Fatalf("gone after one look: %+v", it)
			}
			look(start + int(queueGoneAfter/time.Second))
			if it := itemOf(t, d, id); it.State != store.QueueGone {
				t.Fatalf("item: %+v", it)
			}
			if len(notes.kinds) != 1 || notes.kinds[0] != "session_queue_gone" {
				t.Fatalf("notifications: %v", notes.kinds)
			}
			if got := f.prompts(t); got != "" {
				t.Fatalf("typed: %q", got)
			}
		})
	}
}

func TestQueueCancel(t *testing.T) {
	d, typed := testDaemon(t)
	setTurn(t, d, "q1", "/r/a", "idle")
	id := queueOne(t, d, "q1", "never mind")
	if err := d.sessionQueueCancel(api.SessionQueueCancelParams{ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := d.sessionQueueCancel(api.SessionQueueCancelParams{ID: id}); !api.IsCode(err, api.CodeRejected) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := d.sessionQueueCancel(api.SessionQueueCancelParams{ID: 999}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("unknown item: %v", err)
	}
	if n := auditCount(t, d, "session.queue.cancelled"); n != 1 {
		t.Fatalf("%d cancelled rows", n)
	}
	w, now := newQueueWatch(), time.Now()
	d.watchQueue(w, now)
	d.watchQueue(w, now.Add(3*time.Second))
	if got := typedText(t, typed); got != "" {
		t.Fatalf("typed a cancelled item: %s", got)
	}
}

// What can never be typed takes no item, and bad text is refused as in
// session.prompt.
func TestQueueRefusals(t *testing.T) {
	d, _ := testDaemon(t)
	row(t, d, "ro1", "qwen", "/r/a")
	if _, err := d.sessionQueue(api.SessionQueueParams{SessionID: "ro1", Text: "x"}); !api.IsCode(err, api.CodeRejected) {
		t.Fatalf("read-only provider: %v", err)
	}
	if _, err := d.sessionQueue(api.SessionQueueParams{SessionID: "nope", Text: "x"}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	setTurn(t, d, "q1", "/r/a", "busy")
	for _, bad := range []string{"", "two\nlines", "esc \x1b[2J", strings.Repeat("x", MaxPromptRunes+1)} {
		if _, err := d.sessionQueue(api.SessionQueueParams{SessionID: "q1", Text: bad}); !api.IsCode(err, api.CodeParams) {
			t.Fatalf("text %q: %v", bad, err)
		}
	}
	d.terminal = offTerminal{}
	if _, err := d.sessionQueue(api.SessionQueueParams{SessionID: "q1", Text: "x"}); !api.IsCode(err, api.CodeRejected) {
		t.Fatalf("prompts off: %v", err)
	}
}

// The expired, sent and gone rows go with the message retention. A queued
// row stays.
func TestQueueGC(t *testing.T) {
	d, _ := testDaemon(t)
	setTurn(t, d, "q1", "/r/a", "busy")
	keep := queueOne(t, d, "q1", "keep")
	drop := queueOne(t, d, "q1", "drop")
	if err := d.sessionQueueCancel(api.SessionQueueCancelParams{ID: drop}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-d.Config().Daemon.MessageRetention.Duration - time.Hour).UnixMilli()
	if err := d.store.Update(func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE session_queue SET created_at=?`, old)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GC(); err != nil {
		t.Fatal(err)
	}
	items, err := d.store.QueueItems("q1")
	if err != nil || len(items) != 1 || items[0].ID != keep {
		t.Fatalf("items %+v (%v)", items, err)
	}
}

// Every state the queue can reach from queued is a real move, and no other.
func TestQueueMoves(t *testing.T) {
	d, _ := testDaemon(t)
	setTurn(t, d, "q1", "/r/a", "busy")
	id := queueOne(t, d, "q1", "x")
	it := itemOf(t, d, id)
	for _, c := range []struct {
		from, to string
		moved    bool
		bad      bool
	}{
		{store.QueueSent, store.QueueGone, false, true},
		{store.QueueCancelled, store.QueueQueued, false, true},
		{store.QueueSent, store.QueueQueued, false, false}, // allowed, but the item is queued
		{store.QueueQueued, store.QueueSent, true, false},
	} {
		t.Run(fmt.Sprintf("%s-%s", c.from, c.to), func(t *testing.T) {
			var moved bool
			err := d.store.Update(func(tx *store.Tx) error {
				var err error
				moved, err = tx.QueueMove(it, c.from, c.to, "")
				return err
			})
			if moved != c.moved || (err != nil) != c.bad {
				t.Fatalf("moved %v, err %v", moved, err)
			}
		})
	}
}
