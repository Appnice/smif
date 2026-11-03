// Package config loads and validates ~/.config/mailroom/config.toml (RFC 7.6).
//
// Decoding is strict: unknown keys are rejected. Validation enforces every
// rule the daemon relies on for safety (R-ISO-3), including the catch-all
// rule at the end of each agent's rule list.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/appnice/mailroom/internal/paths"
)

// Duration is a time.Duration decoded from strings such as "6h" or "30m".
// A trailing "d" (days) is also accepted.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return fmt.Errorf("bad duration %q", s)
		}
		d.Duration = time.Duration(n) * 24 * time.Hour
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q", s)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// Config is the whole file.
type Config struct {
	Daemon   Daemon             `toml:"daemon"`
	Defaults Defaults           `toml:"defaults"`
	Profiles map[string]Profile `toml:"profiles"`
	Agents   map[string]*Agent  `toml:"agents"`

	// Path is where the config was loaded from (not part of the file).
	Path string `toml:"-"`
	// Warnings are non-fatal findings from Validate.
	Warnings []string `toml:"-"`
}

// Daemon holds process-wide settings.
type Daemon struct {
	MaxWorkers  int      `toml:"max_workers"`
	CommandPool int      `toml:"command_pool"`
	DefaultTTL  Duration `toml:"default_ttl"`
	PresenceTTL Duration `toml:"presence_ttl"`
	Notify      string   `toml:"notify"` // "os", "none" or "log:<path>"
	// Terminal types the human's prompts into idle sessions (ADR-0012):
	// "auto" (Terminal.app on macOS), "none" (off) or "log:<path>" (tests).
	Terminal           string     `toml:"terminal"`
	RateLimitPerMinute int        `toml:"rate_limit_per_minute"`
	MaxThreadMessages  int        `toml:"max_thread_messages"`
	PingPongLimit      int        `toml:"ping_pong_limit"`
	PingPongWindow     Duration   `toml:"ping_pong_window"`
	AgingStep          Duration   `toml:"aging_step"`
	RetryBackoff       []Duration `toml:"retry_backoff"`
	OverloadExtra      int        `toml:"overload_extra_attempts"`
	MessageRetention   Duration   `toml:"message_retention"`
	TranscriptRetain   Duration   `toml:"transcript_retention"`
	BlobGrace          Duration   `toml:"blob_grace"`
	BranchRetention    Duration   `toml:"branch_retention"`
	AuditRetention     Duration   `toml:"audit_retention"`
	LogRetention       Duration   `toml:"log_retention"`
}

// Defaults apply to every agent unless overridden.
type Defaults struct {
	ClaudeCommand   string   `toml:"claude_command"`
	MaxHops         int      `toml:"max_hops"`
	MaxAttempts     int      `toml:"max_attempts"`
	CommandTimeout  Duration `toml:"command_timeout"`
	MaxOutputBytes  int64    `toml:"max_output_bytes"`
	RotateAtTokens  int64    `toml:"rotate_at_tokens"`
	RotateAfterRuns int      `toml:"rotate_after_runs"`
}

// Profile is a named worker runtime (RFC 6.2).
type Profile struct {
	Model           string   `toml:"model"`
	Effort          string   `toml:"effort"`
	PermissionMode  string   `toml:"permission_mode"`
	AllowedTools    []string `toml:"allowed_tools"`
	DisallowedTools []string `toml:"disallowed_tools"`
	WriteScope      string   `toml:"write_scope"`
	MaxTurns        int      `toml:"max_turns"`
	TimeoutS        int      `toml:"timeout_s"`
	Policy          string   `toml:"policy"`
	OnwardMCP       bool     `toml:"onward_mcp"`
	// Hooks lets the user's and the repo's Claude Code hooks run inside the
	// worker. Off by default: hooks have side effects outside the worker
	// (memory capture, checkpoints, nested claude calls).
	Hooks      bool     `toml:"hooks"`
	Minimal    bool     `toml:"minimal"`
	MaxCostUSD float64  `toml:"max_cost_usd"`
	ExtraArgs  []string `toml:"extra_args"`
}

