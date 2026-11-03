package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/auth"
	"github.com/appnice/mailroom/internal/config"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/router"
	"github.com/appnice/mailroom/internal/store"
)

func (d *Daemon) authenticate(a api.Auth) (api.Caller, error) {
	return d.reg.Authenticate(a.Agent, a.Token, a.Origin)
}

func (d *Daemon) onDenied(method string, a api.Auth, err error) {
	d.log.Warn("auth failed", "method", method, "agent", a.Agent, "error", err.Error())
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("auth.failed", a.Agent, "", "", "", map[string]any{"method": method, "error": err.Error()})
	})
}

// forbiddenDraftKeys are envelope fields only the daemon may set (R-SEC-1).
var forbiddenDraftKeys = []string{"from", "id", "hop", "parent_id", "origin", "created_at", "status"}

// Ingest validates, stamps and persists a client message, then routes it.
// The message is committed before the ack (R-DEL-2).
func (d *Daemon) Ingest(c auth.Principal, kind string, raw json.RawMessage) (*api.SendResult, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, api.Errf(api.CodeParams, "params must be an object")
	}
	for _, k := range forbiddenDraftKeys {
		if _, ok := keys[k]; ok {
			reason := fmt.Sprintf("client-supplied %q is not allowed: the daemon stamps it", k)
			_ = d.store.Update(func(tx *store.Tx) error {
				return tx.Audit("message.rejected_ingress", c.Agent, "", c.RunID, "", map[string]any{"reason": reason, "origin": c.Origin})
			})
			return nil, api.Errf(api.CodeRejected, "%s", reason)
		}
	}
	var p api.SendParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, api.Errf(api.CodeParams, "%v", err)
	}
	if p.Kind == "" {
		p.Kind = kind
	}
	if p.Kind != kind {
		return nil, api.Errf(api.CodeParams, "method expects kind %q, got %q", kind, p.Kind)
	}
	if err := p.Draft.Validate(); err != nil {
		return nil, api.Errf(api.CodeParams, "%v", err)
	}
	if c.IsHuman() && p.Kind == "event" && p.DedupeKey == "" {
		// fine: humans may emit events without a key
	}
	if !d.limiter.Allow(c.Agent) {
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("message.rate_limited", c.Agent, "", c.RunID, "", nil)
		})
		return nil, api.Errf(api.CodeRateLimited, "rate limit: more than %d messages per minute from %s", d.Config().Daemon.RateLimitPerMinute, c.Agent)
	}
	for _, a := range p.Attachments {
		size, _, err := d.store.BlobInfo(a.SHA256)
		if err != nil {
			return nil, api.Errf(api.CodeParams, "attachment %q: unknown blob %s (upload it with blob.put first)", a.Name, a.SHA256)
		}
		if size != a.Size {
			return nil, api.Errf(api.CodeParams, "attachment %q: size %d does not match blob size %d", a.Name, a.Size, size)
		}
	}
	// An ask to a session's name (not an agent) goes to a fork of that
	// session (ADR-0013); nothing is typed into the session (D4).
	var fork *store.SessionAsk
	if d.Config().Agent(p.To) == nil && p.Kind == "ask" {
		if sess := d.sessionByName(p.To); sess != nil {
			agent, cwd, err := d.forkTarget(*sess)
			if err != nil {
				return nil, err
			}
			fork = &store.SessionAsk{SessionID: sess.SessionID, Cwd: cwd}
			p.To = agent
		}
	}
	e := d.stamp(c, &p.Draft)
	return d.acceptFork(e, fork)
}

