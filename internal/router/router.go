// Package router decides, in code, how a message is handled (RFC 6.1, 6.6,
// R-ISO-3): allowlists first, then hop and thread guards, then the first
// matching rule. The model never takes part in these decisions.
package router

import (
	"fmt"
	"sync"
	"time"

	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/envelope"
)

// Action is the routing outcome.
type Action string

// Actions.
const (
	Queue    Action = "queue"    // command or worker handler
	Escalate Action = "escalate" // needs_human
	Digest   Action = "digest"   // stored and counted
	Reject   Action = "reject"   // rejected with a reason
)

// Decision is what the router chose.
type Decision struct {
	Action  Action
	Rule    *config.Rule
	Handler string
	Profile string
	Reason  string
	// Notify asks the daemon to tell the human (escalations, guard trips).
	Notify bool
	// Freeze asks the daemon to freeze the thread.
	Freeze bool
}

// ThreadStats are the counts the guards need, read from the store.
type ThreadStats struct {
	Frozen       bool
	FrozenReason string
	Messages     int // including this one
	PairRecent   int // messages between from and to in the window, including this one
}

// Match reports whether a rule matches the envelope.
func Match(r *config.Rule, e *envelope.Envelope) bool {
	m := &r.Match
	if fs := m.FromList(); len(fs) > 0 && !in(fs, e.From.Agent) {
		return false
	}
	if ks := m.KindList(); len(ks) > 0 && !in(ks, e.Kind) {
		return false
	}
	if m.Intent != "" && !config.IntentMatches(m.Intent, e.Intent) {
		return false
	}
	if m.PriorityAtLeast != "" && config.PriorityLevel(e.Priority) < config.PriorityLevel(m.PriorityAtLeast) {
		return false
	}
	return true
}

func in(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// FirstRule returns the first matching rule for the recipient.
func FirstRule(a *config.Agent, e *envelope.Envelope) *config.Rule {
	for _, r := range a.Rules {
		if Match(r, e) {
			return r
		}
	}
	return nil
}

// Route decides how e is handled. ownReply is true when e answers an ask
// the recipient itself sent (replies to your own asks bypass the allowlist).
func Route(cfg *config.Config, e *envelope.Envelope, ts ThreadStats, ownReply bool) Decision {
	a := cfg.Agent(e.To)
	if a == nil {
		return Decision{Action: Reject, Reason: "unknown_agent: " + e.To}
	}
	if !ownReply && !cfg.Accepts(a, e.From.Agent) {
		return Decision{Action: Reject, Reason: fmt.Sprintf("sender_not_allowed: %s does not accept %s", e.To, e.From.Agent)}
	}
	if e.Hop > a.MaxHops {
		return Decision{Action: Reject, Reason: fmt.Sprintf("max_hops: hop %d > %d", e.Hop, a.MaxHops), Notify: true}
	}
	if ts.Frozen {
		return Decision{Action: Reject, Reason: "thread_frozen: " + ts.FrozenReason}
	}
	if ts.Messages > cfg.Daemon.MaxThreadMessages {
		return Decision{Action: Reject, Reason: fmt.Sprintf("max_thread_messages: %d > %d", ts.Messages, cfg.Daemon.MaxThreadMessages), Notify: true, Freeze: true}
	}
	if e.From.Agent != config.Human && e.From.Agent != e.To && ts.PairRecent > cfg.Daemon.PingPongLimit {
		return Decision{Action: Reject, Reason: fmt.Sprintf("ping_pong: %d messages between %s and %s within %s", ts.PairRecent,
			e.From.Agent, e.To, cfg.Daemon.PingPongWindow.Duration), Notify: true, Freeze: true}
	}
	r := FirstRule(a, e)
	if r == nil { // config validation guarantees a catch-all; be safe anyway
		return Decision{Action: Escalate, Reason: "no rule matched", Notify: true}
	}
	switch r.Handler {
	case "command", "worker":
		return Decision{Action: Queue, Rule: r, Handler: r.Handler, Profile: r.Profile}
	case "escalate":
		return Decision{Action: Escalate, Rule: r, Handler: "escalate", Notify: true, Reason: "rule " + r.ID}
	case "digest":
		return Decision{Action: Digest, Rule: r, Handler: "digest"}
	default: // drop
		return Decision{Action: Reject, Rule: r, Handler: "drop", Reason: "dropped by rule " + r.ID}
	}
}

// Limiter is a per-sender token bucket (RFC 6.6).
type Limiter struct {
	mu      sync.Mutex
	perMin  float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows perMinute messages per sender, bursting to perMinute.
func NewLimiter(perMinute int) *Limiter {
	return &Limiter{perMin: float64(perMinute), buckets: map[string]*bucket{}, now: time.Now}
}

// SetRate changes the rate (config reload).
func (l *Limiter) SetRate(perMinute int) {
	l.mu.Lock()
	l.perMin = float64(perMinute)
	l.mu.Unlock()
}

// Allow takes one token for sender.
func (l *Limiter) Allow(sender string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[sender]
	if b == nil {
		b = &bucket{tokens: l.perMin, last: now}
		l.buckets[sender] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * l.perMin
	if b.tokens > l.perMin {
		b.tokens = l.perMin
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
