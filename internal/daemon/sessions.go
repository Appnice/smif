package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// Session prompts (ADR-0012): the human sees which interactive sessions
// wait for input and types a prompt into one. The text comes from the
// human-only RPC session.prompt, never from a message, so D4 holds: no
// inbound text reaches an interactive session.

// MaxPromptRunes caps one prompt.
const MaxPromptRunes = 4000

func processAlive(pid int) bool { return harness.ProcessAlive(pid) }

// agentForRepo maps a presence row back to its agent.
func agentForRepo(cfg *config.Config, repo string) string {
	for _, id := range cfg.AgentIDs() {
		if cfg.Agent(id).RepoPath() == repo {
			return id
		}
	}
	return ""
}

// liveSessions is every interactive session still running at any level:
// the full sessions, then what the scan found that no full session owns.
func (d *Daemon) liveSessions() ([]store.Presence, error) {
	rows, err := d.fullSessions()
	if err != nil {
		return nil, err
	}
	return d.addScanned(rows, d.scan.get()), nil
}

// fullSessions is the sessions the providers discover (internal/harness),
// joined with the hook rows. A hook row with an agent pid counts while that
// process lives (an idle session sends no heartbeats); one without counts
// within presence_ttl. The status line and the watchers read only these,
// so they start no scan.
func (d *Daemon) fullSessions() ([]store.Presence, error) {
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
	return d.mergeSessions(d.Config(), rows, d.harnesses.Discover()), nil
}

func (d *Daemon) sessionView(cfg *config.Config, p store.Presence) api.SessionView {
	v := api.SessionView{Agent: agentForRepo(cfg, p.Repo), Harness: p.Harness, Level: nonEmpty(p.Level, harness.LevelFull),
		SessionID: p.SessionID, Repo: p.Repo, Turn: p.Turn, TurnAt: p.TurnAt, LastSeen: p.LastSeen, TTY: p.TTY, Term: p.Term,
		Title: p.Title, LastPrompt: p.LastPrompt}
	prov := d.harnesses.Get(p.Harness)
	readOnly := harness.ReadOnly(p.Harness, prov)
	v.Unverified = prov != nil && !prov.Info().Verified
	v.Background = p.AttachID != ""
	if p.Level == "" {
		ti := d.answers.get(prov, p.SessionID, cfg.IsTestCommand)
		v.LastAnswer, v.LastAnswerAt = ti.answer, ti.answerAt
		if ti.ok {
			v.Activity = activityView(ti, p.Cwd)
		}
		// ADR-0019: where the state cannot tell a pending approval from
		// idle, the state is unknown, never idle.
		if v.Turn == "" || !harness.CanTellState(prov) {
			v.Turn = "unknown"
		}
		v.Facts = d.factsView(cfg, p, ti, v.Turn)
	} else if p.Level == harness.LevelHerdr && p.Harness != harness.ClaudeName {
		// herdr gives the state, and its screen rules for this agent decide
		// what idle means. Until a real session checks those, no typing,
		// also for an agent whose own files passed a check (Codex).
		if prov == nil {
			readOnly = ""
		}
		if !herdrChecked(p.Harness) {
			readOnly = p.Harness + " is unverified, no real session checked it: read only"
			v.Unverified = true
		}
	}
	d.prompts.apply(&v, p.TurnAt)
	switch {
	case v.Background:
		v.Why = backgroundWhy
	case p.Level == harness.LevelListed:
		v.Turn, v.Why = "unknown", "listed only: Smif sees the process but not its state, so it does not type"
	case readOnly != "":
		v.Why = readOnly
	case p.PID == 0 || p.TTY == "" && p.Level == "":
		v.Why = "Smif has not seen this session's terminal yet; it learns it at the next prompt or turn end"
	case d.terminal.Supports(p) != "":
		v.Why = d.terminal.Supports(p)
	case v.Turn == "busy":
		v.Why = "a prompt is running"
	case v.Turn == "unknown" && p.Level == harness.LevelHerdr:
		v.Why = herdrUnknownWhy(p.Harness)
	case v.Turn == "unknown":
		v.Why = "no turn seen yet"
	case v.Turn == "waiting":
		v.Why = "it waits for you in its tab (a permission or a question); o shows the tab"
	case v.Turn != "idle":
		v.Why = p.Harness + " reports " + v.Turn
	default:
		v.CanPrompt = true
	}
	return v
}