// Agent is one project role.
type Agent struct {
	Repo           string   `toml:"repo"`
	BaseRef        string   `toml:"base_ref"`
	AcceptFrom     []string `toml:"accept_from"`
	BudgetUSD      float64  `toml:"budget_usd"`
	BudgetTokens   int64    `toml:"budget_tokens"`
	OnBudget       string   `toml:"on_budget"`       // hold | escalate
	DirectConflict string   `toml:"direct_conflict"` // hold | worktree
	AllowBypass    bool     `toml:"allow_bypass"`
	PR             bool     `toml:"pr"`
	MaxHops        int      `toml:"max_hops"`
	MaxAttempts    int      `toml:"max_attempts"`
	Rules          []*Rule  `toml:"rules"`
	// ProfileOverrides lets an agent change fields of a named profile.
	ProfileOverrides map[string]map[string]any `toml:"profile_overrides"`

	ID string `toml:"-"`
}

// Rule maps matching messages to a handler (RFC 6.1).
type Rule struct {
	ID          string   `toml:"id"`
	Match       Match    `toml:"match"`
	Handler     string   `toml:"handler"`
	Profile     string   `toml:"profile"`
	Command     []string `toml:"command"`
	Timeout     Duration `toml:"timeout"`
	Cooldown    Duration `toml:"cooldown"`
	ReopenAfter int      `toml:"reopen_after"`
}

// Match is the rule predicate. From and Kind accept a string or a list.
type Match struct {
	From            any    `toml:"from"`
	Kind            any    `toml:"kind"`
	Intent          string `toml:"intent"`
	PriorityAtLeast string `toml:"priority_at_least"`

	from []string
	kind []string
}

// FromList returns the normalised sender list (nil = any).
func (m *Match) FromList() []string { return m.from }

// KindList returns the normalised kind list (nil = any).
func (m *Match) KindList() []string { return m.kind }

// IsCatchAll reports whether the match has no predicate at all.
func (m *Match) IsCatchAll() bool {
	return len(m.from) == 0 && len(m.kind) == 0 && (m.Intent == "" || m.Intent == "*" || m.Intent == "**") && m.PriorityAtLeast == ""
}

