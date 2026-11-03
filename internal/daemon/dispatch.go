package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/config"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/runner"
	"github.com/appnice/mailroom/internal/scheduler"
	"github.com/appnice/mailroom/internal/store"
	"github.com/appnice/mailroom/internal/workspace"
)

// dispatchLoop starts handlers as soon as a queue allows (R-DEL-1). It is
// woken by every state change and by a timer for the next not_before.
func (d *Daemon) dispatchLoop() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next := d.dispatch()
		wait := time.Hour
		if !next.IsZero() {
			wait = time.Until(next)
			if wait < 10*time.Millisecond {
				wait = 10 * time.Millisecond
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		case <-timer.C:
		}
	}
}

// dispatch starts every runnable message the caps allow and returns when
// the next delayed message becomes runnable.
func (d *Daemon) dispatch() time.Time {
	if d.ctx.Err() != nil {
		return time.Time{}
	}
	cfg := d.Config()
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	// Retry backoff elapsed: back to the queue.
	if rw, err := d.store.Messages(`state='retry_wait' AND not_before<=?`, now.UnixMilli()); err == nil {
		for _, m := range rw {
			_ = d.store.Update(func(tx *store.Tx) error {
				_, err := tx.Transition(m.Env.ID, store.Queued, store.Fields{}, map[string]any{"retry": true}, store.RetryWait)
				return err
			})
		}
	}
	queued, err := d.store.Messages(`state='queued'`)
	if err != nil {
		d.log.Error("dispatch query failed", "error", err.Error())
		return now.Add(time.Second)
	}
	var next time.Time
	setNext := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	byID := map[string]*store.Message{}
	var items []scheduler.Item
	for _, m := range queued {
		if m.NotBefore.After(now) {
			setNext(m.NotBefore)
			continue
		}
		byID[m.Env.ID] = m
		items = append(items, scheduler.Item{ID: m.Env.ID, Agent: m.Env.To, Handler: m.Handler,
			Priority: config.PriorityLevel(m.Env.Priority), CreatedAt: m.CreatedAt})
	}
	if rw, err := d.store.Messages(`state='retry_wait' ORDER BY not_before LIMIT 1`); err == nil && len(rw) > 0 {
		setNext(rw[0].NotBefore)
	}
	scheduler.Order(items, now, cfg.Daemon.AgingStep.Duration)
	for _, it := range items {
		m := byID[it.ID]
		if !d.slots.CanStart(it) {
			continue
		}
		if hold := d.checkHold(cfg, m, now); hold != "" {
			if m.HoldReason != hold {
				_ = d.store.Update(func(tx *store.Tx) error {
					if err := tx.SetFields(m.Env.ID, store.Fields{HoldReason: &hold}); err != nil {
						return err
					}
					return tx.Audit("message.held", m.Env.To, m.Env.ID, "", m.Env.ThreadID, map[string]any{"reason": hold})
				})
			}
			continue
		}
		d.startLocked(cfg, it, m)
	}
	return next
}

