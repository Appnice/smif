package daemon

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// Send when idle (ADR-0021). A busy or waiting session takes the human's
// prompt in a queue, and the watcher types the oldest item once the session
// reads idle, through the gates of session.prompt. Only the human's RPC
// call adds an item, so no inbound text reaches a session (D4). A waiting
// session is never typed into: smif does not answer a permission prompt.

const (
	queueWatchEvery = time.Second      // how often the watcher looks
	queueIdleWait   = 2 * time.Second  // how long a session reads idle first
	queueRetry      = 30 * time.Second // the shortest gap between two tries
	queueKept       = 24 * time.Hour   // an item expires after this
	queueGoneAfter  = 5 * time.Second  // a session missing this long ended
)

// queuedBySession is the items still queued, per session, oldest first.
func (d *Daemon) queuedBySession() map[string][]api.QueuedPrompt {
	items, err := d.store.QueuedItems()
	if err != nil {
		d.log.Warn("cannot read the prompt queue", "error", err.Error())
		return nil
	}
	out := map[string][]api.QueuedPrompt{}
	for _, it := range items {
		out[it.SessionID] = append(out[it.SessionID], api.QueuedPrompt{ID: it.ID, Text: it.Text, CreatedAt: it.CreatedAt, Error: it.Error})
	}
	return out
}

// queueRefusal says why an item for this session could never go ("" = it
// can): the same rules as sessionView for a level, a read-only provider and
// a terminal. A terminal not known yet is no refusal, because a session
// reports it at its next turn.
func (d *Daemon) queueRefusal(p store.Presence, v api.SessionView) string {
	prov := d.harnesses.Get(p.Harness)
	switch {
	case p.Level == harness.LevelListed || v.Unverified || v.Background:
		return v.Why
	case prov != nil || p.Level != harness.LevelHerdr:
		if why := harness.ReadOnly(p.Harness, prov); why != "" {
			return why
		}
	}
	if p.Term != "" {
		return d.terminal.Supports(p)
	}
	return ""
}

// sessionQueue is session.queue (human only): the text waits until the
// session is idle.
func (d *Daemon) sessionQueue(p api.SessionQueueParams) (*api.SessionQueueResult, error) {
	text, err := checkPrompt(d.expandPrompt(p.Text))
	if err != nil {
		return nil, err
	}
	target, err := d.writeTarget(p.SessionID, p.Name)
	if err != nil {
		return nil, err
	}
	v := d.sessionView(d.Config(), *target)
	if why := d.queueRefusal(*target, v); why != "" {
		return nil, api.Errf(api.CodeRejected, "cannot queue for %s: %s", api.ShortSession(target.SessionID), why)
	}
	it := store.QueueItem{SessionID: target.SessionID, Agent: v.Agent, Name: viewName(v), Text: text}
	var id int64
	if err := d.store.Update(func(tx *store.Tx) error {
		var err error
		id, err = tx.AddQueued(it)
		return err
	}); err != nil {
		return nil, err
	}
	return &api.SessionQueueResult{ID: id, Agent: v.Agent, SessionID: target.SessionID}, nil
}

// sessionQueueCancel is session.queue.cancel (human only).
func (d *Daemon) sessionQueueCancel(p api.SessionQueueCancelParams) error {
	it, err := d.store.QueueItem(p.ID)
	if err != nil {
		return err
	}
	if it == nil {
		return api.Errf(api.CodeNotFound, "no queued item %d", p.ID)
	}
	moved := false
	if err := d.store.Update(func(tx *store.Tx) error {
		var err error
		moved, err = tx.QueueMove(*it, store.QueueQueued, store.QueueCancelled, "")
		return err
	}); err != nil {
		return err
	}
	if !moved {
		return api.Errf(api.CodeRejected, "item %d is no longer queued", p.ID)
	}
	return nil
}

// queueWatch is what the watcher keeps between two looks.
type queueWatch struct {
	idleSince map[string]time.Time // session → when it started to read idle
	missing   map[string]time.Time // session → when it first was not live
	tried     map[int64]time.Time  // item → its last try
	herdrPID  map[string]int       // session → the process of its herdr row
}

func newQueueWatch() *queueWatch {
	return &queueWatch{idleSince: map[string]time.Time{}, missing: map[string]time.Time{}, tried: map[int64]time.Time{}, herdrPID: map[string]int{}}
}

// queueLoop sends queued items until the daemon stops. It runs apart from
// the notification watcher, because typing can take 11 s.
func (d *Daemon) queueLoop() {
	t := time.NewTicker(queueWatchEvery)
	defer t.Stop()
	w := newQueueWatch()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			d.watchQueue(w, time.Now())
		}
	}
}

