package daemon

import (
	"context"
	"errors"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/config"
	"github.com/appnice/mailroom/internal/presence"
	"github.com/appnice/mailroom/internal/store"
)

// Session prompts (ADR-0012): the human sees which interactive sessions
// wait for input and types a prompt into one. The text comes from the
// human-only RPC session.prompt, never from a message, so D4 holds: no
// inbound text reaches an interactive session.

// MaxPromptRunes caps one prompt.
const MaxPromptRunes = 4000

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// agentForRepo maps a presence row back to its agent.
func agentForRepo(cfg *config.Config, repo string) string {
	for _, id := range cfg.AgentIDs() {
		if cfg.Agent(id).RepoPath() == repo {
			return id
		}
	}
	return ""
}

// liveSessions is every interactive session still running: the sessions
// Claude Code lists in its session files (discover.go), joined with the
// hook rows. A hook row with a claude pid counts while that process lives
// (an idle session sends no heartbeats); one without counts within
// presence_ttl.
func (d *Daemon) liveSessions() ([]store.Presence, error) {
	all, err := d.store.AllPresence()
	if err != nil {
		return nil, err
	}
	ttl := d.Config().Daemon.PresenceTTL.Duration
	var rows []store.Presence
	for _, p := range all {
		if p.PID > 0 && processAlive(p.PID) || p.PID == 0 && time.Since(p.LastSeen) < ttl {
			rows = append(rows, p)
		}
	}
	return d.mergeSessions(d.Config(), rows, readClaudeSessions(d.sessDir)), nil
}

func (d *Daemon) sessionView(cfg *config.Config, p store.Presence) api.SessionView {
	v := api.SessionView{Agent: agentForRepo(cfg, p.Repo), SessionID: p.SessionID, Repo: p.Repo, Turn: p.Turn,
		TurnAt: p.TurnAt, LastSeen: p.LastSeen, TTY: p.TTY, Term: p.Term,
		Title: p.Title, LastPrompt: p.LastPrompt}
	v.LastAnswer, v.LastAnswerAt = d.answers.get(p.SessionID)
	if v.Turn == "" {
		v.Turn = "unknown"
	}
	switch {
	case p.PID == 0 || p.TTY == "":
		v.Why = "Mailroom has not seen this session's terminal yet; it learns it at the next prompt or turn end"
	case d.terminal.Supports(p) != "":
		v.Why = d.terminal.Supports(p)
	case p.Turn == "busy":
		v.Why = "a prompt is running"
	case p.Turn == "" || p.Turn == "unknown":
		v.Why = "no turn seen yet"
	case p.Turn == "waiting":
		v.Why = "it waits for you in its tab (a permission or a question); o shows the tab"
	case p.Turn != "idle":
		v.Why = "claude reports " + p.Turn
	default:
		v.CanPrompt = true
	}
	return v
}

func (d *Daemon) sessionList() ([]api.SessionView, error) {
	ps, err := d.liveSessions()
	if err != nil {
		return nil, err
	}
	cfg := d.Config()
	out := []api.SessionView{}
	for _, p := range ps {
		out = append(out, d.sessionView(cfg, p)) // Agent "": no agent's repo
	}
	// Discovery reads a map: sort, so every caller sees one fixed order.
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if a != b {
			return a < b
		}
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// sessionFocus brings a live session's Terminal tab to the front
// (session.focus, human only).
func (d *Daemon) sessionFocus(ctx context.Context, p api.SessionFocusParams) (any, error) {
	ps, err := d.liveSessions()
	if err != nil {
		return nil, err
	}
	for _, s := range ps {
		if s.SessionID != p.SessionID {
			continue
		}
		if s.TTY == "" {
			return nil, api.Errf(api.CodeRejected, "Mailroom does not know this session's terminal")
		}
		if why := d.terminal.Supports(s); why != "" {
			return nil, api.Errf(api.CodeRejected, "%s", why)
		}
		if err := d.terminal.Focus(ctx, s); err != nil {
			return nil, api.Errf(api.CodeUnavailable, "%v", err)
		}
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("session.focus", agentForRepo(d.Config(), s.Repo), "", "", "", map[string]any{"session_id": s.SessionID})
		})
		return map[string]any{"ok": true}, nil
	}
	return nil, api.Errf(api.CodeNotFound, "no live session %s", p.SessionID)
}

