// Package harness is the layer between the daemon and each coding agent's
// own files (ADR-0019). A Provider in internal/harness/<name> holds what is
// specific to one agent. Required: Info, Discover and Read. Optional, each
// a small interface of its own: Stater, Hooker, Forker, plus Stamper,
// Checker, BackgroundChecker, Factser and Historian. The daemon knows only
// this package and asks the Registry.
package harness

import (
	"errors"
	"regexp"
	"syscall"
	"time"
)

// Info is what the daemon must know about a provider.
type Info struct {
	Name string // the session's harness: "claude"
	// Process is the agent's process name. The foreground check before
	// typing (ADR-0012) wants a process whose name contains it.
	Process string
	// Verified is true when a real session checked the provider and
	// docs/compat.md has a row with that version. The zero value is the
	// safe one: an unverified provider only shows sessions (ADR-0019).
	Verified bool
}

// ClaudeName is Claude Code's harness name: the default harness.
const ClaudeName = "claude"

// Support levels of a session (ADR-0019). A session at the herdr or listed
// level has no provider behind it: only a scan of herdr or of the process
// table found it.
const (
	LevelFull   = "full"
	LevelHerdr  = "herdr"
	LevelListed = "listed"
)

// Session is one live session a provider found.
type Session struct {
	Harness   string // the provider's name (Registry.Discover sets it)
	PID       int
	SessionID string
	Cwd       string
	Name      string
	StartedAt int64     // process start, ms: with PID it names one process
	Status    string    // the agent's own word for its state, for Stater
	StatusAt  time.Time // when the agent wrote Status
	// AttachID marks a background session (ADR-0021): the id `claude attach`
	// takes. A background session has no tab, so Smif never types into it.
	AttachID string
	// EndedAt is set when a background session finished: its last answer,
	// else its start. LastAnswerAt is zero when it never answered.
	EndedAt, LastAnswerAt time.Time
}

// Reading is what a provider read of one session.
type Reading struct {
	Turns    []Turn   // the last turns, oldest first
	Activity Activity // what the newest exchange does now
	WroteAt  time.Time
	OK       bool // the session's source was there and was read
}

// Provider is one agent. Read is on demand, so it may be slow; session.list
// runs on every UI refresh, so give a slow provider a Stamper.
type Provider interface {
	Info() Info
	// Discover returns the live sessions: the interactive ones whose
	// process lives, and a provider's background sessions. The daemon calls
	// it under the status line, so it must not wait for a slow command.
	Discover() []Session
	// Read reads the last limit turns of a session. An unknown session
	// gives a Reading with OK false.
	Read(sessionID string, limit int) Reading
}

// Stater is a provider that can say what a session does.
type Stater interface {
	// State maps a discovered session to a turn: idle, busy, waiting, or
	// "" when its source says nothing. Return "" when the source cannot
	// tell a pending approval from idle: smif does not type then.
	State(s Session) (turn string, at time.Time)
}

// Hooker is a provider whose agent reports turns through `smif hook`.
type Hooker interface {
	HookEvents() []string
}

// Forker is a provider with a route that asks a copy of a session and
// leaves the source unchanged (ADR-0013). A provider without one has no
// side questions.
type Forker interface {
	// ForkDir is the folder the copy must start in, for a session in cwd
	// under repo.
	ForkDir(cwd, repo string) string
}

// Stamper is a provider with a cheap check for change: the stamp differs
// when Read would give something new, and "" means no source yet. Without
// one the daemon reads on every session.list.
type Stamper interface {
	Stamp(sessionID string) string
}

// Checker is a provider that checks that the agent's files still have the
// shape it reads (smif doctor). State is ok, none or changed.
type Checker interface {
	Check() (state, detail string)
}

// BackgroundChecker is a provider with a list of background sessions. The
// list is not a documented interface, so its state is news, never a failure
// (smif doctor). State is ok, none or changed.
type BackgroundChecker interface {
	CheckBackground() (state, detail string)
}

