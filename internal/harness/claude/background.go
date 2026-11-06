package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// Background sessions (ADR-0021): `claude agents --json --all` lists them.
// It is a research preview, so a failed, missing or changed command gives
// no rows and no error. Discover runs on every session.list and under the
// status line, so it only reads the last result: one goroutine at a time
// runs the command, at most once in every 5 s. Reading a background session
// uses the normal transcript route.

const (
	bgEvery   = 5 * time.Second
	bgTimeout = 5 * time.Second
	bgKillGap = time.Second // a killed command's children can hold the pipe
)

// bgList is the last result of the command.
type bgList struct {
	every, timeout time.Duration

	mu            sync.Mutex
	rows          []harness.Session
	ranAt         time.Time // when the last run started
	running       bool
	state, detail string // for smif doctor

	// Only the one running refresh uses these.
	turns map[string]bgTurn
	ended map[string]bgEnded
}

// bgTurn is when a refresh first saw a state: the list gives no time.
type bgTurn struct {
	state string
	at    time.Time
}

// bgEnded is what a finished session's transcript says, read again only
// when the transcript changes.
type bgEnded struct {
	stamp    string
	answerAt time.Time
}

// bgRow is the part of one row of the command that Smif uses.
type bgRow struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	State     string `json:"state"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"`
}

// backgroundTurn maps a row's state to a turn ("" = a word Smif does not
// know, which reads unknown).
func backgroundTurn(state string) string {
	switch state {
	case "blocked":
		return "waiting"
	case "working":
		return "busy"
	case "done", "idle":
		return "idle"
	}
	return ""
}

// backgroundRows returns the last result and starts a new run when it is
// old. It never waits for the command.
func (p *Provider) backgroundRows() []harness.Session {
	if p.Command == nil {
		return nil
	}
	p.bg.mu.Lock()
	defer p.bg.mu.Unlock()
	if !p.bg.running && time.Since(p.bg.ranAt) >= p.bg.every {
		p.bg.running, p.bg.ranAt = true, time.Now()
		go p.refreshBackground(p.Command())
	}
	return p.bg.rows
}

func (p *Provider) refreshBackground(command string) {
	ctx, cancel := context.WithTimeout(context.Background(), p.bg.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "agents", "--json", "--all")
	cmd.WaitDelay = bgKillGap
	out, err := cmd.Output()
	var rows []harness.Session
	state, detail := "none", "`claude agents --json --all` did not answer"
	if err == nil {
		rows, state, detail = p.parseBackground(out, time.Now())
	} else {
		detail += ": " + err.Error()
	}
	p.bg.mu.Lock()
	defer p.bg.mu.Unlock()
	p.bg.rows, p.bg.state, p.bg.detail, p.bg.running = rows, state, detail, false
}

// parseBackground turns the command's output into sessions. An id reaches
// argv in `claude attach <id>`, so a row with a bad id does not count.
func (p *Provider) parseBackground(out []byte, now time.Time) (rows []harness.Session, state, detail string) {
	var list []bgRow
	if err := json.Unmarshal(out, &list); err != nil {
		p.bg.turns, p.bg.ended = nil, nil
		return nil, "changed", "`claude agents --json --all` is not a JSON list of rows"
	}
	turns, ended := map[string]bgTurn{}, map[string]bgEnded{}
	at := map[string]int{}
	bad, unknown := 0, 0
	for _, r := range list {
		if r.Kind != "background" {
			continue
		}
		if !harness.ValidID(r.SessionID) || !harness.ValidID(r.ID) || strings.HasPrefix(r.ID, "-") {
			bad++
			continue
		}
		if i, ok := at[r.SessionID]; ok && rows[i].StartedAt > r.StartedAt {
			continue
		}
		s := harness.Session{AttachID: r.ID, PID: r.PID, SessionID: r.SessionID, Cwd: r.Cwd, Name: r.Name,
			StartedAt: r.StartedAt, Status: r.State}
		if r.State == "done" {
			e := p.endedAt(r.SessionID)
			ended[r.SessionID] = e
			s.LastAnswerAt, s.EndedAt = e.answerAt, e.answerAt
			if started := time.UnixMilli(r.StartedAt); started.After(s.EndedAt) {
				s.EndedAt = started
			}
		}
		if backgroundTurn(r.State) == "" {
			unknown++
		}
		t, ok := p.bg.turns[r.SessionID]
		if !ok || t.state != r.State {
			t = bgTurn{state: r.State, at: now}
			if !s.EndedAt.IsZero() {
				t.at = s.EndedAt
			}
		}
		turns[r.SessionID] = t
		s.StatusAt = t.at
		if i, ok := at[r.SessionID]; ok {
			rows[i] = s
			continue
		}
		at[r.SessionID] = len(rows)
		rows = append(rows, s)
	}
	p.bg.turns, p.bg.ended = turns, ended
	if bad > 0 {
		return rows, "changed", fmt.Sprintf("%d background rows have no usable id or sessionId", bad)
	}
	detail = fmt.Sprintf("%d background sessions", len(rows))
	if unknown > 0 {
		detail += fmt.Sprintf(", %d with a state Smif does not know", unknown)
	}
	return rows, "ok", detail
}

// endedAt is when a finished session last answered. The list gives no end
// time, and the file's time is no answer: Claude Code 2.1.318 writes to a
// transcript long after the session ended.
func (p *Provider) endedAt(sessionID string) bgEnded {
	stamp := p.Stamp(sessionID)
	if e, ok := p.bg.ended[sessionID]; ok && e.stamp == stamp {
		return e
	}
	e := bgEnded{stamp: stamp}
	if stamp != "" {
		_, e.answerAt = harness.LastAnswer(p.Read(sessionID, 4).Turns)
	}
	return e
}

// CheckBackground is smif doctor's line for the list. The command is a
// research preview, so no state here is a failure.
func (p *Provider) CheckBackground() (state, detail string) {
	p.bg.mu.Lock()
	defer p.bg.mu.Unlock()
	if p.bg.state == "" {
		return "none", "`claude agents --json --all` has not run yet"
	}
	return p.bg.state, p.bg.detail
}
