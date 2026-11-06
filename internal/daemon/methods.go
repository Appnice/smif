package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/envelope"
	"github.com/appnice/smif/internal/store"
	"github.com/appnice/smif/internal/workspace"
)

type authP = auth.Principal

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return api.Errf(api.CodeParams, "%v", err)
	}
	return nil
}

func humanOnly(c api.Caller) error {
	if !c.IsHuman() {
		return api.Errf(api.CodeForbidden, "this method is for the human (CLI); agents cannot read inbound mail")
	}
	return nil
}

func (d *Daemon) registerMethods() {
	s := d.srv
	send := func(kind string) api.Handler {
		return func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
			res, err := d.Ingest(c, kind, raw)
			if err != nil {
				return nil, err
			}
			if kind == "ask" {
				var p struct {
					WaitS int `json:"wait_s"`
				}
				_ = json.Unmarshal(raw, &p)
				if p.WaitS > 0 && !res.Coalesced {
					ar, err := d.await(ctx, c, res.ID, p.WaitS)
					if err == nil && ar.Reply != nil {
						res.Reply = ar.Reply
					}
				}
			}
			return res, nil
		}
	}
	s.Handle("message.send", send("tell"))
	s.Handle("message.ask", send("ask"))
	s.Handle("message.emit", send("event"))
	s.Handle("message.await", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		var p api.AwaitParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.await(ctx, c, p.Ticket, p.WaitS)
	})
	s.Handle("message.reply", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.HumanReplyParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.HumanReply(p)
	})
	s.Handle("message.retry", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := d.Retry(p.ID); err != nil {
			return nil, api.Errf(api.CodeRejected, "%v", err)
		}
		return map[string]string{"id": p.ID, "state": "queued"}, nil
	})
	s.Handle("message.cancel", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		state, err := d.Cancel(p.ID)
		if err != nil {
			return nil, api.Errf(api.CodeRejected, "%v", err)
		}
		return api.CancelResult{ID: p.ID, State: state}, nil
	})
	s.Handle("thread.get", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.threadView(p.ID)
	})
	s.Handle("thread.list", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.ListParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.listMessages(p)
	})
	s.Handle("run.get", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		r, err := d.store.GetRun(p.ID)
		if err != nil {
			return nil, api.Errf(api.CodeNotFound, "run %s not found", p.ID)
		}
		return runView(r), nil
	})
	s.Handle("run.list", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.ListParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		where, args := []string{"1=1"}, []any{}
		if p.Agent != "" {
			where, args = append(where, "agent=?"), append(args, p.Agent)
		}
		if p.MessageID != "" {
			where, args = append(where, "message_id=?"), append(args, p.MessageID)
		}
		if p.State != "" {
			where, args = append(where, "state=?"), append(args, p.State)
		}
		if p.Limit <= 0 {
			p.Limit = 50
		}
		args = append(args, p.Limit)
		rs, err := d.store.Runs(strings.Join(where, " AND ")+" ORDER BY started_at DESC LIMIT ?", args...)
		if err != nil {
			return nil, err
		}
		out := []api.RunView{}
		for _, r := range rs {
			out = append(out, runView(r))
		}
		return out, nil
	})
	s.Handle("run.transcript", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		r, err := d.store.GetRun(p.ID)
		if err != nil {
			return nil, api.Errf(api.CodeNotFound, "run %s not found", p.ID)
		}
		return map[string]string{"run_id": r.ID, "path": r.Transcript, "stderr": r.Stderr, "session_id": r.SessionID}, nil
	})
	runBranch := func(id string) (*store.Run, *config.Agent, error) {
		r, err := d.store.GetRun(id)
		if err != nil {
			return nil, nil, api.Errf(api.CodeNotFound, "run %s not found", id)
		}
		if r.Branch == "" {
			return nil, nil, api.Errf(api.CodeRejected, "run %s has no worker branch (write_scope was not worktree)", r.ID)
		}
		a := d.Config().Agent(r.Agent)
		if a == nil {
			return nil, nil, api.Errf(api.CodeNotFound, "agent %s is no longer configured", r.Agent)
		}
		return r, a, nil
	}
	s.Handle("run.diff", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		r, a, err := runBranch(p.ID)
		if err != nil {
			return nil, err
		}
		diff, err := workspace.Diff(a.RepoPath(), r.Branch)
		if err != nil {
			return nil, api.Errf(api.CodeInternal, "%v", err)
		}
		return map[string]string{"run_id": r.ID, "branch": r.Branch, "repo": a.RepoPath(), "diff": diff}, nil
	})
	s.Handle("run.apply", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		r, a, err := runBranch(p.ID)
		if err != nil {
			return nil, err
		}
		head, err := workspace.Apply(a.RepoPath(), r.Branch, r.ID)
		_ = d.store.Update(func(tx *store.Tx) error {
			detail := map[string]any{"branch": r.Branch, "repo": a.RepoPath(), "head": head}
			if err != nil {
				detail["error"] = err.Error()
			}
			return tx.Audit("human.apply", a.ID, r.MessageID, r.ID, "", detail)
		})
		if err != nil {
			return nil, api.Errf(api.CodeRejected, "%v", err)
		}
		return map[string]string{"run_id": r.ID, "branch": r.Branch, "repo": a.RepoPath(), "head": head}, nil
	})
	s.Handle("agent.list", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		return d.agentViews(), nil
	})
	s.Handle("agent.status", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.status()
	})
	s.Handle("session.list", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.sessionList()
	})
	s.Handle("prompts.list", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.savedPrompts(), nil
	})
	s.Handle("session.harnesses", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.harnessOptions(), nil
	})
	s.Handle("session.new", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionNewParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionNew(ctx, p)
	})
	s.Handle("session.send", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionSendParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionSend(ctx, c, p)
	})
	s.Handle("session.queue", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionQueueParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionQueue(p)
	})
	s.Handle("session.queue.cancel", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionQueueCancelParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return nil, d.sessionQueueCancel(p)
	})
	s.Handle("session.focus", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionFocusParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionFocus(ctx, p)
	})
	s.Handle("session.attach", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionAttachParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionAttach(ctx, p)
	})
	s.Handle("session.history", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.sessionHistory()
	})
	s.Handle("session.search", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionSearchParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionSearch(ctx, p)
	})
	s.Handle("session.resume", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionResumeParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := checkOpenTerm(p.Term); err != nil {
			return nil, err
		}
		if err := d.resumeSession(ctx, p.SessionID, p.Term); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
	s.Handle("session.closed", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.closedList(), nil
	})
	s.Handle("session.reopen", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionReopenParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionReopen(p)
	})
	s.Handle("session.tail", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionTailParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionTail(p)
	})
	s.Handle("session.prompt", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.SessionPromptParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.sessionPrompt(ctx, p)
	})
	s.Handle("presence.update", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		var p api.PresenceParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if !c.IsHuman() {
			// Where a session runs decides where the human's prompts go
			// (ADR-0012): only the hooks (human token) may say it.
			p.TTY, p.PID, p.Term, p.Title, p.LastPrompt = "", 0, "", "", ""
			if p.Event == "busy" || p.Event == "idle" {
				p.Event = "heartbeat"
			}
		}
		return d.presence(p)
	})
	s.Handle("inbox.counts", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		var p api.CountsParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if !c.IsHuman() {
			p.Agent, p.Cwd = c.Agent, ""
			return d.counts(p), nil
		}
		out := d.counts(p)
		out.SessionsWaiting, out.SessionsAnswered = d.sessionAttention(p.SessionID)
		return out, nil
	})
	s.Handle("stats.get", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.StatsParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.stats(p)
	})
	s.Handle("digest.get", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.DigestParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.digest(p)
	})
	s.Handle("audit.since", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		var p api.AuditParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		return d.auditSince(p)
	})
	s.Handle("blob.put", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		var p api.BlobPutParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := envelope.ValidName(p.Name); err != nil {
			return nil, api.Errf(api.CodeParams, "%v", err)
		}
		if len(p.Data) > envelope.MaxAttachment {
			return nil, api.Errf(api.CodeParams, "blob larger than 25 MiB")
		}
		sha, mime, err := d.blobs.Put(p.Data, p.MIME)
		if err != nil {
			return nil, api.Errf(api.CodeUnavailable, "blob store: %v", err)
		}
		if err := d.store.Update(func(tx *store.Tx) error { return tx.PutBlob(sha, int64(len(p.Data)), mime) }); err != nil {
			return nil, err
		}
		return envelope.Attachment{Name: p.Name, SHA256: sha, Size: int64(len(p.Data)), MIME: mime}, nil
	})
	s.Handle("blob.get", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		var p api.IDParams
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if !c.IsHuman() {
			// Agents may fetch blobs attached to messages addressed to them
			// (replies to their own asks).
			var n int
			_ = d.store.DB().QueryRow(`SELECT COUNT(*) FROM message_blobs mb JOIN messages m ON m.id=mb.message_id
WHERE mb.sha256=? AND m.to_agent=? AND m.kind='reply'`, p.ID, c.Agent).Scan(&n)
			if n == 0 {
				return nil, api.Errf(api.CodeForbidden, "blob %s is not attached to a reply addressed to %s", p.ID, c.Agent)
			}
		}
		size, mime, err := d.store.BlobInfo(p.ID)
		if err != nil {
			return nil, api.Errf(api.CodeNotFound, "blob %s not found", p.ID)
		}
		data, err := d.blobs.Get(p.ID)
		if err != nil {
			return nil, api.Errf(api.CodeNotFound, "blob %s missing on disk", p.ID)
		}
		return api.BlobGetResult{SHA256: p.ID, Size: size, MIME: mime, Data: data}, nil
	})
	s.Handle("admin.health", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		return d.health(), nil
	})
	s.Handle("admin.reload", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		if err := d.Reload(); err != nil {
			return nil, api.Errf(api.CodeRejected, "config invalid, old config kept: %v", err)
		}
		return map[string]any{"ok": true, "agents": d.Config().AgentIDs(), "warnings": d.Config().Warnings}, nil
	})
	s.Handle("admin.gc", func(ctx context.Context, c api.Caller, raw json.RawMessage) (any, error) {
		if err := humanOnly(c); err != nil {
			return nil, err
		}
		return d.GC()
	})
}

