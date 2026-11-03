// Package scheduler orders runnable messages and enforces concurrency caps
// (RFC 6.5, R-CON-1): one worker per agent, a global worker cap, and a
// separate command-handler pool.
package scheduler

import (
	"sort"
	"time"
)

// Item is a runnable message.
type Item struct {
	ID        string
	Agent     string
	Handler   string // command | worker
	Priority  int    // 0..3
	CreatedAt time.Time
}

// Effective returns the priority after aging: one level per step waited,
// capped at urgent, so nothing starves.
func Effective(it Item, now time.Time, step time.Duration) int {
	p := it.Priority
	if step > 0 {
		p += int(now.Sub(it.CreatedAt) / step)
	}
	if p > 3 {
		p = 3
	}
	return p
}

// Order sorts items by effective priority (high first), then age.
func Order(items []Item, now time.Time, step time.Duration) {
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := Effective(items[i], now, step), Effective(items[j], now, step)
		if pi != pj {
			return pi > pj
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
}

// Slots tracks what is running.
type Slots struct {
	MaxWorkers  int
	CommandPool int
	busy        map[string]string // agent -> run id (workers only)
	workers     int
	commands    int
}

// NewSlots returns empty slots.
func NewSlots(maxWorkers, pool int) *Slots {
	return &Slots{MaxWorkers: maxWorkers, CommandPool: pool, busy: map[string]string{}}
}

// CanStart reports whether an item may start now.
func (s *Slots) CanStart(it Item) bool {
	if it.Handler == "command" {
		return s.commands < s.CommandPool
	}
	if _, busy := s.busy[it.Agent]; busy {
		return false
	}
	return s.workers < s.MaxWorkers
}

// AgentBusy reports whether a worker for agent is running.
func (s *Slots) AgentBusy(agent string) bool {
	_, ok := s.busy[agent]
	return ok
}

// Take reserves a slot.
func (s *Slots) Take(it Item, runID string) {
	if it.Handler == "command" {
		s.commands++
		return
	}
	s.busy[it.Agent] = runID
	s.workers++
}

// Release frees a slot.
func (s *Slots) Release(handler, agent string) {
	if handler == "command" {
		if s.commands > 0 {
			s.commands--
		}
		return
	}
	if _, ok := s.busy[agent]; ok {
		delete(s.busy, agent)
		s.workers--
	}
}

// Counts returns running workers and commands.
func (s *Slots) Counts() (workers, commands int) { return s.workers, s.commands }