// checkHold returns why a queued message cannot start now ("" = it can).
// It may also move the message (rejected, needs_human) and returns "moved".
func (d *Daemon) checkHold(cfg *config.Config, m *store.Message, now time.Time) string {
	a := cfg.Agent(m.Env.To)
	if a == nil {
		_ = d.store.Update(func(tx *store.Tx) error {
			_, err := tx.Transition(m.Env.ID, store.Rejected, store.Fields{Reason: store.Str("agent_removed")}, nil)
			if err == nil {
				for _, f := range d.syntheticReplyTx(tx, m, "rejected", "agent removed from config") {
					go f()
				}
			}
			return err
		})
		return "moved"
	}
	var frozen bool
	_ = d.store.Update(func(tx *store.Tx) error {
		th, err := tx.GetThread(m.Env.ThreadID)
		if err == nil && th.Frozen {
			frozen = true
			if _, err := tx.Transition(m.Env.ID, store.Rejected, store.Fields{Reason: store.Str("thread_frozen: " + th.FrozenReason)}, nil); err != nil {
				return err
			}
			for _, f := range d.syntheticReplyTx(tx, m, "rejected", "thread frozen") {
				go f()
			}
		}
		return nil
	})
	if frozen {
		return "moved"
	}
	if m.Handler != "worker" {
		return ""
	}
	if !d.isClaudeOK() {
		return "claude unavailable"
	}
	// Daily budget (RFC 6.7).
	if exhausted, why := d.budgetExhausted(a, now); exhausted {
		if a.OnBudget == "escalate" {
			_ = d.store.Update(func(tx *store.Tx) error {
				q := "Daily budget exhausted (" + why + "). Reply to run it anyway, or wait for tomorrow."
				_, err := tx.Transition(m.Env.ID, store.NeedsHuman, store.Fields{HumanQuestion: &q, Summary: store.Str("held: budget exhausted"),
					Reason: store.Str("budget")}, map[string]any{"budget": why}, store.Queued)
				return err
			})
			go d.notifyOnce("budget", a.ID, a.ID+": daily budget exhausted ("+why+"); new worker runs escalate to you", "budget:"+a.ID+":"+day(now))
			return "moved"
		}
		midnight := nextMidnight(now)
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.SetFields(m.Env.ID, store.Fields{NotBefore: &midnight})
		})
		go d.notifyOnce("budget", a.ID, a.ID+": daily budget exhausted ("+why+"); worker runs wait until midnight", "budget:"+a.ID+":"+day(now))
		return "budget exhausted until midnight"
	}
	p, err := cfg.EffectiveProfile(a.ID, m.Profile)
	if err != nil {
		return "profile: " + err.Error()
	}
	if p.WriteScope == "direct" {
		repo := a.RepoPath()
		if _, locked := d.directLocks[repo]; locked {
			return "repo locked by another direct run"
		}
		if pres, _ := d.store.ActivePresence(repo, cfg.Daemon.PresenceTTL.Duration); len(pres) > 0 && a.DirectConflict == "hold" {
			return "interactive session active on " + repo
		}
	}
	return ""
}

func day(t time.Time) string { return t.Local().Format("2006-01-02") }

func nextMidnight(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, time.Local)
}

func (d *Daemon) budgetExhausted(a *config.Agent, now time.Time) (bool, string) {
	if a.BudgetUSD <= 0 && a.BudgetTokens <= 0 {
		return false, ""
	}
	cost, tokens := d.store.Usage(a.ID, day(now))
	if a.BudgetUSD > 0 && cost >= a.BudgetUSD {
		return true, fmt.Sprintf("$%.2f of $%.2f", cost, a.BudgetUSD)
	}
	if a.BudgetTokens > 0 && tokens >= a.BudgetTokens {
		return true, fmt.Sprintf("%d of %d tokens", tokens, a.BudgetTokens)
	}
	return false, ""
}

