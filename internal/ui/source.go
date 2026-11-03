// Package ui is the human terminal UI (`mailroom ui`): a Bubble Tea program
// that polls the daemon socket with the human token and draws the overview.
// It is a client of the human-only RPC methods; it adds no path through
// which an agent could read inbound mail (RFC D4).
package ui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/appnice/mailroom/internal/api"
)

// Snapshot is one poll of the daemon. Err is set when the daemon could not
// be reached or a call failed; the other fields are then empty.
type Snapshot struct {
	At       time.Time
	Err      error
	Status   api.StatusView
	NeedsYou []api.MessageView
	DLQ      []api.MessageView
	Recent   []api.MessageView
	// The home screen's groups beyond NeedsYou and DLQ: messages a worker
	// runs, waiting ones (queued, retry_wait), and the newest done ones.
	Running []api.MessageView
	Waiting []api.MessageView
	Done    []api.MessageView
	// Unread holds the ids of handled items not yet marked read (the
	// digest, peeked without advancing), and DigestCursor bounds a mark.
	Unread       map[string]bool
	DigestCursor int64
	// Sessions are the live interactive sessions (session.list, ADR-0012).
	Sessions []api.SessionView
	// Events are the newest audit events (audit.since), oldest first, at
	// most EventKeep.
	Events []api.AuditEvent
	// EventsErr is set when audit.since failed with an RPC error (an
	// older daemon); the rest of the snapshot is still good.
	EventsErr error
}

// Source produces snapshots. The model calls Fetch from a Bubble Tea
// command, never two at a time.
type Source interface {
	Fetch(ctx context.Context) Snapshot
}

// Caller is the part of *api.Client the daemon source needs.
type Caller interface {
	Call(method string, params, out any, timeout time.Duration) error
	Close() error
}

// DaemonSource polls the daemon over one connection and redials after an
// error, so the UI survives a daemon restart.
type DaemonSource struct {
	// Dial opens a connection with the human token.
	Dial func() (Caller, error)
	// Timeout caps each call (default 3 s).
	Timeout time.Duration
	// RecentLimit is how many recent messages to load (default 20).
	RecentLimit int
	// Now is the clock (default time.Now).
	Now func() time.Time

	mu     sync.Mutex
	c      Caller
	feed   EventFeed
	events []api.AuditEvent // ring for the EVENTS panel
}

// Fetch reads agent.status and three thread.list views. A transport error
// on a reused connection (the daemon restarted) gets one fresh dial before
// Fetch reports the daemon down.
func (s *DaemonSource) Fetch(ctx context.Context) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	at := now()
	if err := ctx.Err(); err != nil {
		return Snapshot{At: at, Err: err}
	}
	snap, err := s.fetchOnce()
	var rpcErr *api.Error
	if err != nil && s.c == nil && snap.reused && !errors.As(err, &rpcErr) {
		snap, err = s.fetchOnce()
	}
	if err != nil {
		return Snapshot{At: at, Err: err}
	}
	snap.At = at
	return snap.Snapshot
}

type attempt struct {
	Snapshot
	reused bool
}

func (s *DaemonSource) fetchOnce() (attempt, error) {
	var a attempt
	if s.c == nil {
		c, err := s.Dial()
		if err != nil {
			return a, err
		}
		s.c = c
	} else {
		a.reused = true
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	limit := s.RecentLimit
	if limit <= 0 {
		limit = 20
	}
	var retrying []api.MessageView
	var digest api.DigestResult
	calls := []struct {
		method string
		params any
		out    any
	}{
		{"agent.status", nil, &a.Status},
		{"thread.list", api.ListParams{State: "needs_human", Limit: 100}, &a.NeedsYou},
		{"thread.list", api.ListParams{State: "dead_letter", Limit: 100}, &a.DLQ},
		{"thread.list", api.ListParams{Limit: limit}, &a.Recent},
		{"thread.list", api.ListParams{State: "running", Limit: 100}, &a.Running},
		{"thread.list", api.ListParams{State: "queued", Limit: 100}, &a.Waiting},
		{"thread.list", api.ListParams{State: "retry_wait", Limit: 100}, &retrying},
		{"thread.list", api.ListParams{State: "done", Limit: 50}, &a.Done},
		{"digest.get", api.DigestParams{Since: "last", Limit: 200}, &digest},
		{"session.list", nil, &a.Sessions},
	}
	for _, c := range calls {
		if err := s.c.Call(c.method, c.params, c.out, timeout); err != nil {
			s.c.Close()
			s.c = nil
			return a, err
		}
	}
	a.Waiting = append(a.Waiting, retrying...)
	a.Unread, a.DigestCursor = map[string]bool{}, digest.Cursor
	for _, it := range digest.Items {
		a.Unread[it.MessageID] = true
	}
	evs, reset, err := s.feed.Poll(func(method string, params, out any) error {
		return s.c.Call(method, params, out, timeout)
	}, EventKeep)
	// Keep what arrived before an error: the cursor is already past it.
	if reset {
		s.events = nil
	}
	s.events = keepEvents(s.events, evs, EventKeep)
	var rpcErr *api.Error
	switch {
	case err != nil && errors.As(err, &rpcErr):
		a.EventsErr = err
	case err != nil:
		s.c.Close()
		s.c = nil
		return a, err
	}
	a.Events = append([]api.AuditEvent(nil), s.events...)
	return a, nil
}

// Close drops the connection.
func (s *DaemonSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		s.c.Close()
		s.c = nil
	}
}