// stamp builds the envelope from a draft and the authenticated caller.
func (d *Daemon) stamp(c auth.Principal, dr *envelope.Draft) *envelope.Envelope {
	cfg := d.Config()
	now := time.Now().UTC()
	id := envelope.NewID()
	origin := c.Origin
	from := c.Agent
	if c.IsHuman() {
		from, origin = config.Human, envelope.OriginHuman
	}
	e := &envelope.Envelope{ID: id, ThreadID: id, From: envelope.From{Agent: from, Origin: origin, RunID: c.RunID},
		To: dr.To, Kind: dr.Kind, Intent: dr.Intent, Priority: dr.Priority, Subject: dr.Subject, Body: dr.Body,
		Data: dr.Data, Attachments: dr.Attachments, DedupeKey: dr.DedupeKey, Deadline: dr.Deadline, CreatedAt: now}
	if c.RunID != "" {
		// A worker's message continues its run's thread with hop + 1.
		e.ThreadID = c.Thread
		e.Hop = c.Hop + 1
	}
	if e.Deadline == nil && cfg.Daemon.DefaultTTL.Duration > 0 {
		dl := now.Add(cfg.Daemon.DefaultTTL.Duration)
		e.Deadline = &dl
	}
	return e
}

// acceptFork commits e (or coalesces it) and routes it. With fork set, a
// fork of that session answers it: its session_asks row is written in the
// same transaction (ADR-0013).
func (d *Daemon) acceptFork(e *envelope.Envelope, fork *store.SessionAsk) (*api.SendResult, error) {
	cfg := d.Config()
	var res *api.SendResult
	var after []func()
	d.mu.Lock()
	err := d.store.Update(func(tx *store.Tx) error {
		res, after = nil, nil
		recurrence := false
		if e.Kind == "event" && e.DedupeKey != "" {
			open, err := tx.OpenByDedupe(e.To, e.DedupeKey)
			if err != nil {
				return err
			}
			if open != nil {
				res = &api.SendResult{ID: open.Env.ID, ThreadID: open.Env.ThreadID, State: string(open.State), Coalesced: true, CoalescedInto: open.Env.ID}
				return tx.Coalesce(open, e)
			}
			if a := cfg.Agent(e.To); a != nil {
				if r := router.FirstRule(a, e); r != nil && r.Cooldown.Duration > 0 {
					last, err := tx.LastDoneByDedupe(e.To, e.DedupeKey, tx.Now().Add(-r.Cooldown.Duration))
					if err != nil {
						return err
					}
					if last != nil {
						if r.ReopenAfter > 0 && last.Coalesced+1 >= r.ReopenAfter {
							recurrence = true
							if err := tx.ResetCoalesced(last.Env.ID); err != nil {
								return err
							}
						} else {
							res = &api.SendResult{ID: last.Env.ID, ThreadID: last.Env.ThreadID, State: string(last.State), Coalesced: true, CoalescedInto: last.Env.ID}
							return tx.Coalesce(last, e)
						}
					}
				}
			}
		}
		if err := tx.InsertMessage(e, recurrence); err != nil {
			return err
		}
		if fork != nil {
			if err := tx.InsertSessionAsk(e.ID, fork.SessionID, fork.Cwd); err != nil {
				return err
			}
		}
		m, err := tx.GetMessage(e.ID)
		if err != nil {
			return err
		}
		after, err = d.routeTx(tx, cfg, m)
		if err != nil {
			return err
		}
		m, _ = tx.GetMessage(e.ID)
		res = &api.SendResult{ID: e.ID, ThreadID: e.ThreadID, State: string(m.State)}
		if e.Kind == "ask" {
			res.Ticket = e.ID
		}
		return nil
	})
	d.mu.Unlock()
	if err != nil {
		d.log.Error("message not persisted", "error", err.Error(), "to", e.To, "intent", e.Intent)
		return nil, api.Errf(api.CodeUnavailable, "message not persisted: %v", err)
	}
	for _, f := range after {
		f()
	}
	if !res.Coalesced {
		d.log.Info("message accepted", "message_id", res.ID, "thread_id", res.ThreadID, "agent", e.To, "from", e.From.Agent,
			"kind", e.Kind, "intent", e.Intent, "state", res.State)
	}
	d.kick()
	return res, nil
}

