package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/auth"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/store"
)

// Session asks (ADR-0013). The human writes to a session from the UI: an
// idle session gets the text typed in (ADR-0012); a busy or waiting one
// cannot take it, so a worker answers instead. That worker resumes a fork
// of the session (claude -p --resume <id> --fork-session) in the session's
// folder: it knows what the session knows, gets a new session id, and the
// session itself is untouched. It runs with SessionAskProfile (read only).
// An agent's ask to a session's name goes the same way. No text from an
// ask or its answer is typed into any session, so D4 holds.

// SessionAskProfile is the worker profile that answers session asks.
const SessionAskProfile = "answer"

// sessionByName is the live session with that name (any case), or nil
// when none or several have it.
func (d *Daemon) sessionByName(name string) *store.Presence {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	ps, err := d.liveSessions()
	if err != nil {
		return nil
	}
	var hit *store.Presence
	for i := range ps {
		if ps[i].Title != "" && strings.EqualFold(strings.TrimSpace(ps[i].Title), name) {
			if hit != nil {
				return nil
			}
			hit = &ps[i]
		}
	}
	return hit
}

// forkTarget is the agent whose worker answers for a session, and the
// folder the fork resumes in (claude finds a session only from its own
// folder). The folder must be inside that agent's repo.
func (d *Daemon) forkTarget(s store.Presence) (agent, cwd string, err error) {
	cfg := d.Config()
	agent = agentForRepo(cfg, s.Repo)
	if agent == "" {
		return "", "", api.Errf(api.CodeRejected, "no agent owns %s, so no worker can answer for this session; "+
			"write to it when it is idle, or add an agent for that folder", s.Repo)
	}
	cwd = s.Cwd
	if cwd == "" {
		cwd = s.Repo
	}
	if a := cfg.AgentForPath(cwd); a == nil || a.RepoPath() != cfg.Agent(agent).RepoPath() {
		return "", "", api.Errf(api.CodeRejected, "the session's folder %s is not inside %s's repo", cwd, agent)
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return "", "", api.Errf(api.CodeRejected, "the session's folder %s is gone", cwd)
	}
	return agent, filepath.Clean(cwd), nil
}

// sessionSend is session.send (human only): type into an idle session, or
// ask a fork of a busy or waiting one.
func (d *Daemon) sessionSend(ctx context.Context, c auth.Principal, p api.SessionSendParams) (*api.SessionSendResult, error) {
	text, err := checkPrompt(d.expandPrompt(p.Text))
	if err != nil {
		return nil, err
	}
	ps, err := d.liveSessions()
	if err != nil {
		return nil, err
	}
	var target *store.Presence
	switch {
	case p.SessionID != "":
		for i := range ps {
			if ps[i].SessionID == p.SessionID {
				target = &ps[i]
			}
		}
	case p.Name != "":
		target = d.sessionByName(p.Name)
		if target == nil && d.Config().Agent(p.Name) != nil {
			// No session has that name: an agent with one session (an
			// idle one first).
			var mine, idle []*store.Presence
			for i := range ps {
				if agentForRepo(d.Config(), ps[i].Repo) == p.Name {
					mine = append(mine, &ps[i])
					if d.sessionView(d.Config(), ps[i]).CanPrompt {
						idle = append(idle, &ps[i])
					}
				}
			}
			switch {
			case len(idle) == 1:
				target = idle[0]
			case len(idle) == 0 && len(mine) == 1:
				target = mine[0]
			case len(mine) > 1:
				return nil, api.Errf(api.CodeRejected, "%s has %d sessions; name one (mailroom sessions shows the names)", p.Name, len(mine))
			}
		}
	default:
		return nil, api.Errf(api.CodeParams, "name a session_id or a name")
	}
	if target == nil {
		return nil, api.Errf(api.CodeNotFound, "no live session %s%s", p.SessionID, p.Name)
	}
	v := d.sessionView(d.Config(), *target)
	if v.CanPrompt {
		r, err := d.sessionPrompt(ctx, api.SessionPromptParams{SessionID: target.SessionID, Text: text})
		if err != nil {
			return nil, err
		}
		return &api.SessionSendResult{Mode: "typed", Agent: r.Agent, SessionID: r.SessionID}, nil
	}
	if v.Turn != "busy" && v.Turn != "waiting" {
		return nil, api.Errf(api.CodeRejected, "cannot write to %s: %s", api.ShortSession(target.SessionID), v.Why)
	}
	agent, cwd, err := d.forkTarget(*target)
	if err != nil {
		return nil, err
	}
	dr := envelope.Draft{To: agent, Kind: "ask", Intent: "session.ask", Subject: clip(text, envelope.MaxSubject), Body: text}
	if err := dr.Validate(); err != nil {
		return nil, api.Errf(api.CodeParams, "%v", err)
	}
	res, err := d.acceptFork(d.stamp(c, &dr), &store.SessionAsk{SessionID: target.SessionID, Cwd: cwd})
	if err != nil {
		return nil, err
	}
	return &api.SessionSendResult{Mode: "worker", Agent: agent, SessionID: target.SessionID, MessageID: res.ID}, nil
}

// askTurns are the asks answered by forks of a session as turns: the
// question ("ask") and, once there, the answer ("worker").
func (d *Daemon) askTurns(sessionID string) []api.SessionTurn {
	asks, err := d.store.SessionAsks(sessionID, 20)
	if err != nil {
		return nil
	}
	var out []api.SessionTurn
	for _, a := range asks {
		m, err := d.store.GetMessage(a.MessageID)
		if err != nil || m == nil {
			continue
		}
		from := "you"
		if m.Env.From.Agent != "human" {
			from = m.Env.From.Agent
		}
		out = append(out, api.SessionTurn{Role: "ask", From: from, Text: m.Env.Body, At: m.Env.CreatedAt, State: string(m.State)})
		replies, _ := d.store.Messages(`parent_id=? AND kind='reply'`, m.Env.ID)
		switch {
		case len(replies) > 0:
			r := replies[0]
			text := r.Env.Body
			if text == "" {
				text = r.Env.Subject
			}
			out = append(out, api.SessionTurn{Role: "worker", Text: text, At: r.Env.CreatedAt, State: nonEmpty(r.Env.Status, "ok")})
		case m.State == store.DeadLetter || m.State == store.Rejected || m.State == store.Expired:
			out = append(out, api.SessionTurn{Role: "worker", Text: nonEmpty(m.Reason, string(m.State)), At: m.UpdatedAt, State: string(m.State)})
		case m.State == store.Done:
			out = append(out, api.SessionTurn{Role: "worker", Text: m.Summary, At: m.UpdatedAt, State: "ok"})
		default:
			out = append(out, api.SessionTurn{Role: "worker", At: m.UpdatedAt, State: string(m.State)})
		}
	}
	return out
}

// mergeTurns joins the transcript turns and the ask turns by time.
func mergeTurns(a, b []api.SessionTurn) []api.SessionTurn {
	out := append(append([]api.SessionTurn{}, a...), b...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