// sessionTail reads the last turns of one session from its transcript
// (session.tail, human only). Nothing from it goes into any session.
func (d *Daemon) sessionTail(p api.SessionTailParams) (*api.SessionTail, error) {
	if !sessionIDRe.MatchString(p.SessionID) {
		return nil, api.Errf(api.CodeParams, "bad session_id")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	out := &api.SessionTail{SessionID: p.SessionID, Turns: []api.SessionTurn{}}
	var turns []api.SessionTurn
	for _, t := range presence.Conversation(transcriptPath(p.SessionID), limit) {
		turns = append(turns, api.SessionTurn{Role: t.Role, Text: t.Text, At: t.At, Tools: t.Tools})
	}
	out.Turns = mergeTurns(turns, d.askTurns(p.SessionID))
	if len(out.Turns) > limit {
		out.Turns = out.Turns[len(out.Turns)-limit:]
	}
	return out, nil
}

// oneLine makes a session title or prompt preview safe to show: control
// characters become spaces, runs of spaces collapse, and it keeps at most
// max runes (with … when cut).
func oneLine(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// checkPrompt validates the human's text: one line of plain text.
func checkPrompt(text string) (string, error) {
	t := strings.TrimSpace(text)
	switch {
	case t == "":
		return "", api.Errf(api.CodeParams, "the prompt is empty")
	case !utf8.ValidString(t):
		return "", api.Errf(api.CodeParams, "the prompt is not valid UTF-8")
	case utf8.RuneCountInString(t) > MaxPromptRunes:
		return "", api.Errf(api.CodeParams, "the prompt is longer than %d characters", MaxPromptRunes)
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return "", api.Errf(api.CodeParams, "the prompt must be one line of plain text (no new lines or control characters)")
		}
	}
	return t, nil
}

func (d *Daemon) sessionPrompt(ctx context.Context, p api.SessionPromptParams) (*api.SessionPromptResult, error) {
	text, err := checkPrompt(p.Text)
	if err != nil {
		return nil, err
	}
	d.promptMu.Lock()
	defer d.promptMu.Unlock()
	ps, err := d.liveSessions()
	if err != nil {
		return nil, err
	}
	cfg := d.Config()
	var target *store.Presence
	if p.SessionID == "" && p.Name != "" {
		var named []store.Presence
		for i := range ps {
			if ps[i].Title != "" && strings.EqualFold(strings.TrimSpace(ps[i].Title), strings.TrimSpace(p.Name)) {
				named = append(named, ps[i])
			}
		}
		switch {
		case len(named) == 1:
			target = &named[0]
		case len(named) > 1:
			return nil, api.Errf(api.CodeRejected, "%d sessions are named %q; name one by session id", len(named), p.Name)
		case cfg.Agent(p.Name) != nil:
			p.Agent = p.Name
		default:
			return nil, api.Errf(api.CodeNotFound, "no live session named %q and no agent %q", p.Name, p.Name)
		}
	}
	var idle []store.Presence
	for i := range ps {
		ag := agentForRepo(cfg, ps[i].Repo)
		switch {
		case p.SessionID != "" && ps[i].SessionID == p.SessionID:
			target = &ps[i]
		case p.SessionID == "" && ag == p.Agent && ag != "" && d.sessionView(cfg, ps[i]).CanPrompt:
			idle = append(idle, ps[i])
		}
	}
	if p.SessionID == "" && target == nil {
		switch {
		case p.Agent == "":
			return nil, api.Errf(api.CodeParams, "name a session_id or an agent")
		case len(idle) == 0:
			return nil, api.Errf(api.CodeRejected, "%s has no idle session that Mailroom can type into", p.Agent)
		case len(idle) > 1:
			return nil, api.Errf(api.CodeRejected, "%s has %d idle sessions; name one by session id", p.Agent, len(idle))
		}
		target = &idle[0]
	}
	if target == nil {
		return nil, api.Errf(api.CodeNotFound, "no live session %s", p.SessionID)
	}
	v := d.sessionView(cfg, *target)
	if !v.CanPrompt {
		return nil, api.Errf(api.CodeRejected, "cannot type into %s: %s", api.ShortSession(target.SessionID), v.Why)
	}
	if err := d.terminal.Ready(*target); err != nil {
		return nil, api.Errf(api.CodeRejected, "cannot type into %s: %v", api.ShortSession(target.SessionID), err)
	}
	if err := d.terminal.Type(ctx, *target, text); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "%v", err)
	}
	preview := text
	if utf8.RuneCountInString(preview) > 80 {
		preview = string([]rune(preview)[:80]) + "…"
	}
	err = d.store.Update(func(tx *store.Tx) error {
		// A discovered session may have no hook row yet: write the full row.
		if err := tx.UpsertPresence(target.Repo, target.SessionID, "active", store.PresenceUpdate{Turn: "busy",
			TTY: target.TTY, PID: target.PID, Term: target.Term, Title: target.Title}); err != nil {
			return err
		}
		return tx.Audit("session.prompt", v.Agent, "", "", "", map[string]any{"session_id": target.SessionID,
			"chars": utf8.RuneCountInString(text), "text": preview})
	})
	return &api.SessionPromptResult{Agent: v.Agent, SessionID: target.SessionID}, err
}