// routeTx routes an accepted message. It returns side effects (notifications,
// waiter wake-ups) to run after commit.
func (d *Daemon) routeTx(tx *store.Tx, cfg *config.Config, m *store.Message) ([]func(), error) {
	e := &m.Env
	var after []func()
	// Replies to an ask from an interactive session, the CLI or the human
	// are stored for the asker (await / digest), not routed (RFC 5.3).
	if e.Kind == "reply" {
		ask, _ := d.parentAsk(tx, e)
		if ask == nil || ask.Env.From.Origin != envelope.OriginWorker || e.To == config.Human {
			_, err := tx.Transition(e.ID, store.Done, store.Fields{Handler: store.Str("inbox"), Summary: store.Str(clip(e.Subject, 280))},
				map[string]any{"route": "reply to asker"}, store.Accepted)
			return after, err
		}
	}
	th, err := tx.GetThread(e.ThreadID)
	if err != nil {
		return nil, err
	}
	nThread, err := tx.CountThread(e.ThreadID)
	if err != nil {
		return nil, err
	}
	pair, err := tx.CountPair(e.ThreadID, e.From.Agent, e.To, tx.Now().Add(-cfg.Daemon.PingPongWindow.Duration))
	if err != nil {
		return nil, err
	}
	ownReply := false
	if e.Kind == "reply" {
		if ask, _ := d.parentAsk(tx, e); ask != nil && ask.Env.From.Agent == e.To {
			ownReply = true
		}
	}
	dec := router.Route(cfg, e, router.ThreadStats{Frozen: th.Frozen, FrozenReason: th.FrozenReason, Messages: nThread, PairRecent: pair}, ownReply)
	ruleID := ""
	if dec.Rule != nil {
		ruleID = dec.Rule.ID
	}
	// A fork of a session answers a session ask, whatever the agent's
	// rules say; the guards (hops, loops, accept_from) still reject.
	if e.Kind == "ask" && !dec.Freeze && dec.Action != router.Reject {
		if sa, err := tx.SessionAskTx(e.ID); err != nil {
			return nil, err
		} else if sa != nil {
			dec = router.Decision{Action: router.Queue, Handler: "worker", Profile: SessionAskProfile, Reason: "session ask"}
			ruleID = "session.ask"
		}
	}
	detail := map[string]any{"rule": ruleID, "action": string(dec.Action)}
	if err := tx.Audit("route.decision", e.To, e.ID, "", e.ThreadID, map[string]any{"rule": ruleID, "action": string(dec.Action), "reason": dec.Reason, "handler": dec.Handler}); err != nil {
		return nil, err
	}
	if dec.Freeze {
		if err := tx.FreezeThread(e.ThreadID, dec.Reason); err != nil {
			return nil, err
		}
		// Queued work in a frozen thread stops too.
		qs, err := tx.Messages(`thread_id=? AND state IN ('queued','retry_wait') AND id<>?`, e.ThreadID, e.ID)
		if err != nil {
			return nil, err
		}
		for _, q := range qs {
			if _, err := tx.Transition(q.Env.ID, store.Rejected, store.Fields{Reason: store.Str("thread_frozen: " + dec.Reason)}, nil); err != nil {
				return nil, err
			}
			after = append(after, d.syntheticReplyTx(tx, q, "rejected", "thread frozen: "+dec.Reason)...)
		}
	}
	switch dec.Action {
	case router.Queue:
		_, err = tx.Transition(e.ID, store.Queued, store.Fields{RuleID: &ruleID, Handler: &dec.Handler, Profile: &dec.Profile}, detail, store.Accepted)
	case router.Digest:
		_, err = tx.Transition(e.ID, store.Done, store.Fields{RuleID: &ruleID, Handler: store.Str("digest"), Summary: store.Str(clip(e.Subject, 280))}, detail, store.Accepted)
	case router.Escalate:
		q := fmt.Sprintf("%s %s %q from %s (rule %s)", e.Kind, e.Intent, e.Subject, e.From.Agent, ruleID)
		_, err = tx.Transition(e.ID, store.NeedsHuman, store.Fields{RuleID: &ruleID, Handler: store.Str("escalate"),
			HumanQuestion: &q, Summary: store.Str(clip("Needs you: "+e.Subject, 280))}, detail, store.Accepted)
		agent, id, subj := e.To, e.ID, e.Subject
		after = append(after, func() { d.notify("needs_human", agent, subj+" (from "+e.From.Agent+")", id) })
	case router.Reject:
		_, err = tx.Transition(e.ID, store.Rejected, store.Fields{RuleID: &ruleID, Handler: store.Str(nonEmpty(dec.Handler, "router")), Reason: &dec.Reason}, detail, store.Accepted)
		if err == nil {
			after = append(after, d.syntheticReplyTx(tx, m, "rejected", dec.Reason)...)
		}
		if dec.Notify {
			agent, id, reason := e.To, e.ID, dec.Reason
			after = append(after, func() { d.notify("needs_human", agent, "Escalated: "+reason, id) })
		}
	}
	return after, err
}

