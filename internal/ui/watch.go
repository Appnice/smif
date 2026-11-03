package ui

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

// WatchSummary is the compact plain-text summary `mailroom watch` prints
// every interval: one header line, the agents table, then one line per
// running run, needs-you item, dead letter and each of the last events
// (newest last). It has no colours and no cursor movement, so it works in
// a pipe, a tmux pane or over ssh.
func WatchSummary(s Snapshot, now time.Time, interval time.Duration, events int) string {
	var b strings.Builder
	stamp := now.Local().Format("15:04:05")
	if s.Err != nil {
		fmt.Fprintf(&b, "== %s · DAEMON DOWN: %s · retrying every %s\n", stamp, oneLine(s.Err.Error()), interval)
		return b.String()
	}
	st := s.Status
	d := st.Daemon
	claude := "claude " + d.ClaudeVersion + " ok"
	if !d.ClaudeOK {
		claude = "claude UNAVAILABLE (" + strings.Join(d.ClaudeProblems, "; ") + ")"
	}
	fmt.Fprintf(&b, "== %s · daemon %s pid %d up %s · %s · queue %d · running %d · needs you %d · dead letters %d\n",
		stamp, d.Version, d.PID, dur(now.Sub(d.StartedAt)), claude, st.Metrics.QueueDepth, len(st.Running), len(s.NeedsYou), st.DLQ)
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   agent\tqueued\trunning\tneeds you\tdead\tspent today\tbudget left\tsession")
	for _, a := range st.Agents {
		left := "-"
		if a.BudgetLeft != nil {
			left = money(*a.BudgetLeft)
		}
		sess := "-"
		if a.Interactive {
			sess = "interactive"
		}
		fmt.Fprintf(tw, "   %s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", a.ID, a.Queued, a.Running, a.NeedsHuman, a.DeadLetters,
			money(a.SpentUSD), left, sess)
	}
	tw.Flush()
	for _, r := range st.Running {
		hd := r.Handler
		if r.Profile != "" {
			hd += "/" + r.Profile
		}
		fmt.Fprintf(&b, "   running %s %s %s attempt %d %s msg %s\n", Short(r.ID), r.Agent, hd, r.Attempt, dur(now.Sub(r.StartedAt)),
			Short(r.MessageID))
	}
	for _, x := range s.NeedsYou {
		q := x.HumanQuestion
		if q == "" {
			q = x.Subject
		}
		fmt.Fprintf(&b, "   needs you %s %s from %s %s: %s\n", Short(x.ID), x.To, x.From.Agent, dur(now.Sub(x.UpdatedAt)), clipLine(q, 120))
	}
	for _, x := range s.DLQ {
		fmt.Fprintf(&b, "   dead %s %s from %s %s %s: %s\n", Short(x.ID), x.To, x.From.Agent, x.Intent, dur(now.Sub(x.UpdatedAt)),
			clipLine(x.Reason, 120))
	}
	if s.EventsErr != nil {
		fmt.Fprintf(&b, "   events: cannot read the audit feed: %s\n", oneLine(rpcMessage(s.EventsErr)))
	}
	evs := s.Events
	if events < len(evs) {
		evs = evs[len(evs)-max(events, 0):]
	}
	for _, ev := range evs {
		b.WriteString("   event " + FormatEvent(ev) + "\n")
	}
	return b.String()
}

func clipLine(s string, n int) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