// await waits for the reply to the caller's own ask.
func (d *Daemon) await(ctx context.Context, c api.Caller, ticket string, waitS int) (*api.AwaitResult, error) {
	m, err := d.store.FindMessage(ticket)
	if err != nil {
		return nil, api.Errf(api.CodeNotFound, "ticket %s not found", ticket)
	}
	owner := m.Env.From.Agent
	if !c.IsHuman() && owner != c.Agent {
		return nil, api.Errf(api.CodeForbidden, "ticket %s belongs to %s", ticket, owner)
	}
	if c.IsHuman() && owner != config.Human {
		// The human may read any reply through the CLI.
	}
	if m.Env.Kind != "ask" {
		return nil, api.Errf(api.CodeParams, "%s is a %s, not an ask", ticket, m.Env.Kind)
	}
	if waitS < 0 {
		waitS = 0
	}
	if waitS > 600 {
		waitS = 600
	}
	deadline := time.NewTimer(time.Duration(waitS) * time.Second)
	defer deadline.Stop()
	for {
		m, err = d.store.GetMessage(m.Env.ID)
		if err != nil {
			return nil, err
		}
		if m.AnsweredBy != "" {
			r, err := d.store.GetMessage(m.AnsweredBy)
			if err != nil {
				return nil, err
			}
			if owner == c.Agent || c.IsHuman() && owner == config.Human {
				_ = d.store.Update(func(tx *store.Tx) error { return tx.MarkSeen(r.Env.ID) })
			}
			return &api.AwaitResult{Ticket: m.Env.ID, Status: "answered", State: string(m.State), Reply: replyView(r)}, nil
		}
		ch := make(chan struct{})
		d.mu.Lock()
		d.waiters[m.Env.ID] = append(d.waiters[m.Env.ID], ch)
		d.mu.Unlock()
		// Re-check after subscribing to avoid a lost wake-up.
		if again, _ := d.store.GetMessage(m.Env.ID); again != nil && again.AnsweredBy != "" {
			continue
		}
		select {
		case <-ch:
			continue
		case <-deadline.C:
			return &api.AwaitResult{Ticket: m.Env.ID, Status: "pending", State: string(m.State)}, nil
		case <-ctx.Done():
			return nil, api.Errf(api.CodeUnavailable, "daemon shutting down")
		}
	}
}