// parentAsk returns the ask a reply answers.
func (d *Daemon) parentAsk(tx *store.Tx, e *envelope.Envelope) (*store.Message, error) {
	if e.ParentID == nil {
		return nil, nil
	}
	p, err := tx.GetMessage(*e.ParentID)
	if err != nil {
		return nil, err
	}
	if p.Env.Kind != "ask" {
		return nil, nil
	}
	return p, nil
}

// syntheticReplyTx answers an unanswered ask with a status-only reply
// (rejected, timeout, failed) so the asker is never left waiting.
func (d *Daemon) syntheticReplyTx(tx *store.Tx, ask *store.Message, status, text string) []func() {
	if ask.Env.Kind != "ask" || ask.AnsweredBy != "" {
		return nil
	}
	_, after, err := d.createReplyTx(tx, ask, envelope.From{Agent: ask.Env.To, Origin: envelope.OriginDaemon}, status, text, nil, nil, ask.Env.Hop)
	if err != nil {
		d.log.Error("synthetic reply failed", "message_id", ask.Env.ID, "error", err.Error())
	}
	return after
}

// createReplyTx creates the one reply to an ask, marks the ask answered and
// routes the reply. A second reply to the same ask is ignored.
func (d *Daemon) createReplyTx(tx *store.Tx, ask *store.Message, from envelope.From, status, text string, data json.RawMessage,
	atts []envelope.Attachment, hop int) (*envelope.Envelope, []func(), error) {
	cur, err := tx.GetMessage(ask.Env.ID)
	if err != nil {
		return nil, nil, err
	}
	if cur.AnsweredBy != "" {
		return nil, nil, nil
	}
	id := envelope.NewID()
	parent := ask.Env.ID
	subj := "Re: " + ask.Env.Subject
	if len(subj) > envelope.MaxSubject {
		subj = subj[:envelope.MaxSubject]
	}
	if len(text) > envelope.MaxBody {
		text = text[:envelope.MaxBody]
	}
	r := &envelope.Envelope{ID: id, ThreadID: ask.Env.ThreadID, ParentID: &parent, From: from, To: ask.Env.From.Agent,
		Kind: "reply", Intent: ask.Env.Intent, Priority: ask.Env.Priority, Subject: subj, Body: text, Data: data,
		Attachments: atts, Hop: hop, CreatedAt: time.Now().UTC(), Status: status}
	if err := tx.InsertMessage(r, false); err != nil {
		return nil, nil, err
	}
	if err := tx.SetFields(ask.Env.ID, store.Fields{AnsweredBy: &id}); err != nil {
		return nil, nil, err
	}
	m, err := tx.GetMessage(id)
	if err != nil {
		return nil, nil, err
	}
	after, err := d.routeTx(tx, d.Config(), m)
	askID := ask.Env.ID
	after = append(after, func() { d.wakeWaiters(askID) })
	return r, after, err
}

func (d *Daemon) wakeWaiters(askID string) {
	d.mu.Lock()
	ws := d.waiters[askID]
	delete(d.waiters, askID)
	d.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
}