// ReadOnly says why smif must neither type into nor fork a session of this
// provider ("" = it may). p is nil when no provider has the session's
// harness name.
func ReadOnly(name string, p Provider) string {
	switch {
	case p == nil:
		return "smif has no provider for " + name + ": read only"
	case !p.Info().Verified:
		return name + " is unverified, no real session checked it: read only"
	case !CanTellState(p):
		return "smif cannot tell idle from a pending approval in " + name + ": read only"
	}
	return ""
}

// CanTellState reports whether the provider has a state source: its own or
// the hooks. Without one a session's state is unknown, never idle.
func CanTellState(p Provider) bool {
	if _, ok := p.(Stater); ok {
		return true
	}
	_, ok := p.(Hooker)
	return ok
}

// Registry holds the providers. Register them before the daemon serves.
type Registry struct {
	list []Provider
}

// NewRegistry returns a registry with these providers, in this order.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{}
	for _, p := range ps {
		r.Register(p)
	}
	return r
}

// Register adds a provider, or replaces the one with its name.
func (r *Registry) Register(p Provider) {
	for i, old := range r.list {
		if old.Info().Name == p.Info().Name {
			r.list[i] = p
			return
		}
	}
	r.list = append(r.list, p)
}

// Get returns the provider with that name, or nil.
func (r *Registry) Get(name string) Provider {
	for _, p := range r.list {
		if p.Info().Name == name {
			return p
		}
	}
	return nil
}

// All returns the providers in order.
func (r *Registry) All() []Provider { return r.list }

// Discover returns every provider's live sessions, each with its harness.
func (r *Registry) Discover() []Session {
	var out []Session
	for _, p := range r.list {
		for _, s := range p.Discover() {
			s.Harness = p.Info().Name
			out = append(out, s)
		}
	}
	return out
}

// Read asks the providers in order for a session: a session id alone does
// not say which agent wrote it, and an ended session has no live row.
func (r *Registry) Read(sessionID string, limit int) Reading {
	for _, p := range r.list {
		if rd := p.Read(sessionID, limit); rd.OK {
			return rd
		}
	}
	return Reading{}
}

// sessionIDRe allows _ for OpenCode's ses_ ids. Neither . nor / passes.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidID reports whether id is a session id: providers build paths from it.
func ValidID(id string) bool { return sessionIDRe.MatchString(id) }

// ProcessAlive reports whether a process with this pid exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Roles of a Turn, as session.tail sends them. The answer role keeps its
// wire value for every agent.
const (
	RoleHuman = "you"
	RoleAgent = "claude"
)

// Turn is one step of a session's conversation: the human's prompt
// (RoleHuman) or the agent's answer (RoleAgent). An answer holds only its
// final message ("" while it still works) and counts its tool calls.
type Turn struct {
	Role  string    `json:"role"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	Tools int       `json:"tools,omitempty"`
}

// LastAnswer is the final message of the newest answer and when it came
// ("" when the newest answer has no text yet).
func LastAnswer(turns []Turn) (string, time.Time) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == RoleAgent {
			return turns[i].Text, turns[i].At
		}
	}
	return "", time.Time{}
}

// Step is one tool call of the newest answer: the tool, what it works on
// (a command, a file, a question) and whether its result came back.
type Step struct {
	ID     string    `json:"-"`
	Tool   string    `json:"tool"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
	Done   bool      `json:"done"`
}

// Activity is what the newest exchange is doing: the human's prompt it
// answers and the tool calls since, oldest first (the last MaxSteps).
// Pending is the oldest call without a result: the tool that runs now, or
// the permission or question a waiting session shows in its tab.
type Activity struct {
	Prompt   string
	PromptAt time.Time
	Tools    int
	Steps    []Step
	Pending  *Step
}

// MaxSteps caps Activity.Steps.
const MaxSteps = 12

// StepText is a step in words: "Bash: git push origin main", a question
// as itself.
func StepText(tool, target string) string {
	switch {
	case tool == "AskUserQuestion" && target != "":
		return "asks: " + target
	case tool == "ExitPlanMode":
		return "a plan to approve"
	case target == "":
		return tool
	}
	return tool + ": " + target
}