// startLocked moves a message to running, records the run and launches it.
// Caller holds d.mu.
func (d *Daemon) startLocked(cfg *config.Config, it scheduler.Item, m *store.Message) {
	a := cfg.Agent(m.Env.To)
	runID := envelope.NewID()
	var prof config.Profile
	scope := "none"
	if m.Handler == "worker" {
		p, err := cfg.EffectiveProfile(a.ID, m.Profile)
		if err != nil {
			return
		}
		prof = p
		scope = p.WriteScope
		if scope == "direct" {
			if pres, _ := d.store.ActivePresence(a.RepoPath(), cfg.Daemon.PresenceTTL.Duration); len(pres) > 0 && a.DirectConflict == "worktree" {
				scope = "worktree"
				prof.WriteScope = "worktree"
			}
		}
	}
	attempt := m.Attempt + 1
	err := d.store.Update(func(tx *store.Tx) error {
		if _, err := tx.Transition(m.Env.ID, store.Running, store.Fields{Attempt: &attempt, HoldReason: store.Str("")},
			map[string]any{"run_id": runID, "attempt": attempt}, store.Queued); err != nil {
			return err
		}
		return tx.InsertRun(&store.Run{ID: runID, MessageID: m.Env.ID, Agent: a.ID, Handler: m.Handler, Profile: m.Profile, Attempt: attempt})
	})
	if err != nil {
		d.log.Error("start failed", "message_id", m.Env.ID, "error", err.Error())
		return
	}
	d.slots.Take(it, runID)
	ctx, cancel := context.WithCancelCause(d.ctx)
	ar := &activeRun{id: runID, msgID: m.Env.ID, agent: a.ID, handler: m.Handler, cancel: cancel}
	if scope == "direct" {
		ar.repo = a.RepoPath()
		d.directLocks[ar.repo] = runID
	}
	d.running[runID] = ar
	m.Attempt = attempt
	d.log.Info("run started", "run_id", runID, "message_id", m.Env.ID, "thread_id", m.Env.ThreadID, "agent", a.ID,
		"handler", m.Handler, "profile", m.Profile, "attempt", attempt, "scope", scope)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.execute(ctx, ar, a, m, prof, scope)
	}()
}

// sessionState is agents/<agent>/session.json.
type sessionState struct {
	Sessions map[string]*workerSession `json:"sessions"` // keyed by workspace dir
	Archived []archivedSession         `json:"archived"`
}

type workerSession struct {
	Current         string `json:"current"`
	Runs            int    `json:"runs"`
	LastInputTokens int64  `json:"last_input_tokens"`
	ForceRotate     bool   `json:"force_rotate"`
}

type archivedSession struct {
	ID         string    `json:"id"`
	Workspace  string    `json:"workspace"`
	ArchivedAt time.Time `json:"archived_at"`
	Reason     string    `json:"reason"`
}

func (d *Daemon) agentDir(agent string) string { return filepath.Join(d.dataDir, "agents", agent) }

func (d *Daemon) loadSessions(agent string) *sessionState {
	st := &sessionState{Sessions: map[string]*workerSession{}}
	b, err := os.ReadFile(filepath.Join(d.agentDir(agent), "session.json"))
	if err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Sessions == nil {
		st.Sessions = map[string]*workerSession{}
	}
	return st
}

func (d *Daemon) saveSessions(agent string, st *sessionState) {
	dir := d.agentDir(agent)
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := filepath.Join(dir, "session.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "session.json"))
	}
}

func (d *Daemon) memoryPath(agent string) string {
	return filepath.Join(d.agentDir(agent), "memory.md")
}