// backgroundWhy is why Smif neither types into nor forks a background
// session (ADR-0021).
const backgroundWhy = "a background session has no tab, so Smif does not write to it; o opens claude attach"

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
	step := func(s harness.Step) api.SessionStep {
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
	queued := d.queuedBySession()
	out := []api.SessionView{}
	for _, p := range ps {
		v := d.sessionView(cfg, p) // Agent "": no agent's repo
		v.Queue = queued[p.SessionID]
		out = append(out, v)
	}
	out = append(out, d.remoteRows(time.Now())...)
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

// harnessOptions are the agents session.new can start now, claude first:
// claude, plus each known or configured harness with a command on the
// daemon's PATH (session.harnesses, human only, ADR-0021).
func (d *Daemon) harnessOptions() []string {
	cfg := d.Config()
	names := harness.BuiltinNames()
	for name := range cfg.Harnesses {
		if harness.BuiltinCommand(name) == "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := []string{harness.ClaudeName}
	for _, name := range names {
		if name == harness.ClaudeName {
			continue
		}
		if c := cfg.HarnessCommand(name); c != "" {
			if _, err := exec.LookPath(c); err == nil {
				out = append(out, name)
			}
		}
	}
	return out
}

// newArgv builds a new session's argv. The command comes from the config or
// the built-in table; the call only picks a name among them (R-ISO-3). The
// title reaches argv only for claude, and the caller checked it.
func (d *Daemon) newArgv(h, title string) ([]string, error) {
	h = nonEmpty(h, harness.ClaudeName)
	cfg := d.Config()
	command := cfg.HarnessCommand(h)
	switch {
	case command == "":
		return nil, api.Errf(api.CodeParams, "no harness named %q", h)
	case h == harness.ClaudeName && title != "":
		return []string{command, "--name", title}, nil
	case h == harness.ClaudeName:
		return []string{command}, nil
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "the daemon cannot find %s on its PATH", command)
	}
	return []string{command}, nil
}

// sessionNew opens a new terminal tab that runs a coding agent in a folder
// (session.new, human only, ADR-0015, ADR-0021). Only the folder, the name
// and the harness's name come from the call; the command is the config's
// or the built-in table's.
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
	if err := checkOpenTerm(p.Term); err != nil {
		return nil, err
	}
	argv, err := d.newArgv(p.Harness, name)
	if err != nil {
		return nil, err
	}
	if err := d.terminal.Open(ctx, p.Term, dir, name, argv); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "%v", err)
	}
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("session.new", agentForRepo(d.Config(), gitRoot(dir)), "", "", "", map[string]any{"dir": dir, "name": name, "term": p.Term, "harness": nonEmpty(p.Harness, harness.ClaudeName)})
	})
	return map[string]any{"ok": true}, nil
}

// checkOpenTerm refuses a terminal that session.new and session.attach
// cannot open a tab in.
func checkOpenTerm(term string) error {
	switch term {
	case "", termApple, termITerm, termTmux, termHerdr:
		return nil
	}
	return api.Errf(api.CodeParams, "new sessions open in Terminal.app, iTerm2, tmux or herdr, not %s", term)
}