func replyView(r *store.Message) *api.ReplyView {
	return &api.ReplyView{ID: r.Env.ID, From: r.Env.From.Agent, Status: nonEmpty(r.Env.Status, "ok"), Text: r.Env.Body,
		Data: r.Env.Data, Attachments: r.Env.Attachments, CreatedAt: r.Env.CreatedAt}
}

func messageView(m *store.Message, withBody bool) api.MessageView {
	v := api.MessageView{ID: m.Env.ID, ThreadID: m.Env.ThreadID, From: m.Env.From, To: m.Env.To, Kind: m.Env.Kind,
		Intent: m.Env.Intent, Priority: m.Env.Priority, Subject: m.Env.Subject, State: string(m.State), Hop: m.Env.Hop,
		Coalesced: m.Coalesced, Recurrence: m.Recurrence, Attempt: m.Attempt, Handler: m.Handler, RuleID: m.RuleID,
		Summary: m.Summary, Reason: m.Reason, HoldReason: m.HoldReason, HumanQuestion: m.HumanQuestion, ReplyStatus: m.Env.Status,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
	if m.Env.ParentID != nil {
		v.ParentID = *m.Env.ParentID
	}
	if withBody {
		v.Body, v.Data, v.Attachments = m.Env.Body, m.Env.Data, m.Env.Attachments
	}
	return v
}

func runView(r *store.Run) api.RunView {
	v := api.RunView{ID: r.ID, MessageID: r.MessageID, Agent: r.Agent, Handler: r.Handler, Profile: r.Profile, Attempt: r.Attempt,
		State: r.State, PID: r.PID, SessionID: r.SessionID, ArchivedSession: r.ArchivedSession, Workspace: r.Workspace, Branch: r.Branch,
		HeadSHA: r.HeadSHA, Diffstat: r.Diffstat, CostUSD: r.CostUSD, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		Error: r.Error, Transcript: r.Transcript, Stderr: r.Stderr, StartedAt: r.StartedAt}
	if r.ResultJSON != "" {
		v.Result = json.RawMessage(r.ResultJSON)
	}
	if !r.EndedAt.IsZero() {
		t := r.EndedAt
		v.EndedAt = &t
	}
	return v
}

func (d *Daemon) threadView(id string) (*api.ThreadView, error) {
	thread := id
	if m, err := d.store.FindMessage(id); err == nil {
		thread = m.Env.ThreadID
	}
	ms, err := d.store.Messages(`thread_id=? ORDER BY created_at, id`, thread)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, api.Errf(api.CodeNotFound, "thread %s not found", id)
	}
	tv := &api.ThreadView{ThreadID: thread, Messages: []api.MessageView{}, Runs: []api.RunView{}}
	var frozen int
	var reason *string
	_ = d.store.DB().QueryRow(`SELECT frozen, frozen_reason FROM threads WHERE thread_id=?`, thread).Scan(&frozen, &reason)
	tv.Frozen = frozen == 1
	if reason != nil {
		tv.FrozenReason = *reason
	}
	for _, m := range ms {
		tv.Messages = append(tv.Messages, messageView(m, true))
		rs, _ := d.store.Runs(`message_id=? ORDER BY started_at`, m.Env.ID)
		for _, r := range rs {
			tv.Runs = append(tv.Runs, runView(r))
		}
	}
	return tv, nil
}

