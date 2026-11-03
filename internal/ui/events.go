package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/appnice/mailroom/internal/api"
)

// EventKeep is how many audit events the overview keeps for the EVENTS
// panel.
const EventKeep = 200

// eventPage caps one audit.since page; a poll reads at most eventPages.
const (
	eventPage  = 500
	eventPages = 10
)

// EventFeed follows the audit table through audit.since (ADR-0008). It
// keeps only a cursor: the daemon holds the rows.
type EventFeed struct {
	cursor  int64
	started bool
}

// Cursor is the seq of the newest event the feed returned.
func (f *EventFeed) Cursor() int64 { return f.cursor }

// Poll returns the events after the cursor, oldest first. The first poll
// returns the newest backlog events and puts the cursor at the head.
// reset is true when the daemon's head is behind the cursor (a new
// database); the feed then starts again from the head.
func (f *EventFeed) Poll(call func(method string, params, out any) error, backlog int) (evs []api.AuditEvent, reset bool, err error) {
	if !f.started {
		var pg api.AuditPage
		if err := call("audit.since", api.AuditParams{FromEnd: true, Limit: backlog}, &pg); err != nil {
			return nil, false, err
		}
		f.cursor, f.started = pg.Cursor, true
		return pg.Events, false, nil
	}
	for i := 0; i < eventPages; i++ {
		var pg api.AuditPage
		if err := call("audit.since", api.AuditParams{After: f.cursor, Limit: eventPage}, &pg); err != nil {
			return evs, false, err
		}
		if pg.Head < f.cursor {
			f.started = false
			evs, _, err := f.Poll(call, backlog)
			return evs, true, err
		}
		evs = append(evs, pg.Events...)
		f.cursor = pg.Cursor
		if !pg.More {
			break
		}
	}
	return evs, false, nil
}

// PollEvents is EventFeed.Poll on the source's connection, for `mailroom
// watch --events`. It shares the feed with Fetch, so use one or the other.
func (s *DaemonSource) PollEvents(ctx context.Context, backlog int) ([]api.AuditEvent, bool, error) {
	call := func(method string, params, out any) error {
		return s.callT(ctx, method, params, out, 5*time.Second)
	}
	return s.feed.Poll(call, backlog)
}

// EventCursor is the seq of the newest event PollEvents returned.
func (s *DaemonSource) EventCursor() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.feed.Cursor()
}

// FormatEvent is one audit event on one line, as `mailroom watch
// --events` prints it:
//
//	13:10:05 #42 message.transition cafe msg 0000000001 queued→running
func FormatEvent(ev api.AuditEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%d %s", ev.TS.Local().Format("15:04:05"), ev.Seq, ev.Event)
	if ev.Agent != "" {
		b.WriteString(" " + ev.Agent)
	}
	if ev.MessageID != "" {
		b.WriteString(" msg " + Short(ev.MessageID))
	}
	if ev.RunID != "" {
		b.WriteString(" run " + Short(ev.RunID))
	}
	if d := EventDetail(ev); d != "" {
		b.WriteString(" " + d)
	}
	return b.String()
}

// EventDetail is the detail of an event in short form: the state change
// first for message.transition, then key=value pairs in key order. Each
// value is on one line and at most 80 characters.
func EventDetail(ev api.AuditEvent) string {
	det := ev.Detail
	skip := map[string]bool{}
	var parts []string
	switch ev.Event {
	case "message.transition":
		parts = append(parts, fmt.Sprint(det["from_state"])+"→"+fmt.Sprint(det["to_state"]))
		skip["from_state"], skip["to_state"] = true, true
	case "message.accepted":
		parts = append(parts, fmt.Sprintf("%v %v from %v", det["kind"], det["intent"], det["from"]))
		skip["kind"], skip["intent"], skip["from"], skip["state"] = true, true, true, true
	}
	keys := make([]string, 0, len(det))
	for k := range det {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := det[k]
		if v == nil || v == "" {
			continue
		}
		s := oneLine(fmt.Sprint(v))
		if f, ok := v.(float64); ok {
			s = strconv.FormatFloat(f, 'f', -1, 64)
		}
		if r := []rune(s); len(r) > 80 {
			s = string(r[:79]) + "…"
		}
		if strings.ContainsAny(s, " =") {
			s = fmt.Sprintf("%q", s)
		}
		parts = append(parts, k+"="+s)
	}
	return strings.Join(parts, " ")
}

// keepEvents appends evs to the ring and keeps the newest n.
func keepEvents(ring, evs []api.AuditEvent, n int) []api.AuditEvent {
	ring = append(ring, evs...)
	if len(ring) > n {
		ring = append([]api.AuditEvent(nil), ring[len(ring)-n:]...)
	}
	return ring
}