var (
	agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	intentRe  = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_-]+)*$`)
	globRe    = regexp.MustCompile(`^[a-z0-9_*?-]+(\.[a-z0-9_*?-]+)*$`)
)

// ValidIntent checks an intent label.
func ValidIntent(s string) bool { return intentRe.MatchString(s) }

// Kinds, priorities, handlers and modes accepted by the schema.
var (
	Kinds           = []string{"tell", "ask", "event", "reply"}
	Priorities      = []string{"low", "normal", "high", "urgent"}
	Handlers        = []string{"command", "worker", "escalate", "digest", "drop"}
	PermissionModes = []string{"default", "acceptEdits", "plan", "bypassPermissions"}
	WriteScopes     = []string{"none", "worktree", "direct"}
	Efforts         = []string{"low", "medium", "high", "xhigh", "max"}
)

// PriorityLevel converts a priority name to 0..3 (-1 if unknown).
func PriorityLevel(p string) int {
	for i, v := range Priorities {
		if v == p {
			return i
		}
	}
	return -1
}

// Reserved principals that are not agents.
const (
	Human = "human"
)

// forbiddenExtra lists flags that may never appear in extra_args: they would
// let config bypass the argv builder's control of permissions, tools and
// sessions (R-ISO-3, 8.2).
var forbiddenExtra = []string{
	"--permission-mode", "--allowedTools", "--allowed-tools", "--disallowedTools",
	"--disallowed-tools", "--tools", "--dangerously", "--allow-dangerously",
	"--mcp-config", "--strict-mcp-config", "--settings", "--setting-sources",
	"--resume", "-r", "--continue", "-c", "--session-id", "--fork-session",
	"--append-system-prompt", "--system-prompt", "--add-dir", "--permission-prompts",
	"--permission-prompt-tool", "--json-schema", "--output-format", "--input-format",
	"--max-turns", "--max-budget-usd", "--model", "--bg", "--background", "--worktree",
	"-w", "--remote-control", "--cloud", "--agents", "--agent", "--plugin-dir", "--plugin-url",
	"--safe-mode", "--bare", "--restricted", "--brief", "--chrome", "--name", "-n",
}

// Load reads, decodes, defaults and validates a config file.
func Load(file string) (*Config, error) {
	if file == "" {
		file = paths.ConfigFile()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	c.Path = file
	return c, nil
}

// Parse decodes, defaults and validates config bytes.
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var sm *toml.StrictMissingError
		if errors.As(err, &sm) {
			return nil, fmt.Errorf("unknown keys: %s", strings.TrimSpace(sm.String()))
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, fmt.Errorf("line %d col %d: %s", row, col, de.Error())
		}
		return nil, err
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	d := &c.Daemon
	if d.MaxWorkers == 0 {
		d.MaxWorkers = 3
	}
	if d.CommandPool == 0 {
		d.CommandPool = 4
	}
	if d.DefaultTTL.Duration == 0 {
		d.DefaultTTL.Duration = 24 * time.Hour
	}
	if d.PresenceTTL.Duration == 0 {
		d.PresenceTTL.Duration = 30 * time.Minute
	}
	if d.Notify == "" {
		d.Notify = "os"
	}
	if d.Terminal == "" {
		d.Terminal = "auto"
	}
	if d.RateLimitPerMinute == 0 {
		d.RateLimitPerMinute = 60
	}
	if d.MaxThreadMessages == 0 {
		d.MaxThreadMessages = 20
	}
	if d.PingPongLimit == 0 {
		d.PingPongLimit = 6
	}
	if d.PingPongWindow.Duration == 0 {
		d.PingPongWindow.Duration = 10 * time.Minute
	}
	if d.AgingStep.Duration == 0 {
		d.AgingStep.Duration = 30 * time.Minute
	}
	if len(d.RetryBackoff) == 0 {
		d.RetryBackoff = []Duration{{30 * time.Second}, {2 * time.Minute}, {10 * time.Minute}}
	}
	if d.OverloadExtra == 0 {
		d.OverloadExtra = 5
	}
	def := func(x *Duration, v time.Duration) {
		if x.Duration == 0 {
			x.Duration = v
		}
	}
	day := 24 * time.Hour
	def(&d.MessageRetention, 90*day)
	def(&d.TranscriptRetain, 30*day)
	def(&d.BlobGrace, 7*day)
	def(&d.BranchRetention, 30*day)
	def(&d.AuditRetention, 90*day)
	def(&d.LogRetention, 14*day)

	f := &c.Defaults
	if f.ClaudeCommand == "" {
		f.ClaudeCommand = "claude"
	}
	if f.MaxHops == 0 {
		f.MaxHops = 4
	}
	if f.MaxAttempts == 0 {
		f.MaxAttempts = 3
	}
	if f.CommandTimeout.Duration == 0 {
		f.CommandTimeout.Duration = 30 * time.Second
	}
	if f.MaxOutputBytes == 0 {
		f.MaxOutputBytes = 8 << 20
	}
	if f.RotateAtTokens == 0 {
		f.RotateAtTokens = 120_000
	}
	if f.RotateAfterRuns == 0 {
		f.RotateAfterRuns = 25
	}

	// Built-in profiles first, user profiles replace them by name.
	merged := BuiltinProfiles()
	for name, p := range c.Profiles {
		merged[name] = p
	}
	for name, p := range merged {
		merged[name] = fillProfile(p)
	}
	c.Profiles = merged

	for id, a := range c.Agents {
		if a == nil {
			a = &Agent{}
			c.Agents[id] = a
		}
		a.ID = id
		if a.BaseRef == "" {
			a.BaseRef = "main"
		}
		if a.OnBudget == "" {
			a.OnBudget = "hold"
		}
		if a.DirectConflict == "" {
			a.DirectConflict = "hold"
		}
		if a.MaxHops == 0 {
			a.MaxHops = f.MaxHops
		}
		if a.MaxAttempts == 0 {
			a.MaxAttempts = f.MaxAttempts
		}
		for i, r := range a.Rules {
			if r == nil {
				r = &Rule{}
				a.Rules[i] = r
			}
			if r.ID == "" {
				r.ID = fmt.Sprintf("%s#%d", id, i+1)
			}
		}
	}
}

func fillProfile(p Profile) Profile {
	if p.Model == "" {
		p.Model = "sonnet"
	}
	if p.PermissionMode == "" {
		p.PermissionMode = "default"
	}
	if p.WriteScope == "" {
		p.WriteScope = "none"
	}
	if p.MaxTurns == 0 {
		p.MaxTurns = 30
	}
	if p.TimeoutS == 0 {
		p.TimeoutS = 900
	}
	return p
}

// BuiltinProfiles returns triage, fix, answer and release (RFC 6.2).
func BuiltinProfiles() map[string]Profile {
	ro := []string{"Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git show:*)", "Bash(git diff:*)", "Bash(git status:*)"}
	noWrite := []string{"Edit", "Write", "NotebookEdit"}
	return map[string]Profile{
		"triage":  {PermissionMode: "default", AllowedTools: ro, DisallowedTools: noWrite, WriteScope: "none", MaxTurns: 25, TimeoutS: 900},
		"fix":     {PermissionMode: "acceptEdits", AllowedTools: append(append([]string{}, ro...), "Edit", "Write", "Bash(git add:*)", "Bash(git commit:*)"), WriteScope: "worktree", MaxTurns: 40, TimeoutS: 1800},
		"answer":  {PermissionMode: "default", AllowedTools: ro, DisallowedTools: noWrite, WriteScope: "none", MaxTurns: 8, TimeoutS: 600},
		"release": {PermissionMode: "acceptEdits", AllowedTools: append(append([]string{}, ro...), "Edit", "Write"), WriteScope: "direct", MaxTurns: 40, TimeoutS: 1800},
	}
}

func asList(v any, field string) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if x == "" || x == "*" {
			return nil, nil
		}
		return []string{x}, nil
	case []any:
		var out []string
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s: list items must be strings", field)
			}
			if s == "*" {
				return nil, nil
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s: must be a string or a list of strings", field)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Validate checks every rule in RFC 6 and 7.6. It collects all problems.
func (c *Config) Validate() error {
	c.Warnings = nil
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Daemon.MaxWorkers < 1 {
		add("daemon.max_workers must be >= 1")
	}
	if c.Daemon.CommandPool < 1 {
		add("daemon.command_pool must be >= 1")
	}
	n := c.Daemon.Notify
	if n != "os" && n != "none" && !strings.HasPrefix(n, "log:") {
		add("daemon.notify must be \"os\", \"none\" or \"log:<path>\"")
	}
	if t := c.Daemon.Terminal; t != "auto" && t != "none" && !strings.HasPrefix(t, "log:") {
		add("daemon.terminal must be \"auto\", \"none\" or \"log:<path>\"")
	}
	if len(c.Agents) == 0 {
		add("no agents configured")
	}

	for name, p := range c.Profiles {
		where := "profiles." + name
		if !contains(PermissionModes, p.PermissionMode) {
			add("%s.permission_mode %q: must be one of %v", where, p.PermissionMode, PermissionModes)
		}
		if !contains(WriteScopes, p.WriteScope) {
			add("%s.write_scope %q: must be one of %v", where, p.WriteScope, WriteScopes)
		}
		if p.Effort != "" && !contains(Efforts, p.Effort) {
			add("%s.effort %q: must be one of %v", where, p.Effort, Efforts)
		}
		if p.MaxTurns < 1 || p.TimeoutS < 1 {
			add("%s: max_turns and timeout_s must be positive", where)
		}
		if p.WriteScope == "none" {
			for _, t := range p.AllowedTools {
				if t == "Edit" || t == "Write" || t == "NotebookEdit" {
					add("%s: write_scope \"none\" cannot allow %s", where, t)
				}
			}
		}
		for _, a := range p.ExtraArgs {
			for _, f := range forbiddenExtra {
				if a == f || strings.HasPrefix(a, f+"=") || (strings.HasPrefix(f, "--dangerously") && strings.HasPrefix(a, f)) {
					add("%s.extra_args: %q is controlled by Mailroom and cannot be passed", where, a)
				}
			}
		}
		if p.Minimal && p.OnwardMCP {
			add("%s: minimal (--safe-mode) disables MCP servers, so it cannot be combined with onward_mcp", where)
		}
		if p.Policy != "" {
			if _, err := os.Stat(paths.Expand(p.Policy)); err != nil {
				add("%s.policy: %v", where, err)
			}
		}
	}

	ids := make([]string, 0, len(c.Agents))
	for id := range c.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := c.Agents[id]
		where := "agents." + id
		if !agentIDRe.MatchString(id) {
			add("%s: agent id must match %s", where, agentIDRe)
		}
		if id == Human {
			add("%s: %q is reserved", where, Human)
		}
		if a.Repo == "" {
			add("%s.repo is required", where)
		}
		if a.OnBudget != "hold" && a.OnBudget != "escalate" {
			add("%s.on_budget must be \"hold\" or \"escalate\"", where)
		}
		if a.DirectConflict != "hold" && a.DirectConflict != "worktree" {
			add("%s.direct_conflict must be \"hold\" or \"worktree\"", where)
		}
		for _, s := range a.AcceptFrom {
			if s != Human && s != "*" && c.Agents[s] == nil {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s.accept_from: agent %q is not configured (ignored until it is)", where, s))
			}
		}
		for p := range a.ProfileOverrides {
			if _, ok := c.Profiles[p]; !ok {
				add("%s.profile_overrides: unknown profile %q", where, p)
			}
		}
		if len(a.Rules) == 0 {
			add("%s: no rules; every agent must end with a catch-all rule (match = {})", where)
			continue
		}
		for i, r := range a.Rules {
			rw := fmt.Sprintf("%s.rules[%d]", where, i)
			var err error
			if r.Match.from, err = asList(r.Match.From, rw+".match.from"); err != nil {
				add("%v", err)
			}
			if r.Match.kind, err = asList(r.Match.Kind, rw+".match.kind"); err != nil {
				add("%v", err)
			}
			for _, f := range r.Match.from {
				if f != Human && c.Agents[f] == nil {
					add("%s.match.from: unknown agent %q", rw, f)
				}
			}
			for _, k := range r.Match.kind {
				if !contains(Kinds, k) {
					add("%s.match.kind %q: must be one of %v", rw, k, Kinds)
				}
			}
			if r.Match.Intent != "" && !globRe.MatchString(r.Match.Intent) {
				add("%s.match.intent %q: not a valid intent glob", rw, r.Match.Intent)
			} else if r.Match.Intent != "" {
				if _, err := path.Match(globToPath(r.Match.Intent), "x"); err != nil {
					add("%s.match.intent %q: %v", rw, r.Match.Intent, err)
				}
			}
			if r.Match.PriorityAtLeast != "" && PriorityLevel(r.Match.PriorityAtLeast) < 0 {
				add("%s.match.priority_at_least %q: must be one of %v", rw, r.Match.PriorityAtLeast, Priorities)
			}
			if !contains(Handlers, r.Handler) {
				add("%s.handler %q: must be one of %v", rw, r.Handler, Handlers)
			}
			switch r.Handler {
			case "worker":
				p, ok := c.Profiles[r.Profile]
				if r.Profile == "" || !ok {
					add("%s: handler \"worker\" needs a known profile (got %q)", rw, r.Profile)
				} else {
					ep, err := c.EffectiveProfile(id, r.Profile)
					if err != nil {
						add("%s: %v", rw, err)
					} else {
						p = ep
					}
					if p.PermissionMode == "bypassPermissions" && !a.AllowBypass {
						add("%s: profile %q uses bypassPermissions; set allow_bypass = true on %s", rw, r.Profile, where)
					}
				}
			case "command":
				if len(r.Command) == 0 {
					add("%s: handler \"command\" needs command = [...]", rw)
				}
			}
			if r.Handler != "worker" && r.Profile != "" {
				add("%s: profile is only valid with handler \"worker\"", rw)
			}
			if r.Handler != "command" && len(r.Command) > 0 {
				add("%s: command is only valid with handler \"command\"", rw)
			}
			if r.ReopenAfter < 0 {
				add("%s.reopen_after must be >= 0", rw)
			}
		}
		last := a.Rules[len(a.Rules)-1]
		if !last.Match.IsCatchAll() {
			add("%s: the last rule must be a catch-all (match = {})", where)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// globToPath maps the dotted intent glob onto path.Match by treating dots as
// separators, so "crash.*" matches "crash.report" but not "crash.a.b".
// A trailing ".**" matches any depth.
func globToPath(g string) string { return strings.ReplaceAll(g, ".", "/") }

// IntentMatches reports whether a dotted intent matches a glob.
func IntentMatches(glob, intent string) bool {
	if glob == "" || glob == "*" || glob == "**" {
		return true
	}
	if strings.HasSuffix(glob, ".**") {
		prefix := strings.TrimSuffix(glob, ".**")
		ok, _ := path.Match(globToPath(prefix), globToPath(firstN(intent, strings.Count(prefix, ".")+1)))
		return ok && strings.Count(intent, ".") > strings.Count(prefix, ".")
	}
	ok, _ := path.Match(globToPath(glob), globToPath(intent))
	return ok
}

func firstN(intent string, n int) string {
	parts := strings.Split(intent, ".")
	if len(parts) < n {
		return intent
	}
	return strings.Join(parts[:n], ".")
}

// EffectiveProfile returns profile name for agent id with overrides applied.
func (c *Config) EffectiveProfile(agentID, name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q", name)
	}
	a := c.Agents[agentID]
	if a == nil || a.ProfileOverrides == nil || a.ProfileOverrides[name] == nil {
		return p, nil
	}
	// Round-trip through TOML so overrides use the same strict field names.
	base, err := toml.Marshal(p)
	if err != nil {
		return p, err
	}
	var m map[string]any
	if err := toml.Unmarshal(base, &m); err != nil {
		return p, err
	}
	for k, v := range a.ProfileOverrides[name] {
		m[k] = v
	}
	b, err := toml.Marshal(m)
	if err != nil {
		return p, err
	}
	var out Profile
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return p, fmt.Errorf("profile_overrides.%s: %v", name, err)
	}
	return fillProfile(out), nil
}

// Agent returns the agent or nil.
func (c *Config) Agent(id string) *Agent { return c.Agents[id] }

// AgentIDs returns sorted agent ids.
func (c *Config) AgentIDs() []string {
	ids := make([]string, 0, len(c.Agents))
	for id := range c.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RepoPath returns the agent's expanded repository path.
func (a *Agent) RepoPath() string { return paths.Expand(a.Repo) }

// Accepts reports whether the agent accepts traffic from sender (R-SEC-2).
// The default is every configured agent plus the human.
func (c *Config) Accepts(to *Agent, from string) bool {
	if from == Human {
		return true
	}
	if len(to.AcceptFrom) == 0 {
		return c.Agents[from] != nil
	}
	return contains(to.AcceptFrom, from) || contains(to.AcceptFrom, "*") && c.Agents[from] != nil
}

// AgentForPath returns the agent whose repo contains dir (longest match).
// Symlinks are resolved on both sides, so a symlinked checkout matches.
func (c *Config) AgentForPath(dir string) *Agent {
	dirs := []string{paths.Expand(dir)}
	if r, err := filepath.EvalSymlinks(dirs[0]); err == nil && r != dirs[0] {
		dirs = append(dirs, r)
	}
	var best *Agent
	bestLen := -1
	for _, a := range c.Agents {
		repos := []string{a.RepoPath()}
		if r, err := filepath.EvalSymlinks(repos[0]); err == nil && r != repos[0] {
			repos = append(repos, r)
		}
		for _, d := range dirs {
			for _, r := range repos {
				if (d == r || strings.HasPrefix(d, r+"/")) && len(r) > bestLen {
					best, bestLen = a, len(r)
				}
			}
		}
	}
	return best
}

// Intents lists the intent globs an agent handles (its capabilities, 7.1).
func (a *Agent) Intents() []string {
	var out []string
	for _, r := range a.Rules {
		if r.Handler == "drop" || r.Handler == "digest" && r.Match.IsCatchAll() {
			continue
		}
		label := r.Match.Intent
		if label == "" {
			label = "*"
		}
		if ks := r.Match.KindList(); len(ks) > 0 {
			label = strings.Join(ks, "|") + ":" + label
		}
		label += " -> " + r.Handler
		out = append(out, label)
	}
	return out
}
