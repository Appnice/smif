package daemon

import (
	"database/sql"
	"sort"
	"time"

	"github.com/appnice/mailroom/internal/api"
)

// stats computes stats.get (ADR-0011) straight from the store: messages by
// creation time, runs by start time, merged repeats by audit time. Whether
// a message ever needed the human comes from the audit trail (a transition
// to needs_human), not from its current state.
func (d *Daemon) stats(p api.StatsParams) (*api.StatsResult, error) {
	now := time.Now()
	since := p.Since
	if since.IsZero() {
		since = now.AddDate(0, 0, -14)
	}
	cfg := d.Config()
	res := &api.StatsResult{Since: since, Until: now, Total: api.StatsRow{Key: "all"},
		KeptFrom: now.Add(-cfg.Daemon.MessageRetention.Duration)}
	agents, days := map[string]*api.StatsRow{}, map[string]*api.StatsRow{}
	rows := func(agent string, ts int64) []*api.StatsRow {
		a := agents[agent]
		if a == nil {
			a = &api.StatsRow{Key: agent}
			agents[agent] = a
		}
		k := time.UnixMilli(ts).Local().Format("2006-01-02")
		dd := days[k]
		if dd == nil {
			dd = &api.StatsRow{Key: k}
			days[k] = dd
		}
		return []*api.StatsRow{&res.Total, a, dd}
	}
	db := d.store.DB()
	ms := since.UnixMilli()

	q, err := db.Query(`
WITH nh AS (SELECT DISTINCT message_id FROM audit WHERE event='message.transition'
            AND json_extract(detail,'$.to_state')='needs_human'),
     hc AS (SELECT DISTINCT message_id FROM audit WHERE event='message.transition'
            AND json_extract(detail,'$.to_state')='rejected' AND json_extract(detail,'$.human')=1)
SELECT to_agent, created_at, kind, COALESCE(handler,''), state,
       id IN (SELECT message_id FROM nh), id IN (SELECT message_id FROM hc)
FROM messages WHERE created_at >= ?`, ms)
	if err != nil {
		return nil, err
	}
	for q.Next() {
		var agent, kind, handler, state string
		var created int64
		var needed, cancelled bool
		if err := q.Scan(&agent, &created, &kind, &handler, &state, &needed, &cancelled); err != nil {
			q.Close()
			return nil, err
		}
		if kind == "reply" {
			res.Replies++
			continue
		}
		for _, r := range rows(agent, created) {
			r.Inbound++
			switch {
			case needed:
				r.NeededYou++
				switch state {
				case "done":
					r.Answered++
				case "needs_human":
					r.Waiting++
				case "rejected", "expired":
					r.CancelledNeeded++
				default:
					r.InProgress++ // answered and running again
				}
			case state == "done":
				r.Auto++
				if r.AutoBy == nil {
					r.AutoBy = map[string]int{}
				}
				r.AutoBy[handler]++
			case state == "rejected" && cancelled:
				r.Cancelled++
			case state == "rejected":
				r.Rejected++
			case state == "dead_letter":
				r.Dead++
			case state == "expired":
				r.Expired++
			default:
				r.InProgress++
			}
		}
	}
	q.Close()
	if err := q.Err(); err != nil {
		return nil, err
	}

	if err := scan(db, `SELECT COALESCE(agent,''), ts FROM audit WHERE event='message.coalesced' AND ts >= ?`, ms,
		func(s func(...any) error) error {
			var agent string
			var ts int64
			if err := s(&agent, &ts); err != nil {
				return err
			}
			for _, r := range rows(agent, ts) {
				r.Merged++
			}
			return nil
		}); err != nil {
		return nil, err
	}

	if err := scan(db, `SELECT agent, started_at, handler, state, COALESCE(input_tokens,0), COALESCE(output_tokens,0),
       COALESCE(cost_usd,0) FROM runs WHERE started_at >= ?`, ms, func(s func(...any) error) error {
		var agent, handler, state string
		var started, in, out int64
		var cost float64
		if err := s(&agent, &started, &handler, &state, &in, &out, &cost); err != nil {
			return err
		}
		for _, r := range rows(agent, started) {
			r.Runs++
			switch handler {
			case "worker":
				r.WorkerRuns++
			case "command":
				r.CommandRuns++
			}
			if state == "failed" {
				r.FailedRuns++
			}
			r.InputTokens += in
			r.OutputTokens += out
			r.CostUSD += cost
		}
		return nil
	}); err != nil {
		return nil, err
	}

	for _, a := range agents {
		res.Agents = append(res.Agents, *a)
	}
	sort.Slice(res.Agents, func(i, j int) bool { return res.Agents[i].Key < res.Agents[j].Key })
	for _, x := range days {
		res.Days = append(res.Days, *x)
	}
	sort.Slice(res.Days, func(i, j int) bool { return res.Days[i].Key < res.Days[j].Key })
	db = d.store.DB()
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit WHERE event='session.prompt' AND ts >= ?`, ms).Scan(&res.Sessions.Typed)
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit WHERE event='session.focus' AND ts >= ?`, ms).Scan(&res.Sessions.Focus)
	_ = db.QueryRow(`SELECT COALESCE(SUM(m.from_agent='human'),0), COALESCE(SUM(m.from_agent<>'human'),0)
FROM session_asks a JOIN messages m ON m.id=a.message_id WHERE a.created_at >= ?`, ms).Scan(&res.Sessions.WorkerAsks, &res.Sessions.AgentAsks)
	return res, nil
}

func scan(db *sql.DB, query string, arg any, each func(func(...any) error) error) error {
	rows, err := db.Query(query, arg)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}