// watchQueue is one look: items that are too old expire, the items of a
// session that ended are gone, and each session that read idle long enough
// takes its oldest item. With nothing queued it reads no session.
func (d *Daemon) watchQueue(w *queueWatch, now time.Time) {
	items, err := d.store.QueuedItems()
	if err != nil {
		d.log.Warn("cannot read the prompt queue", "error", err.Error())
		return
	}
	if len(items) == 0 {
		*w = *newQueueWatch()
		return
	}
	var order []string
	bySession := map[string][]store.QueueItem{}
	for _, it := range items {
		if now.Sub(it.CreatedAt) >= queueKept {
			d.moveQueued(it, store.QueueExpired, "")
			continue
		}
		if bySession[it.SessionID] == nil {
			order = append(order, it.SessionID)
		}
		bySession[it.SessionID] = append(bySession[it.SessionID], it)
	}
	ps, err := d.liveSessions()
	if err != nil {
		return
	}
	cfg := d.Config()
	live := map[string]api.SessionView{}
	for _, p := range ps {
		if bySession[p.SessionID] != nil {
			live[p.SessionID] = d.sessionView(cfg, p)
			if p.Level == harness.LevelHerdr {
				w.herdrPID[p.SessionID] = p.PID
			}
		}
	}
	for _, id := range order {
		v, ok := live[id]
		if !ok {
			// A herdr call that fails or hangs gives no row, and a scan
			// result cannot show why. The process can: while it lives, the
			// session did not end, and the item waits for the row.
			if pid, seen := w.herdrPID[id]; seen && processAlive(pid) {
				delete(w.missing, id)
				delete(w.idleSince, id)
				continue
			}
			first, seen := w.missing[id]
			if !seen {
				w.missing[id] = now
			} else if now.Sub(first) >= queueGoneAfter {
				delete(w.missing, id)
				delete(w.idleSince, id)
				delete(w.herdrPID, id)
				d.queueGone(bySession[id])
			}
			continue
		}
		delete(w.missing, id)
		if v.Turn != "idle" {
			delete(w.idleSince, id)
			continue
		}
		since, seen := w.idleSince[id]
		if !seen {
			w.idleSince[id] = now
			continue
		}
		it := bySession[id][0]
		if tried, ok := w.tried[it.ID]; now.Sub(since) < queueIdleWait || ok && now.Sub(tried) < queueRetry {
			continue
		}
		d.sendQueued(w, it, now)
	}
}

// moveQueued changes an item's state.
func (d *Daemon) moveQueued(it store.QueueItem, to, why string) bool {
	moved := false
	err := d.store.Update(func(tx *store.Tx) error {
		var err error
		moved, err = tx.QueueMove(it, store.QueueQueued, to, why)
		return err
	})
	if err != nil {
		d.log.Warn("queue item not moved", "error", err.Error(), "item", it.ID)
	}
	return err == nil && moved
}

// queueGone ends the items of a session that ended, with one notification.
// The notification counts them and quotes no prompt.
func (d *Daemon) queueGone(items []store.QueueItem) {
	n := 0
	for _, it := range items {
		if d.moveQueued(it, store.QueueGone, "the session ended") {
			n++
		}
	}
	if n == 0 {
		return
	}
	it := items[0]
	text := "1 queued prompt did not go in: the session ended"
	if n > 1 {
		text = fmt.Sprintf("%d queued prompts did not go in: the session ended", n)
	}
	v := api.SessionView{SessionID: it.SessionID, Agent: it.Agent}
	d.notifySession("session_queue_gone", nonEmpty(it.Name, "session "+api.ShortSession(it.SessionID)), v, text)
}

// queueGate reads the session again and applies the gates of
// session.prompt. Target nil means that it no longer reads idle, which is
// no refusal: the item waits for the next idle. The caller holds promptMu.
func (d *Daemon) queueGate(sessionID string) (target *store.Presence, v api.SessionView, why string) {
	ps, err := d.liveSessions()
	if err != nil {
		return nil, v, ""
	}
	for i := range ps {
		if ps[i].SessionID == sessionID {
			target = &ps[i]
		}
	}
	if target == nil {
		return nil, v, ""
	}
	v = d.sessionView(d.Config(), *target)
	switch {
	case v.Turn != "idle":
		return nil, v, ""
	case !v.CanPrompt:
		why = v.Why
	case v.Unsent != "":
		// The next text would land behind it in the input box.
		why = "the last prompt may sit unsent in its input; o shows the tab"
	default:
		if err := d.terminal.Ready(*target); err != nil {
			why = err.Error()
		}
	}
	return target, v, why
}

// sendQueued tries one item. The item turns sent before the first key, so
// a crash or a cancel cannot type it twice. It goes back to queued only
// when no key went in.
func (d *Daemon) sendQueued(w *queueWatch, it store.QueueItem, now time.Time) {
	d.promptMu.Lock()
	target, v, why := d.queueGate(it.SessionID)
	if target == nil {
		d.promptMu.Unlock()
		return
	}
	w.tried[it.ID] = now
	if why != "" {
		d.promptMu.Unlock()
		d.noteRefused(it, why)
		return
	}
	if !d.moveQueued(it, store.QueueSent, "") {
		d.promptMu.Unlock()
		return // the human cancelled it a moment ago
	}
	d.prompts.start(it.SessionID)
	d.promptMu.Unlock()
	delete(w.idleSince, it.SessionID) // one item per idle
	err := d.deliverPrompt(d.ctx, *target, v.Agent, it.Text)
	switch {
	case err == nil:
		d.prompts.done(it.SessionID)
	case errors.Is(err, errNotTyped):
		d.prompts.done(it.SessionID)
		why := strings.TrimPrefix(err.Error(), errNotTyped.Error()+": ")
		if uerr := d.store.Update(func(tx *store.Tx) error {
			_, err := tx.QueueMove(it, store.QueueSent, store.QueueQueued, why)
			return err
		}); uerr != nil {
			d.log.Warn("queue item not moved back", "error", uerr.Error(), "item", it.ID)
		}
	default:
		// The keys went in: the row shows Unsent, the human looks, and
		// the text is never typed again.
		d.prompts.fail(it.SessionID, err.Error())
		_ = d.store.Update(func(tx *store.Tx) error { return tx.QueueNote(it.ID, err.Error()) })
	}
}

// noteRefused keeps why the gates refused an item.
func (d *Daemon) noteRefused(it store.QueueItem, why string) {
	if err := d.store.Update(func(tx *store.Tx) error { return tx.QueueRefused(it, why) }); err != nil {
		d.log.Warn("queue refusal not kept", "error", err.Error(), "item", it.ID)
	}
}
