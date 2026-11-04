package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/presence"
	"github.com/appnice/smif/internal/store"
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
	ti := d.answers.get(p.SessionID)
	v.LastAnswer, v.LastAnswerAt = ti.answer, ti.answerAt
	if ti.ok {
		v.Activity = activityView(ti, p.Cwd)
	}
	if v.Turn == "" {
		v.Turn = "unknown"
	}
	switch {
	case p.PID == 0 || p.TTY == "":
		v.Why = "Smif has not seen this session's terminal yet; it learns it at the next prompt or turn end"
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

// activityView is the API form of a transcript's activity, with paths
// under the session's folder written relative to it and the home folder
// as ~.
func activityView(ti transcriptInfo, cwd string) *api.SessionActivity {
	home, _ := os.UserHomeDir()
	short := func(s string) string {
		if cwd != "" && cwd != "/" {
			s = strings.ReplaceAll(s, cwd+"/", "")
		}
		if home != "" {
			s = strings.ReplaceAll(s, home+"/", "~/")
		}
		return oneLine(s, 300)
	}
	a := ti.act
	out := &api.SessionActivity{Prompt: a.Prompt, PromptAt: a.PromptAt, Tools: a.Tools, WroteAt: ti.wroteAt}
	step := func(s presence.Step) api.SessionStep {
		return api.SessionStep{Tool: s.Tool, Target: short(s.Target), At: s.At, Done: s.Done}
	}
	for _, s := range a.Steps {
		out.Steps = append(out.Steps, step(s))
	}
	if a.Pending != nil {
		n := step(*a.Pending)
		out.Now = &n
	}
	return out
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

// sessionNameRe is a name for a new session: letters, digits, spaces and
// . _ & -, at most 40.
var sessionNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._&-]{0,39}$`)

// sessionNew opens a new terminal tab that runs claude in a folder, named
// (session.new, human only, ADR-0015). Only the folder and the name come
// from the call; the command is the configured claude.
func (d *Daemon) sessionNew(ctx context.Context, p api.SessionNewParams) (any, error) {
	dir := filepath.Clean(p.Dir)
	if !filepath.IsAbs(dir) {
		return nil, api.Errf(api.CodeParams, "the folder must be an absolute path")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, api.Errf(api.CodeParams, "no folder %s", dir)
	}
	name := strings.TrimSpace(p.Name)
	if name != "" && !sessionNameRe.MatchString(name) {
		return nil, api.Errf(api.CodeParams, "a session name is letters, digits, spaces and . _ & -, at most 40")
	}
	switch p.Term {
	case "", termApple, termITerm, termTmux:
	default:
		return nil, api.Errf(api.CodeParams, "new sessions open in Terminal.app, iTerm2 or tmux, not %s", p.Term)
	}
	argv := []string{d.Config().Defaults.ClaudeCommand}
	if name != "" {
		argv = append(argv, "--name", name)
	}
	if err := d.terminal.Open(ctx, p.Term, dir, argv); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "%v", err)
	}
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("session.new", agentForRepo(d.Config(), gitRoot(dir)), "", "", "", map[string]any{"dir": dir, "name": name, "term": p.Term})
	})
	return map[string]any{"ok": true}, nil
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
			return nil, api.Errf(api.CodeRejected, "Smif does not know this session's terminal")
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

// savedPrompts are the config's saved prompts, sorted by name (ADR-0015).
func (d *Daemon) savedPrompts() []api.SavedPrompt {
	out := []api.SavedPrompt{}
	for name, text := range d.Config().Prompts {
		out = append(out, api.SavedPrompt{Name: name, Text: text})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// expandPrompt turns "@name rest" into the saved prompt name, then rest.
// Text whose first word is no saved prompt stays as it is: "@README.md"
// is Claude Code's file mention.
func (d *Daemon) expandPrompt(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return text
	}
	name, rest, _ := strings.Cut(t[1:], " ")
	saved, ok := d.Config().Prompts[name]
	if !ok {
		return text
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		return saved + " " + rest
	}
	return saved
}

// promptStartWait is how long session.prompt waits for claude to start the
// turn after typing.
const promptStartWait = 8 * time.Second

// turnStarted polls the live sessions until the session's turn is busy or
// waiting with a time after since, or wait runs out.
func (d *Daemon) turnStarted(sessionID string, since time.Time, wait time.Duration) bool {
	end := time.Now().Add(wait)
	for {
		if ps, err := d.liveSessions(); err == nil {
			for _, p := range ps {
				if p.SessionID == sessionID && p.Turn != "idle" && p.Turn != "" && p.TurnAt.After(since) {
					return true
				}
			}
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (d *Daemon) sessionPrompt(ctx context.Context, p api.SessionPromptParams) (*api.SessionPromptResult, error) {
	text, err := checkPrompt(d.expandPrompt(p.Text))
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
			return nil, api.Errf(api.CodeRejected, "%s has no idle session that Smif can type into", p.Agent)
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
	typedAt := time.Now()
	if err := d.terminal.Type(ctx, *target, text); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "%v", err)
	}
	typeMS := time.Since(typedAt).Milliseconds()
	retried := false
	// A real terminal can take the keys and claude still start no turn
	// (the text sits in its input box). Wait for the turn: the hook or the
	// session file says busy. Never type again: the human looks (o).
	// One more Enter (never the text) when the first one went into the
	// paste: an Enter in an empty input box does nothing.
	if _, real := d.terminal.(autoTerminal); real && !d.turnStarted(target.SessionID, typedAt, 3*time.Second) {
		retried = true
		if err := d.terminal.Enter(ctx, *target); err != nil {
			return nil, api.Errf(api.CodeUnavailable, "typed, but Enter failed: %v", err)
		}
	}
	if _, real := d.terminal.(autoTerminal); real && !d.turnStarted(target.SessionID, typedAt, promptStartWait) {
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("session.prompt_unsent", v.Agent, "", "", "", map[string]any{"session_id": target.SessionID,
				"chars": utf8.RuneCountInString(text), "type_ms": typeMS})
		})
		return nil, api.Errf(api.CodeUnavailable, "typed into the tab of %s, but claude started no turn in %s: "+
			"the text may sit unsent in its input; o shows the tab", api.ShortSession(target.SessionID), promptStartWait)
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
			"chars": utf8.RuneCountInString(text), "text": preview,
			// How long the human waited: typing (with the pause before
			// Enter) and until claude started the turn.
			"type_ms": typeMS, "total_ms": time.Since(typedAt).Milliseconds(), "enter_retry": retried})
	})
	return &api.SessionPromptResult{Agent: v.Agent, SessionID: target.SessionID}, err
}