// sessionAttach opens `claude attach <id>` for a background session in a
// new tab (session.attach, human only, ADR-0021). The call picks a session.
// The id comes from the list, and the program from the config (R-ISO-3).
func (d *Daemon) sessionAttach(ctx context.Context, p api.SessionAttachParams) (any, error) {
	if err := checkOpenTerm(p.Term); err != nil {
		return nil, err
	}
	ps, err := d.fullSessions()
	if err != nil {
		return nil, err
	}
	for _, s := range ps {
		if s.SessionID != p.SessionID || s.AttachID == "" {
			continue
		}
		dir := nonEmpty(s.Cwd, s.Repo)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return nil, api.Errf(api.CodeRejected, "the session's folder %s is gone", dir)
		}
		argv := []string{d.Config().Defaults.ClaudeCommand, "attach", s.AttachID}
		if err := d.terminal.Open(ctx, p.Term, dir, s.AttachID, argv); err != nil {
			return nil, api.Errf(api.CodeUnavailable, "%v", err)
		}
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("session.attach", agentForRepo(d.Config(), s.Repo), "", "", "", map[string]any{"session_id": s.SessionID, "term": p.Term})
		})
		return map[string]any{"ok": true}, nil
	}
	return nil, api.Errf(api.CodeNotFound, "no background session %s", p.SessionID)
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
		if s.AttachID != "" {
			return nil, api.Errf(api.CodeRejected, "%s", backgroundWhy)
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

// sessionTail reads the last turns of one session from its provider
// (session.tail, human only). Nothing from it goes into any session.
func (d *Daemon) sessionTail(p api.SessionTailParams) (*api.SessionTail, error) {
	if !harness.ValidID(p.SessionID) {
		return nil, api.Errf(api.CodeParams, "bad session_id")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	out := &api.SessionTail{SessionID: p.SessionID, Turns: []api.SessionTurn{}}
	var turns []api.SessionTurn
	for _, t := range d.harnesses.Read(p.SessionID, limit).Turns {
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
		if ps, err := d.fullSessions(); err == nil {
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
	res := &api.SessionPromptResult{Agent: v.Agent, SessionID: target.SessionID}
	if _, real := d.terminal.(autoTerminal); real && p.Async {
		// The human does not wait for the keys: the row shows busy now,
		// and Unsent if claude takes no turn.
		d.prompts.start(target.SessionID)
		go func(t store.Presence) {
			if err := d.deliverPrompt(context.Background(), t, v.Agent, text); err != nil {
				d.prompts.fail(t.SessionID, err.Error())
			} else {
				d.prompts.done(t.SessionID)
			}
		}(*target)
		return res, nil
	}
	if err := d.deliverPrompt(ctx, *target, v.Agent, text); err != nil {
		return nil, api.Errf(api.CodeUnavailable, "%v", err)
	}
	return res, nil
}

// errNotTyped marks a failure of the terminal itself: the watcher of the
// queue may try that item again (ADR-0021). Every other error of
// deliverPrompt comes after the keys went in, so the text is not typed twice.
var errNotTyped = errors.New("not typed")

// deliverPrompt types text into the session's tab and, on a real terminal,
// waits for claude to start the turn. A terminal can take the keys and
// claude still start no turn (the text sits in its input box): after 3 s
// it presses Enter once more (an Enter in an empty box does nothing), never
// the text again; after promptStartWait the human looks (o).
func (d *Daemon) deliverPrompt(ctx context.Context, target store.Presence, agent, text string) error {
	d.typeMu.Lock()
	defer d.typeMu.Unlock()
	typedAt := time.Now()
	if err := d.terminal.Type(ctx, target, text); err != nil {
		return fmt.Errorf("%w: %w", errNotTyped, err)
	}
	typeMS := time.Since(typedAt).Milliseconds()
	retried := false
	_, real := d.terminal.(autoTerminal)
	real = real && target.Level == "" // `herdr agent prompt` sends the Enter itself
	if real && !d.turnStarted(target.SessionID, typedAt, 3*time.Second) {
		retried = true
		if err := d.terminal.Enter(ctx, target); err != nil {
			return fmt.Errorf("typed, but Enter failed: %v", err)
		}
	}
	if real && !d.turnStarted(target.SessionID, typedAt, promptStartWait) {
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("session.prompt_unsent", agent, "", "", "", map[string]any{"session_id": target.SessionID,
				"chars": utf8.RuneCountInString(text), "type_ms": typeMS})
		})
		return fmt.Errorf("typed into its tab, but claude started no turn in %s: "+
			"the text may sit unsent in its input; o shows the tab", promptStartWait)
	}
	preview := text
	if utf8.RuneCountInString(preview) > 80 {
		preview = string([]rune(preview)[:80]) + "…"
	}
	if target.Level != "" {
		d.scan.expire() // the row shows the new state at the next look
	}
	return d.store.Update(func(tx *store.Tx) error {
		// A discovered session may have no hook row yet: write the full row.
		// A scanned session has no row: each scan finds it again.
		if target.Level == "" {
			if err := tx.UpsertPresence(target.Repo, target.SessionID, "active", store.PresenceUpdate{Turn: "busy",
				TTY: target.TTY, PID: target.PID, Term: target.Term, Title: target.Title, Harness: target.Harness}); err != nil {
				return err
			}
		}
		return tx.Audit("session.prompt", agent, "", "", "", map[string]any{"session_id": target.SessionID,
			"chars": utf8.RuneCountInString(text), "text": preview,
			// Typing (with the pause before Enter), and until claude
			// started the turn.
			"type_ms": typeMS, "total_ms": time.Since(typedAt).Milliseconds(), "enter_retry": retried})
	})
}

// promptState keeps the prompts typed in the background: the sessions
// being typed into (shown busy) and the last prompt that took no turn.
type promptState struct {
	mu      sync.Mutex
	sending map[string]time.Time
	unsent  map[string]unsentNote
}

type unsentNote struct {
	at  time.Time
	why string
}

func (s *promptState) start(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sending == nil {
		s.sending, s.unsent = map[string]time.Time{}, map[string]unsentNote{}
	}
	s.sending[id] = time.Now()
	delete(s.unsent, id)
}

func (s *promptState) done(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sending, id)
}

func (s *promptState) fail(id, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sending, id)
	s.unsent[id] = unsentNote{at: time.Now(), why: why}
}

// apply shows a prompt being typed as busy and a failed one as Unsent,
// unless the session has had a newer turn.
func (s *promptState) apply(v *api.SessionView, turnAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.sending[v.SessionID]; ok && !turnAt.After(at) {
		v.Turn, v.TurnAt = "busy", at
	}
	if u, ok := s.unsent[v.SessionID]; ok && !turnAt.After(u.at) {
		v.Unsent, v.UnsentAt = u.why, u.at
	}
}