// execute runs one handler and applies its outcome.
func (d *Daemon) execute(ctx context.Context, ar *activeRun, a *config.Agent, m *store.Message, prof config.Profile, scope string) {
	cfg := d.Config()
	runDir := filepath.Join(d.dataDir, "runs", ar.id)
	blobDir := filepath.Join(runDir, "attachments")
	transcript := filepath.Join(d.dataDir, "transcripts", a.ID, ar.id+".jsonl")
	_ = os.MkdirAll(filepath.Dir(transcript), 0o700)
	defer os.RemoveAll(runDir)

	req := &runner.Request{Agent: a, Message: &m.Env, Attempt: m.Attempt, RunID: ar.id, Profile: prof,
		BlobDir: blobDir, Transcript: transcript,
		OnStart: func(pid, pgid int) {
			live := ""
			if m.Handler == "worker" {
				live = transcript
			}
			_ = d.store.SetRunProcess(ar.id, pid, pgid, live)
		},
	}
	var outcome *runner.Outcome
	var ws *workspace.Workspace
	var archived string
	var wsess *workerSession
	var sessions *sessionState

	atts, err := d.blobs.CopyReadOnly(m.Env.Attachments, blobDir)
	if err != nil {
		outcome = &runner.Outcome{Failure: runner.FailExit, Error: "attachments: " + err.Error()}
	}
	req.Attachment = atts

	if outcome == nil && m.Handler == "command" {
		rule := d.ruleByID(a, m.RuleID)
		if rule == nil {
			outcome = &runner.Outcome{Failure: runner.FailExit, Error: "rule " + m.RuleID + " no longer exists"}
		} else {
			req.Workspace = a.RepoPath()
			req.Command = rule.Command
			req.Timeout = int(rule.Timeout.Seconds())
			if req.Timeout == 0 {
				req.Timeout = int(cfg.Defaults.CommandTimeout.Seconds())
			}
			outcome = d.commands.Run(ctx, req)
		}
	}
	if outcome == nil && m.Handler == "worker" {
		ws, err = d.ws.Prepare(a, scope, m.Env.ThreadID, d.store.ThreadBranch(m.Env.ThreadID, a.ID))
		if err != nil {
			outcome = &runner.Outcome{Failure: runner.FailExit, Error: "workspace: " + err.Error()}
		}
	}
	if outcome == nil && m.Handler == "worker" {
		req.Workspace = ws.Dir
		if ws.Branch != "" {
			_ = d.store.Update(func(tx *store.Tx) error {
				if _, err := tx.Exec(`UPDATE runs SET workspace=?, branch=?, base_sha=? WHERE id=?`, ws.Dir, ws.Branch, ws.BaseSHA, ar.id); err != nil {
					return err
				}
				return tx.SetThreadBranch(m.Env.ThreadID, a.ID, ws.Branch)
			})
		} else {
			_ = d.store.Update(func(tx *store.Tx) error {
				_, err := tx.Exec(`UPDATE runs SET workspace=? WHERE id=?`, ws.Dir, ar.id)
				return err
			})
		}
		// A session ask resumes a fork of the interactive session in its
		// folder: the session's context, a new session id, the session
		// itself untouched (ADR-0013). No worker session or memory.
		if sa, _ := d.store.SessionAsk(m.Env.ID); sa != nil {
			req.Workspace, req.SessionID, req.ForkSession = sa.Cwd, sa.SessionID, true
			req.OnSession = func(id string) { _ = d.store.SetRunSession(ar.id, id) }
			outcome = d.claude.Run(ctx, req)
		}
	}
	if outcome == nil && m.Handler == "worker" {
		sessions = d.loadSessions(a.ID)
		wsess = sessions.Sessions[ws.Dir]
		if wsess == nil {
			wsess = &workerSession{}
			sessions.Sessions[ws.Dir] = wsess
		}
		memory, _ := os.ReadFile(d.memoryPath(a.ID))
		// Rotation before the context overflows (RFC 6.8).
		if wsess.Current != "" && (wsess.ForceRotate || wsess.LastInputTokens > cfg.Defaults.RotateAtTokens || wsess.Runs >= cfg.Defaults.RotateAfterRuns) {
			reason := fmt.Sprintf("input tokens %d, runs %d, forced %v", wsess.LastInputTokens, wsess.Runs, wsess.ForceRotate)
			creq := *req
			creq.SessionID = wsess.Current
			creq.Workspace = ws.Dir
			mem, co := d.claude.Compact(ctx, &creq, string(memory))
			if co.Failure == runner.FailNone && mem != "" {
				_ = os.MkdirAll(d.agentDir(a.ID), 0o700)
				_ = os.WriteFile(d.memoryPath(a.ID), []byte(mem), 0o600)
				memory = []byte(mem)
			} else {
				d.log.Warn("compaction failed; rotating without a new memory file", "agent", a.ID, "run_id", ar.id, "error", co.Error)
			}
			archived = wsess.Current
			sessions.Archived = append(sessions.Archived, archivedSession{ID: archived, Workspace: ws.Dir, ArchivedAt: time.Now(), Reason: reason})
			*wsess = workerSession{}
			d.saveSessions(a.ID, sessions)
			_ = d.store.Update(func(tx *store.Tx) error {
				return tx.Audit("session.rotated", a.ID, m.Env.ID, ar.id, m.Env.ThreadID, map[string]any{"archived": archived, "reason": reason, "cost_usd": co.CostUSD})
			})
			if co.CostUSD > 0 {
				_ = d.store.Update(func(tx *store.Tx) error {
					return tx.AddUsage(a.ID, day(time.Now()), co.CostUSD, co.InputTokens+co.OutputTokens)
				})
			}
		}
		req.SessionID = wsess.Current
		req.Memory = string(memory)
		req.OnSession = func(id string) { _ = d.store.SetRunSession(ar.id, id) }
		if prof.OnwardMCP {
			req.RunToken = d.reg.MintRun(authPrincipal(a.ID, ar.id, m))
			ar.runToken = req.RunToken
		}
		outcome = d.claude.Run(ctx, req)
		if ar.runToken != "" {
			d.reg.RevokeRun(ar.runToken)
		}
	}

	// Commit any worktree changes, whatever happened (RFC 6.4, 8.4).
	var head, diffstat string
	if ws != nil && ws.Scope == "worktree" {
		summary := ""
		if outcome.Result != nil {
			summary = outcome.Result.Summary
		} else {
			summary = "interrupted run (" + string(outcome.Failure) + ")"
		}
		head, diffstat, err = d.ws.Finish(ws, summary, ar.id)
		if err != nil {
			d.log.Error("commit-on-exit failed", "run_id", ar.id, "error", err.Error())
		}
	}
	if wsess != nil {
		if outcome.SessionID != "" {
			wsess.Current = outcome.SessionID
		}
		wsess.Runs++
		if outcome.InputTokens > 0 {
			wsess.LastInputTokens = outcome.InputTokens
		}
		switch outcome.Failure {
		case runner.FailContext:
			wsess.ForceRotate = true
		case runner.FailSessionLost:
			*wsess = workerSession{}
		}
		d.saveSessions(a.ID, sessions)
	}
	d.finish(ctx, ar, a, m, outcome, ws, head, diffstat, archived)
}