func (d *Daemon) listMessages(p api.ListParams) ([]api.MessageView, error) {
	where, args := []string{"1=1"}, []any{}
	if p.Agent != "" {
		where, args = append(where, "(to_agent=? OR from_agent=?)"), append(args, p.Agent, p.Agent)
	}
	if p.State != "" {
		where, args = append(where, "state=?"), append(args, p.State)
	}
	if p.Limit <= 0 {
		p.Limit = 30
	}
	args = append(args, p.Limit)
	ms, err := d.store.Messages(strings.Join(where, " AND ")+" ORDER BY created_at DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	out := []api.MessageView{}
	for _, m := range ms {
		out = append(out, messageView(m, false))
	}
	return out, nil
}

func (d *Daemon) count(q string, args ...any) int {
	var n int
	_ = d.store.DB().QueryRow(q, args...).Scan(&n)
	return n
}

func (d *Daemon) agentViews() []api.AgentView {
	cfg := d.Config()
	today := day(time.Now())
	var out []api.AgentView
	for _, id := range cfg.AgentIDs() {
		a := cfg.Agent(id)
		cost, tokens := d.store.Usage(id, today)
		v := api.AgentView{ID: id, Repo: a.RepoPath(), Intents: a.Intents(),
			Queued:      d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND state IN ('queued','retry_wait')`, id),
			Running:     d.count(`SELECT COUNT(*) FROM runs WHERE agent=? AND state='running'`, id),
			NeedsHuman:  d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND state='needs_human'`, id),
			DeadLetters: d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND state='dead_letter'`, id),
			BudgetUSD:   a.BudgetUSD, SpentUSD: cost, TokensToday: tokens}
		if a.BudgetUSD > 0 {
			left := a.BudgetUSD - cost
			v.BudgetLeft = &left
		}
		pres, _ := d.store.ActivePresence(a.RepoPath(), cfg.Daemon.PresenceTTL.Duration)
		v.Interactive = len(pres) > 0
		out = append(out, v)
	}
	return out
}

func (d *Daemon) agentForCwd(cwd string) *config.Agent {
	if cwd == "" {
		return nil
	}
	// A cwd inside Smif's own worktrees is never an interactive session.
	if strings.HasPrefix(filepath.Clean(cwd)+"/", filepath.Join(d.dataDir, "worktrees")+"/") {
		return nil
	}
	return d.Config().AgentForPath(cwd)
}

func (d *Daemon) presence(p api.PresenceParams) (any, error) {
	a := d.agentForCwd(p.Cwd)
	if p.Agent != "" {
		a = d.Config().Agent(p.Agent)
	}
	if a == nil {
		return map[string]any{"agent": nil}, nil
	}
	if p.SessionID == "" {
		p.SessionID = "unknown"
	}
	state := "active"
	ev := "presence.heartbeat"
	u := store.PresenceUpdate{TTY: p.TTY, PID: p.PID, Term: p.Term,
		Title: oneLine(p.Title, 60), LastPrompt: oneLine(p.LastPrompt, 120)}
	switch p.Event {
	case "start":
		ev, u.Turn = "presence.start", "idle"
	case "end":
		state, ev = "ended", "presence.end"
	case "busy", "idle":
		u.Turn = p.Event
	}
	err := d.store.Update(func(tx *store.Tx) error {
		if err := tx.UpsertPresence(a.RepoPath(), p.SessionID, state, u); err != nil {
			return err
		}
		if p.Event == "start" || p.Event == "end" {
			return tx.Audit(ev, a.ID, "", "", "", map[string]any{"session_id": p.SessionID})
		}
		return nil
	})
	if p.Event == "end" {
		d.kick()
	}
	return map[string]any{"agent": a.ID, "state": state}, err
}

func (d *Daemon) counts(p api.CountsParams) api.Counts {
	var a *config.Agent
	if p.Agent != "" {
		a = d.Config().Agent(p.Agent)
	} else {
		a = d.agentForCwd(p.Cwd)
	}
	c := api.Counts{ClaudeOK: d.isClaudeOK(),
		GlobalNeedsHuman: d.count(`SELECT COUNT(*) FROM messages WHERE state='needs_human'`),
		GlobalDLQ:        d.count(`SELECT COUNT(*) FROM messages WHERE state='dead_letter'`)}
	if a == nil {
		return c
	}
	cursor := d.store.DigestCursor(a.ID)
	c.Agent = a.ID
	c.Running = d.count(`SELECT COUNT(*) FROM runs WHERE agent=? AND state='running'`, a.ID)
	c.Queued = d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND state IN ('queued','retry_wait')`, a.ID)
	c.NeedsHuman = d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND state='needs_human'`, a.ID)
	// "Handled" is what a human may want to read: worker results and FYI
	// digests. Deterministic command answers and replies to own asks are not.
	c.Handled = d.count(`SELECT COUNT(*) FROM messages m WHERE m.to_agent=? AND m.state='done' AND m.handled_seq>?
AND m.handler IN ('worker','digest')`, a.ID, cursor)
	c.RepliesReady = d.count(`SELECT COUNT(*) FROM messages WHERE to_agent=? AND kind='reply' AND handler='inbox' AND seen=0`, a.ID)
	d.mu.Lock()
	_, c.DirectLock = d.directLocks[a.RepoPath()]
	d.mu.Unlock()
	return c
}

func (d *Daemon) digest(p api.DigestParams) (*api.DigestResult, error) {
	cfg := d.Config()
	var agents []string
	switch {
	case p.Agent != "":
		if cfg.Agent(p.Agent) == nil {
			return nil, api.Errf(api.CodeNotFound, "unknown agent %s", p.Agent)
		}
		agents = []string{p.Agent}
	case p.Cwd != "":
		if a := d.agentForCwd(p.Cwd); a != nil {
			agents = []string{a.ID}
		} else {
			agents = cfg.AgentIDs()
		}
	default:
		agents = cfg.AgentIDs()
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	res := &api.DigestResult{Agents: agents, Items: []api.DigestItem{}}
	for _, ag := range agents {
		cursor := int64(0)
		if p.Since != "all" {
			cursor = d.store.DigestCursor(ag)
		}
		until := p.Until
		if until <= 0 {
			until = 1<<63 - 1
		}
		ms, err := d.store.Messages(`to_agent=? AND handled_seq>? AND handled_seq<=? AND state IN ('done','needs_human','dead_letter','rejected','expired') ORDER BY handled_seq LIMIT ?`, ag, cursor, until, p.Limit)
		if err != nil {
			return nil, err
		}
		maxSeq := cursor
		for _, m := range ms {
			it := api.DigestItem{MessageID: m.Env.ID, ThreadID: m.Env.ThreadID, Agent: ag, From: m.Env.From.Agent, Kind: m.Env.Kind,
				Intent: m.Env.Intent, Subject: m.Env.Subject, State: string(m.State), Summary: m.Summary, HumanQuestion: m.HumanQuestion,
				Coalesced: m.Coalesced, At: m.UpdatedAt}
			if m.State == store.Rejected || m.State == store.Expired || m.State == store.DeadLetter {
				it.Summary = nonEmpty(m.Summary, m.Reason)
			}
			if m.Env.Kind == "reply" && m.Handler == "inbox" {
				it.Summary = "Reply from " + m.Env.From.Agent + ": " + clip(m.Env.Body, 280)
			}
			if rs, _ := d.store.Runs(`message_id=? ORDER BY started_at DESC LIMIT 1`, m.Env.ID); len(rs) > 0 {
				it.RunID, it.Branch = rs[0].ID, rs[0].Branch
			}
			res.Items = append(res.Items, it)
			if m.HandledSeq > maxSeq {
				maxSeq = m.HandledSeq
			}
		}
		if p.Advance && maxSeq > cursor {
			if err := d.store.Update(func(tx *store.Tx) error {
				if err := tx.SetDigestCursor(ag, maxSeq); err != nil {
					return err
				}
				// Replies the human has now read no longer count as "ready".
				if _, err := tx.Exec(`UPDATE messages SET seen=1 WHERE to_agent=? AND kind='reply' AND handler='inbox' AND handled_seq<=?`, ag, maxSeq); err != nil {
					return err
				}
				return tx.Audit("digest.read", ag, "", "", "", map[string]any{"cursor": maxSeq, "items": len(ms)})
			}); err != nil {
				return nil, err
			}
		}
		if maxSeq > res.Cursor {
			res.Cursor = maxSeq
		}
	}
	sort.SliceStable(res.Items, func(i, j int) bool { return res.Items[i].At.Before(res.Items[j].At) })
	return res, nil
}

// auditSince pages the audit table for the human (audit.since,
// ADR-0008). Details pass the same token redaction as the JSONL log.
func (d *Daemon) auditSince(p api.AuditParams) (*api.AuditPage, error) {
	head, err := d.store.AuditHead()
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit > 1000 {
		limit = 1000
	}
	var rows []store.AuditRow
	page := &api.AuditPage{Events: []api.AuditEvent{}, Head: head, Cursor: p.After}
	if p.FromEnd {
		if rows, err = d.store.AuditTail(max(limit, 0)); err != nil {
			return nil, err
		}
		page.Cursor = head
	} else {
		if limit <= 0 {
			limit = 200
		}
		if rows, err = d.store.AuditSince(p.After, limit+1); err != nil {
			return nil, err
		}
		if len(rows) > limit {
			rows, page.More = rows[:limit], true
		}
		if n := len(rows); n > 0 {
			page.Cursor = rows[n-1].Seq
		}
	}
	for _, r := range rows {
		ev := api.AuditEvent{Seq: r.Seq, TS: r.TS, Event: r.Event, Agent: r.Agent, MessageID: r.MessageID, RunID: r.RunID,
			ThreadID: r.ThreadID}
		// A detail that fails the redaction round trip is dropped, never
		// sent unredacted.
		if len(r.Detail) > 0 {
			if b, err := json.Marshal(r.Detail); err == nil {
				var det map[string]any
				if json.Unmarshal([]byte(d.reg.Redact(string(b))), &det) == nil {
					ev.Detail = det
				}
			}
		}
		page.Events = append(page.Events, ev)
	}
	return page, nil
}

func (d *Daemon) health() api.HealthView {
	d.capsMu.RLock()
	caps := d.caps
	ok := d.claudeOK
	d.capsMu.RUnlock()
	h := api.HealthView{OK: true, Version: Version, SchemaVersion: d.store.Version(), ConfigPath: d.Config().Path,
		StartedAt: d.startedAt, PID: os.Getpid(), ClaudeOK: ok, DB: "ok"}
	h.Sessions, h.SessionsDetail = d.sessionsCanary()
	h.Providers = d.providerChecks()
	h.Background = d.backgroundChecks()
	var n int
	if err := d.store.DB().QueryRow(`PRAGMA quick_check`).Scan(new(string)); err != nil {
		h.DB, h.OK = "error: "+err.Error(), false
	}
	_ = n
	if caps != nil {
		h.ClaudeVersion = caps.Version
		h.ClaudeProblems = caps.Problems
		h.FlagProbe = caps.Lines()
	}
	return h
}

func (d *Daemon) status() (*api.StatusView, error) {
	cfg := d.Config()
	sv := &api.StatusView{Daemon: d.health(), Agents: d.agentViews(), Running: []api.RunView{}}
	rs, err := d.store.Runs(`state='running' ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		sv.Running = append(sv.Running, runView(r))
	}
	sv.DLQ = d.count(`SELECT COUNT(*) FROM messages WHERE state='dead_letter'`)
	mt := api.Metrics{CostToday: map[string]float64{}, TokensToday: map[string]int64{}, BudgetLeft: map[string]float64{}, DLQ: sv.DLQ,
		Running: len(rs), QueueDepth: d.count(`SELECT COUNT(*) FROM messages WHERE state IN ('queued','retry_wait')`)}
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	recent, _ := d.store.Runs(`started_at>=? AND state<>'running'`, since)
	var ok, judged int
	var durs []float64
	for _, r := range recent {
		if r.State != store.RunCancelled { // a human cancel says nothing about the handler
			judged++
		}
		if r.State == store.RunSucceeded || r.State == store.RunNeedsHuman {
			ok++
		}
		if !r.EndedAt.IsZero() {
			durs = append(durs, r.EndedAt.Sub(r.StartedAt).Seconds())
		}
	}
	mt.RunsLast24h = len(recent)
	if judged > 0 {
		mt.SuccessRate = float64(ok) / float64(judged)
	}
	sort.Float64s(durs)
	mt.P50Seconds, mt.P95Seconds = pct(durs, 0.5), pct(durs, 0.95)
	today := day(time.Now())
	for _, id := range cfg.AgentIDs() {
		c, t := d.store.Usage(id, today)
		mt.CostToday[id], mt.TokensToday[id] = c, t
		if b := cfg.Agent(id).BudgetUSD; b > 0 {
			mt.BudgetLeft[id] = b - c
		}
	}
	mt.Coalesced24h = d.count(`SELECT COUNT(*) FROM audit WHERE event='message.coalesced' AND ts>=?`, since)
	sv.Metrics = mt
	return sv, nil
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	i := int(float64(len(xs)-1) * p)
	return xs[i]
}

// IsNotFound reports whether err is a store not-found.
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