// expire moves queued messages past their deadline to expired and answers
// overdue asks with a timeout reply (RFC 5.1, 5.3).
func (d *Daemon) expire() {
	now := time.Now()
	ms, err := d.store.Messages(`deadline IS NOT NULL AND deadline<? AND (state IN ('accepted','queued','retry_wait') OR (kind='ask' AND answered_by IS NULL AND state NOT IN ('rejected','expired')))`, now.UnixMilli())
	if err != nil || len(ms) == 0 {
		return
	}
	var after []func()
	d.mu.Lock()
	for _, m := range ms {
		_ = d.store.Update(func(tx *store.Tx) error {
			cur, err := tx.GetMessage(m.Env.ID)
			if err != nil {
				return err
			}
			if cur.State == store.Accepted || cur.State == store.Queued || cur.State == store.RetryWait {
				if _, err := tx.Transition(cur.Env.ID, store.Expired, store.Fields{Reason: store.Str("deadline passed")}, nil); err != nil {
					return err
				}
			}
			after = append(after, d.syntheticReplyTx(tx, cur, "timeout", "no reply before the deadline")...)
			return nil
		})
	}
	d.mu.Unlock()
	for _, f := range after {
		f()
	}
}

// HumanReply answers a needs_human item (RFC 5.3, 7.3 `mailroom reply`).
//
// An ask the rules escalated is answered directly: the text becomes the
// reply to the asker. Anything else continues in the same thread: the text
// goes back to the agent as a reply and its worker runs again.
func (d *Daemon) HumanReply(p api.HumanReplyParams) (*api.SendResult, error) {
	if strings.TrimSpace(p.Text) == "" {
		return nil, api.Errf(api.CodeParams, "text is required")
	}
	m, err := d.store.FindMessage(p.MessageID)
	if err != nil {
		return nil, api.Errf(api.CodeNotFound, "message %s not found", p.MessageID)
	}
	if m.State != store.NeedsHuman {
		return nil, api.Errf(api.CodeRejected, "message %s is %s, not needs_human", m.Env.ID, m.State)
	}
	cfg := d.Config()
	var res *api.SendResult
	var after []func()
	d.mu.Lock()
	err = d.store.Update(func(tx *store.Tx) error {
		after = nil
		human := envelope.From{Agent: config.Human, Origin: envelope.OriginHuman}
		if m.Handler == "escalate" && m.Env.Kind == "ask" {
			r, a, err := d.createReplyTx(tx, m, human, "ok", p.Text, p.Data, nil, 0)
			if err != nil {
				return err
			}
			after = append(after, a...)
			if _, err := tx.Transition(m.Env.ID, store.Done, store.Fields{Summary: store.Str("answered by the human")}, map[string]any{"human": true}, store.NeedsHuman); err != nil {
				return err
			}
			if r != nil {
				res = &api.SendResult{ID: r.ID, ThreadID: r.ThreadID, State: "done"}
			}
			return nil
		}
		id := envelope.NewID()
		parent := m.Env.ID
		c := &envelope.Envelope{ID: id, ThreadID: m.Env.ThreadID, ParentID: &parent, From: human, To: m.Env.To,
			Kind: "reply", Intent: m.Env.Intent, Priority: m.Env.Priority, Subject: clip("Re: "+m.Env.Subject, envelope.MaxSubject),
			Body: p.Text, Data: p.Data, Hop: 0, CreatedAt: time.Now().UTC(), Status: "ok"}
		if err := tx.InsertMessage(c, false); err != nil {
			return err
		}
		if _, err := tx.Transition(m.Env.ID, store.Done, store.Fields{Summary: store.Str("human answered; continued in thread")}, map[string]any{"human": true, "continuation": id}, store.NeedsHuman); err != nil {
			return err
		}
		cm, _ := tx.GetMessage(id)
		if m.Handler == "worker" && cfg.Agent(m.Env.To) != nil {
			// Continue with the same worker profile, regardless of reply rules.
			_, err := tx.Transition(id, store.Queued, store.Fields{RuleID: store.Str(m.RuleID), Handler: store.Str("worker"), Profile: store.Str(m.Profile)},
				map[string]any{"route": "human answer continues the worker"}, store.Accepted)
			if err != nil {
				return err
			}
		} else {
			a, err := d.routeTx(tx, cfg, cm)
			if err != nil {
				return err
			}
			after = append(after, a...)
		}
		cm, _ = tx.GetMessage(id)
		res = &api.SendResult{ID: id, ThreadID: c.ThreadID, State: string(cm.State)}
		return nil
	})
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, f := range after {
		f()
	}
	d.kick()
	return res, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