func authPrincipal(agent, runID string, m *store.Message) (p authP) {
	return authP{Agent: agent, RunID: runID, Hop: m.Env.Hop, Thread: m.Env.ThreadID}
}

func (d *Daemon) ruleByID(a *config.Agent, id string) *config.Rule {
	for _, r := range a.Rules {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// finish records the run and moves the message (RFC 5.2, 6.7, 8.4).
func (d *Daemon) finish(ctx context.Context, ar *activeRun, a *config.Agent, m *store.Message, o *runner.Outcome,
	ws *workspace.Workspace, head, diffstat, archived string) {
	cfg := d.Config()
	cause := context.Cause(ctx)
	end := store.RunEnd{SessionID: o.SessionID, ArchivedSession: archived, HeadSHA: head, Diffstat: diffstat,
		UsageJSON: o.UsageJSON, CostUSD: o.CostUSD, InputTokens: o.InputTokens, OutputTokens: o.OutputTokens,
		Error: d.reg.Redact(o.Error), Stderr: d.reg.Redact(o.Stderr)}
	if ws != nil {
		end.Branch = ws.Branch
	}
	if m.Handler == "worker" {
		end.Transcript = filepath.Join(d.dataDir, "transcripts", a.ID, ar.id+".jsonl")
	}
	if o.Result != nil {
		b, _ := json.Marshal(o.Result)
		end.ResultJSON = string(b)
	}
	// Artifacts go to the blob store before the transaction.
	var artifacts []envelope.Attachment
	if o.Result != nil && len(o.Result.Artifacts) > 0 {
		base := a.RepoPath()
		if ws != nil {
			base = ws.Dir
		}
		for _, art := range o.Result.Artifacts {
			p := filepath.Join(base, filepath.Clean(art.Path))
			if rel, err := filepath.Rel(base, p); err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			sha, size, mime, err := d.blobs.PutFile(p)
			if err != nil {
				d.log.Warn("artifact skipped", "run_id", ar.id, "path", art.Path, "error", err.Error())
				continue
			}
			artifacts = append(artifacts, envelope.Attachment{Name: envelope.SanitizeName(filepath.Base(art.Path)), SHA256: sha, Size: size, MIME: mime})
		}
	}

	var after []func()
	d.mu.Lock()
	err := d.store.Update(func(tx *store.Tx) error {
		after = nil
		for _, at := range artifacts {
			if err := tx.PutBlob(at.SHA256, at.Size, at.MIME); err != nil {
				return err
			}
		}
		if o.CostUSD > 0 || o.InputTokens > 0 {
			if err := tx.AddUsage(a.ID, day(time.Now()), o.CostUSD, o.InputTokens+o.OutputTokens); err != nil {
				return err
			}
		}
		cur, err := tx.GetMessage(m.Env.ID)
		if err != nil {
			return err
		}
		switch {
		case errors.Is(cause, errShutdown):
			end.State = store.RunKilled
			end.Error = "daemon shutdown; requeued"
			if err := tx.FinishRun(ar.id, end); err != nil {
				return err
			}
			if cur.State == store.Running {
				// Not the handler's fault: requeue without counting the attempt.
				now := tx.Now()
				_, err = tx.Transition(m.Env.ID, store.RetryWait, store.Fields{Attempt: store.Int(cur.Attempt - 1), NotBefore: &now, Reason: store.Str("daemon shutdown")}, nil, store.Running)
			}
			return err
		case errors.Is(cause, errCancelled) || cur.State != store.Running:
			end.State = store.RunCancelled
			if err := tx.FinishRun(ar.id, end); err != nil {
				return err
			}
			if cur.State == store.Running {
				if _, err := tx.Transition(m.Env.ID, store.Rejected, store.Fields{Reason: store.Str("cancelled")}, map[string]any{"human": true}, store.Running); err != nil {
					return err
				}
				after = append(after, d.syntheticReplyTx(tx, cur, "rejected", "cancelled by the human")...)
			}
			return nil
		}
		if o.Result != nil && o.Result.Status != "failed" {
			res := o.Result
			if res.Status == "needs_human" {
				end.State = store.RunNeedsHuman
			} else {
				end.State = store.RunSucceeded
			}
			if err := tx.FinishRun(ar.id, end); err != nil {
				return err
			}
			// Reply to the ask this run answers (RFC 5.3).
			if res.Reply != "" || len(res.ReplyData) > 0 || len(artifacts) > 0 || (m.Env.Kind == "ask" && res.Status == "done") {
				if target := d.askToAnswer(tx, a.ID, cur); target != nil {
					_, af, err := d.createReplyTx(tx, target, envelope.From{Agent: a.ID, Origin: envelope.OriginWorker, RunID: ar.id},
						"ok", nonEmpty(res.Reply, res.Summary), res.ReplyData, artifacts, cur.Env.Hop+1)
					if err != nil {
						return err
					}
					after = append(after, af...)
				}
			}
			// Followups go out with hop + 1 in the same thread (RFC 6.3, D3).
			for _, f := range res.Followups {
				fe := &envelope.Envelope{ID: envelope.NewID(), ThreadID: cur.Env.ThreadID, From: envelope.From{Agent: a.ID, Origin: envelope.OriginWorker, RunID: ar.id},
					To: f.To, Kind: f.Kind, Intent: f.Intent, Priority: nonEmpty(f.Priority, "normal"), Subject: clip(f.Subject, envelope.MaxSubject),
					Body: f.Body, Data: f.Data, Hop: cur.Env.Hop + 1, CreatedAt: time.Now().UTC()}
				if dl := cfg.Daemon.DefaultTTL.Duration; dl > 0 {
					t := fe.CreatedAt.Add(dl)
					fe.Deadline = &t
				}
				if err := tx.InsertMessage(fe, false); err != nil {
					return err
				}
				fm, _ := tx.GetMessage(fe.ID)
				af, err := d.routeTx(tx, cfg, fm)
				if err != nil {
					return err
				}
				after = append(after, af...)
			}
			if res.Status == "needs_human" {
				q := res.HumanQuestion
				_, err := tx.Transition(m.Env.ID, store.NeedsHuman, store.Fields{Summary: store.Str(clip(res.Summary, 280)), HumanQuestion: &q}, map[string]any{"run_id": ar.id}, store.Running)
				if err != nil {
					return err
				}
				agent, id, s := a.ID, m.Env.ID, res.Summary
				after = append(after, func() { d.notify("needs_human", agent, s, id) })
				return nil
			}
			_, err := tx.Transition(m.Env.ID, store.Done, store.Fields{Summary: store.Str(clip(res.Summary, 280))}, map[string]any{"run_id": ar.id}, store.Running)
			return err
		}
		// Failure paths.
		failure := o.Failure
		reason := o.Error
		if o.Result != nil && o.Result.Status == "failed" {
			failure, reason = runner.FailHandlerFailed, "handler reported failure: "+o.Result.Summary
		}
		switch failure {
		case runner.FailTimeout:
			end.State = store.RunTimeout
		default:
			end.State = store.RunFailed
		}
		if end.Error == "" {
			end.Error = reason
		}
		if err := tx.FinishRun(ar.id, end); err != nil {
			return err
		}
		switch failure {
		case runner.FailOverload, runner.FailSessionLost:
			// Not counted toward max_attempts, up to overload_extra (RFC 8.4).
			if cur.ExtraAttempts < cfg.Daemon.OverloadExtra {
				nb := tx.Now().Add(backoff(cfg, cur.ExtraAttempts+1))
				_, err := tx.Transition(m.Env.ID, store.RetryWait, store.Fields{Attempt: store.Int(cur.Attempt - 1), ExtraAttempts: store.Int(cur.ExtraAttempts + 1),
					NotBefore: &nb, Reason: store.Str(string(failure) + ": " + reason)}, nil, store.Running)
				return err
			}
		case runner.FailUnavailable:
			go d.setClaudeDown(reason)
			nb := tx.Now()
			_, err := tx.Transition(m.Env.ID, store.RetryWait, store.Fields{Attempt: store.Int(cur.Attempt - 1), NotBefore: &nb,
				Reason: store.Str("claude unavailable: " + reason)}, nil, store.Running)
			return err
		}
		return d.failAttemptTxAfter(tx, cfg, cur, string(failure)+": "+reason, &after)
	})
	// Release the slot and lock.
	d.slots.Release(ar.handler, ar.agent)
	delete(d.running, ar.id)
	if ar.repo != "" && d.directLocks[ar.repo] == ar.id {
		delete(d.directLocks, ar.repo)
	}
	d.mu.Unlock()
	if err != nil {
		d.log.Error("finishing run failed", "run_id", ar.id, "message_id", m.Env.ID, "error", err.Error())
	}
	for _, f := range after {
		f()
	}
	d.log.Info("run finished", "run_id", ar.id, "message_id", m.Env.ID, "thread_id", m.Env.ThreadID, "agent", a.ID,
		"state", end.State, "failure", string(o.Failure), "cost_usd", o.CostUSD)
	d.kick()
}

// askToAnswer finds the ask a run's reply resolves: the message itself if
// it is an ask, or, for a human answer continuing an escalated ask to this
// agent, that ask.
func (d *Daemon) askToAnswer(tx *store.Tx, agent string, m *store.Message) *store.Message {
	if m.Env.Kind == "ask" {
		if m.AnsweredBy == "" {
			return m
		}
		return nil
	}
	if m.Env.Kind == "reply" && m.Env.ParentID != nil {
		p, err := tx.GetMessage(*m.Env.ParentID)
		if err == nil && p.Env.Kind == "ask" && p.Env.To == agent && p.AnsweredBy == "" {
			return p
		}
	}
	return nil
}

func backoff(cfg *config.Config, attempt int) time.Duration {
	b := cfg.Daemon.RetryBackoff
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(b) {
		return b[len(b)-1].Duration
	}
	return b[attempt-1].Duration
}

// failAttemptTx schedules a retry or dead-letters the message.
func (d *Daemon) failAttemptTx(tx *store.Tx, cfg *config.Config, m *store.Message, reason string, _ bool) error {
	var after []func()
	err := d.failAttemptTxAfter(tx, cfg, m, reason, &after)
	for _, f := range after {
		go f()
	}
	return err
}

func (d *Daemon) failAttemptTxAfter(tx *store.Tx, cfg *config.Config, m *store.Message, reason string, after *[]func()) error {
	max := cfg.Defaults.MaxAttempts
	if a := cfg.Agent(m.Env.To); a != nil {
		max = a.MaxAttempts
	}
	if m.Attempt >= max {
		if _, err := tx.Transition(m.Env.ID, store.DeadLetter, store.Fields{Reason: store.Str(clip(reason, 1000))},
			map[string]any{"attempts": m.Attempt}, store.Running); err != nil {
			return err
		}
		*after = append(*after, d.syntheticReplyTx(tx, m, "failed", "handler failed after "+fmt.Sprint(m.Attempt)+" attempts")...)
		agent, id, subj := m.Env.To, m.Env.ID, m.Env.Subject
		*after = append(*after, func() {
			d.notify("dead_letter", agent, fmt.Sprintf("Dead letter after %d attempts: %s (%s)", m.Attempt, subj, clip(reason, 120)), id)
		})
		return nil
	}
	nb := tx.Now().Add(backoff(cfg, m.Attempt))
	_, err := tx.Transition(m.Env.ID, store.RetryWait, store.Fields{NotBefore: &nb, Reason: store.Str(clip(reason, 1000))},
		map[string]any{"attempt": m.Attempt, "retry_at": nb}, store.Running)
	return err
}

// Cancel cancels a queued, waiting or running message. It returns the
// state the item has after the call: api.CancelRejected when the item is
// rejected at once, api.CancelCancelling when a running worker must stop
// first (the outcome then shows in thread.get).
func (d *Daemon) Cancel(id string) (string, error) {
	m, err := d.store.FindMessage(id)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch m.State {
	case store.Queued, store.RetryWait, store.NeedsHuman, store.Accepted:
		var after []func()
		err := d.store.Update(func(tx *store.Tx) error {
			if _, err := tx.Transition(m.Env.ID, store.Rejected, store.Fields{Reason: store.Str("cancelled")}, map[string]any{"human": true}); err != nil {
				return err
			}
			after = d.syntheticReplyTx(tx, m, "rejected", "cancelled by the human")
			return nil
		})
		for _, f := range after {
			go f()
		}
		if err != nil {
			return "", err
		}
		return api.CancelRejected, nil
	case store.Running:
		for _, r := range d.running {
			if r.msgID == m.Env.ID {
				r.cancel(errCancelled)
				return api.CancelCancelling, nil
			}
		}
		return "", fmt.Errorf("message %s is running but no live run was found", m.Env.ID)
	default:
		return "", fmt.Errorf("message %s is %s and cannot be cancelled", m.Env.ID, m.State)
	}
}

// Retry requeues a dead letter.
func (d *Daemon) Retry(id string) error {
	m, err := d.store.FindMessage(id)
	if err != nil {
		return err
	}
	d.mu.Lock()
	err = d.store.Update(func(tx *store.Tx) error {
		now := tx.Now()
		_, err := tx.Transition(m.Env.ID, store.Queued, store.Fields{Attempt: store.Int(0), ExtraAttempts: store.Int(0), NotBefore: &now,
			Reason: store.Str("")}, map[string]any{"human": true, "retry": true}, store.DeadLetter)
		return err
	})
	d.mu.Unlock()
	d.kick()
	return err
}
