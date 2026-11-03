// Package auth manages agent tokens, run-scoped worker tokens and peer
// credential checks (RFC 5.4, 8.2).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/appnice/smif/internal/paths"
)

// HumanPrincipal is the token name for the human (CLI, hooks, status line).
const HumanPrincipal = "human"

// RunTokenPrefix marks worker run tokens.
const RunTokenPrefix = "run_"

// Principal is an authenticated caller.
type Principal struct {
	Agent  string // agent id, or "human"
	Origin string // interactive | cli | worker | human
	RunID  string // set for worker run tokens
	Hop    int    // hop of the message the run is handling
	Thread string // thread of the message the run is handling
}

// IsHuman reports whether the caller holds the human token.
func (p Principal) IsHuman() bool { return p.Agent == HumanPrincipal }

// ErrAuth is returned for any authentication failure.
var ErrAuth = errors.New("authentication failed")

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// TokenPath is where the token for name lives.
func TokenPath(name string) string { return filepath.Join(paths.TokenDir(), name) }

// EnsureToken creates name's token file (0600) if it is missing and returns it.
func EnsureToken(name string) (string, error) {
	if err := os.MkdirAll(paths.TokenDir(), 0o700); err != nil {
		return "", err
	}
	p := TokenPath(name)
	if b, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) == 64 {
			return t, nil
		}
	}
	t := newToken()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(t + "\n"); err != nil {
		return "", err
	}
	return t, nil
}

// ReadToken reads name's token file.
func ReadToken(name string) (string, error) {
	b, err := os.ReadFile(TokenPath(name))
	if err != nil {
		return "", fmt.Errorf("no token for %q (run `smif`: it starts the daemon, which makes the tokens): %w", name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Registry validates tokens. Agent tokens come from files; run tokens are
// minted per run and revoked when the run ends.
type Registry struct {
	mu      sync.RWMutex
	agents  map[string]string // name -> token
	runs    map[string]Principal
	revoked map[string]string // run token -> run id (for audit messages)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{agents: map[string]string{}, runs: map[string]Principal{}, revoked: map[string]string{}}
}

// Load ensures and loads tokens for the given agent ids plus the human.
func (r *Registry) Load(agentIDs []string) error {
	m := map[string]string{}
	for _, id := range append([]string{HumanPrincipal}, agentIDs...) {
		t, err := EnsureToken(id)
		if err != nil {
			return err
		}
		m[id] = t
	}
	r.mu.Lock()
	r.agents = m
	r.mu.Unlock()
	return nil
}

// MintRun creates a run-scoped token.
func (r *Registry) MintRun(p Principal) string {
	t := RunTokenPrefix + newToken()
	p.Origin = "worker"
	r.mu.Lock()
	r.runs[t] = p
	r.mu.Unlock()
	return t
}

// RevokeRun invalidates a run token.
func (r *Registry) RevokeRun(token string) {
	r.mu.Lock()
	if p, ok := r.runs[token]; ok {
		r.revoked[token] = p.RunID
		delete(r.runs, token)
	}
	r.mu.Unlock()
}

// Authenticate checks {agent, token}. origin is the client's claimed origin
// for agent tokens ("interactive" or "cli"); it is ignored for run tokens.
func (r *Registry) Authenticate(agent, token, origin string) (Principal, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if strings.HasPrefix(token, RunTokenPrefix) {
		p, ok := r.runs[token]
		if !ok {
			if run, was := r.revoked[token]; was {
				return Principal{}, fmt.Errorf("%w: run token for %s has expired (the run is no longer running)", ErrAuth, run)
			}
			return Principal{}, fmt.Errorf("%w: unknown run token", ErrAuth)
		}
		if agent != "" && agent != p.Agent {
			return Principal{}, fmt.Errorf("%w: run token belongs to %s, not %s", ErrAuth, p.Agent, agent)
		}
		return p, nil
	}
	want, ok := r.agents[agent]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return Principal{}, fmt.Errorf("%w: bad token for %q", ErrAuth, agent)
	}
	if agent == HumanPrincipal {
		return Principal{Agent: HumanPrincipal, Origin: "human"}, nil
	}
	if origin != "interactive" {
		origin = "cli"
	}
	return Principal{Agent: agent, Origin: origin}, nil
}

// Redact replaces any known token in s with "[redacted]".
func (r *Registry) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.agents {
		s = strings.ReplaceAll(s, t, "[redacted]")
	}
	for t := range r.runs {
		s = strings.ReplaceAll(s, t, "[redacted]")
	}
	return s
}
