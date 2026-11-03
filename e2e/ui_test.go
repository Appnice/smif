package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
	"github.com/appnice/mailroom/internal/ui"
)

// renderUI polls the live daemon once through the UI's own data source and
// returns the overview as plain text at 120x40.
func renderUI(t *testing.T, m *ui.Model, src *ui.DaemonSource) string {
	t.Helper()
	m.Update(ui.SnapshotMsg(src.Fetch(context.Background())))
	return ansi.Strip(m.Render())
}

// agentRow returns the fields of an agent's row in the AGENTS table.
func agentRow(view, agent string) []string {
	for _, l := range strings.Split(view, "\n") {
		f := strings.Fields(strings.TrimPrefix(l, ">"))
		if len(f) == 8 && f[0] == agent {
			return f
		}
	}
	return nil
}

// UI-1: `mailroom ui` renders the overview from a live daemon (agents with
// counts, running runs, needs you, dead letters) and survives a daemon restart.
func TestUIOverviewLive(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe", Extra: "budget_usd = 3"},
		AgentSpec{ID: "web", Extra: "max_attempts = 1"})
	e.Start()

	if out, err := e.CLI("", "ui", "--help"); err != nil || !strings.Contains(out, "Full-screen terminal UI") || !strings.Contains(out, "--interval") {
		t.Fatalf("ui --help: %v\n%s", err, out)
	}
	if _, err := e.CLI("", "ui"); err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("ui without a terminal: %v", err)
	}

	done := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "hello", Body: "#fake:cost=0.25"})
	e.WaitState(done.ID, 15*time.Second, "done")
	ask := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "store.release",
		Subject: "Ship 1.0.2?", Body: "#fake:result=needs_human"})
	e.WaitState(ask.ID, 15*time.Second, "needs_human")
	slow := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "slow", Body: "#fake:sleep=30"})
	e.WaitState(slow.ID, 15*time.Second, "running")
	queued := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "waits"})
	e.WaitState(queued.ID, 5*time.Second, "queued")
	dead := e.Send("observability", "message.send", envelope.Draft{To: "web", Kind: "tell", Intent: "crash.report", Subject: "boom", Body: "#fake:exit=3"})
	e.WaitState(dead.ID, 15*time.Second, "dead_letter")
	// An interactive session in the web repo.
	var pr map[string]any
	e.Call("presence.update", api.PresenceParams{Event: "start", Cwd: e.Repos["web"], SessionID: "sess-ui-1"}, &pr)

	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	m := ui.New(src, ui.Options{Dashboard: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	out := renderUI(t, m, src)
	t.Logf("live overview at 120x40:\n%s", out)

	want := map[string][]string{
		"cafe":          {"cafe", "1", "1", "1", "0", "$0.25", "$2.75", "-"},
		"observability": {"observability", "0", "0", "0", "0", "$0.00", "-", "-"},
		"web":           {"web", "0", "0", "0", "1", "$0.00", "-", "interactive"},
	}
	for agent, w := range want {
		got := agentRow(out, agent)
		if strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("agent row %s = %v, want %v", agent, got, w)
		}
	}
	slowRun := e.Runs(slow.ID)[0]
	for _, s := range []string{
		"claude 2.1.316 ok", "running 1 · needs you 1 · dead letters 1",
		"AGENTS (3)", "RUNNING (1)", "NEEDS YOU (1)", "DEAD LETTERS (1)",
		ui.Short(slowRun.ID) + "  cafe", ui.Short(ask.ID), ui.Short(dead.ID), "crash.report",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("overview lacks %q", s)
		}
	}
	if m.Down() {
		t.Fatal("model reports the daemon down")
	}

	// Daemon down: the screen keeps the last data and says so.
	e.Stop()
	out = renderUI(t, m, src)
	t.Logf("daemon stopped:\n%s", out)
	if !m.Down() || !strings.Contains(out, "DAEMON DOWN") || !strings.Contains(out, "showing stale data") || agentRow(out, "cafe") == nil {
		t.Fatalf("down state not shown:\n%s", out)
	}
	// A fresh UI with no daemon at all.
	fresh := ui.New(src, ui.Options{Dashboard: true})
	fresh.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if out := renderUI(t, fresh, src); !strings.Contains(out, "DAEMON DOWN") || !strings.Contains(out, "not reachable") {
		t.Fatalf("fresh down view:\n%s", out)
	}

	// The daemon comes back: the same source redials and the view recovers.
	e.Start()
	out = renderUI(t, m, src)
	if m.Down() || strings.Contains(out, "DAEMON DOWN") || agentRow(out, "web") == nil {
		t.Fatalf("no recovery after restart:\n%s", out)
	}
}
