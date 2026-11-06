package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/machines"
)

type stubMachineDaemon struct{ rows []api.ProviderCheck }

func (d *stubMachineDaemon) Reload() error { return nil }
func (d *stubMachineDaemon) Reached(string) (bool, string, error) {
	return true, "1 sessions", nil
}
func (d *stubMachineDaemon) Remotes() ([]api.ProviderCheck, error) { return d.rows, nil }

// machinesModel is an open screen with two found machines and an added one.
func machinesModel(t *testing.T) (*Model, *stubMachineDaemon) {
	t.Helper()
	d := &stubMachineDaemon{rows: []api.ProviderCheck{{Name: "mini.local", State: "ok", Detail: "1 sessions"}}}
	m := New(nil, Options{MachineDaemon: d, Now: func() time.Time { return time.Unix(1000, 0) }})
	m.width, m.height = 120, 40
	m.snap = &Snapshot{Sessions: []api.SessionView{{SessionID: "mini.local/a", Host: "mini.local", Title: "deploy", Turn: "waiting"}, {SessionID: "b", Repo: "/here"}}}
	m.mach.discover = func(context.Context, map[string]bool) []machines.Found {
		return []machines.Found{{Name: "devbox", Target: "devbox", Source: "~/.ssh/config", Open: true}, {Name: "nas", Target: "nas", Source: "Tailscale"}}
	}
	m.openMachines()
	m.Update(machineHealthMsg{d.rows})
	m.Update(machinesFoundMsg{m.mach.discover(nil, nil)})
	return m, d
}

func screen(m *Model) string { return ansi.Strip(m.Render()) }

func press(m *Model, k string) {
	msg := tea.KeyPressMsg{Text: k}
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	}
	m.Update(msg)
}

func TestMachinesList(t *testing.T) {
	m, _ := machinesModel(t)
	s := screen(m)
	for _, want := range []string{"Machines", "THIS MACHINE", "1 sessions", "ADDED", "mini.local", "FOUND", "devbox", "ssh open", "nas", "ssh closed", "SOMEWHERE ELSE", "Add this machine", "Install smif there"} {
		if !strings.Contains(s, want) {
			t.Fatalf("no %q:\n%s", want, s)
		}
	}
	if m.mach.selected().name != "devbox" {
		t.Fatalf("cursor on %s", m.mach.selected().name)
	}
	press(m, "down")
	s = screen(m)
	if !strings.Contains(s, "ssh does not answer") || !strings.Contains(s, machines.CommandLinux[:40]) {
		t.Fatalf("closed:\n%s", s)
	}
}

func TestMachinesMouse(t *testing.T) {
	m, _ := machinesModel(t)
	y := -1
	lines := strings.Split(screen(m), "\n")
	for i, l := range lines {
		if strings.Contains(l, "mini.local") && strings.Contains(l, "1 sessions") {
			y = i
		}
	}
	if y < 0 {
		t.Fatalf("no row:\n%s", strings.Join(lines, "\n"))
	}
	m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if m.mach.selected().name != "mini.local" {
		t.Fatalf("click picked %s", m.mach.selected().name)
	}
	s := screen(m)
	if !strings.Contains(s, "Connected") || !strings.Contains(s, "deploy") || !strings.Contains(s, "Remove") {
		t.Fatalf("added:\n%s", s)
	}
	m.Update(tea.MouseReleaseMsg{X: 5, Y: y})
	if m.sel != nil {
		t.Fatal("a click started a selection")
	}
}

// Questions come back to the row, the password stays off the screen and out of
// the box after enter, and the row ends in ADDED.
func TestMachinesSetup(t *testing.T) {
	m, _ := machinesModel(t)
	got := make(chan string, 2)
	release := make(chan struct{})
	m.mach.start = func(ctx context.Context, s *machines.Setup) error {
		s.Report(machines.Event{Step: machines.StepReach, State: machines.Done})
		s.Report(machines.Event{Step: machines.StepLogin, State: machines.Running})
		_, pass, ok := s.Ask.Login("me", "")
		if !ok {
			return machines.ErrStopped
		}
		got <- pass
		s.Report(machines.Event{Step: machines.StepLogin, State: machines.Done, Detail: "password used once, not stored"})
		s.Report(machines.Event{Step: machines.StepLinger, State: machines.Running})
		pass, ok = s.Ask.Sudo("")
		got <- map[bool]string{true: pass, false: "skipped"}[ok]
		s.Report(machines.Event{Step: machines.StepLinger, State: machines.Skipped, Detail: "can drop out"})
		<-release
		return nil
	}
	drain := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case msg := <-m.mach.events:
				m.Update(msg)
			case <-time.After(2 * time.Second):
				t.Fatal("no event")
			}
		}
	}
	press(m, "enter")
	drain(3)
	s := screen(m)
	if !strings.Contains(s, "Password") || !strings.Contains(s, "needs you") {
		t.Fatalf("login:\n%s", s)
	}
	for _, c := range "hunter2" {
		press(m, string(c))
	}
	s = screen(m)
	if strings.Contains(s, "hunter2") || !strings.Contains(s, "•••••••") {
		t.Fatalf("the password shows:\n%s", s)
	}
	press(m, "enter")
	if p := <-got; p != "hunter2" {
		t.Fatalf("got %q", p)
	}
	if m.mach.secret.Value() != "" {
		t.Fatal("the box kept the password")
	}
	drain(3)
	if s = screen(m); !strings.Contains(s, "sudo") || !strings.Contains(s, "Turn on") {
		t.Fatalf("sudo:\n%s", s)
	}
	press(m, "esc")
	if p := <-got; p != "skipped" {
		t.Fatalf("esc gave %q", p)
	}
	drain(1)
	close(release)
	drain(1)
	r := m.mach.row("devbox")
	if r.kind != mAdded || !r.skipped {
		t.Fatalf("row %+v", r)
	}
	if s = screen(m); !strings.Contains(s, "Linger is off") {
		t.Fatalf("added:\n%s", s)
	}
}

func TestMachinesStopAndFail(t *testing.T) {
	m, _ := machinesModel(t)
	m.mach.start = func(ctx context.Context, s *machines.Setup) error {
		_, _, ok := s.Ask.Login("", "")
		if !ok {
			return machines.ErrStopped
		}
		return nil
	}
	press(m, "enter")
	m.Update(<-m.mach.events)
	press(m, "esc")
	m.Update(<-m.mach.events)
	if r := m.mach.row("devbox"); r.kind != mFound || r.cancel != nil {
		t.Fatalf("stopped: %+v", r)
	}
	m.Update(machineDoneMsg{name: "devbox", err: context.DeadlineExceeded})
	if r := m.mach.row("devbox"); r.kind != mFailed {
		t.Fatalf("failed: %+v", r)
	}
	if s := screen(m); !strings.Contains(s, "Try again") {
		t.Fatalf("fail:\n%s", s)
	}
}

func TestMachinesAddress(t *testing.T) {
	m, _ := machinesModel(t)
	m.mach.start = func(ctx context.Context, s *machines.Setup) error {
		<-ctx.Done()
		return machines.ErrStopped
	}
	press(m, "a")
	for _, c := range "-oX" {
		press(m, string(c))
	}
	press(m, "enter")
	if m.mach.selected().name != "devbox" || !strings.Contains(screen(m), "not an ssh address") {
		t.Fatalf("took a bad address:\n%s", screen(m))
	}
	m.mach.addr = lineInput{}
	for _, c := range "me@10.0.0.9" {
		press(m, string(c))
	}
	press(m, "enter")
	r := m.mach.selected()
	if r.name != "10.0.0.9" || r.target != "me@10.0.0.9" || r.kind != mRun {
		t.Fatalf("row %+v", r)
	}
	m.stopMachine(r)
}

// The daemon names a host with its user, and the row started without one: it
// stays one machine, during the setup and after a restart of the screen.
func TestMachinesOneRowPerMachine(t *testing.T) {
	m, d := machinesModel(t)
	m.mach.start = func(ctx context.Context, s *machines.Setup) error {
		<-ctx.Done()
		return machines.ErrStopped
	}
	press(m, "enter")
	rows := []api.ProviderCheck{{Name: "mini.local", State: "ok"}, {Name: "me@devbox", State: "ok"}}
	d.rows = rows
	m.Update(machineHealthMsg{rows})
	n := 0
	for _, r := range m.mach.rows {
		if strings.Contains(r.name, "devbox") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d devbox rows during the setup", n)
	}
	m.stopMachine(m.mach.row("devbox"))
	m.Update(<-m.mach.events)
	m.Update(machineHealthMsg{rows})
	if r := m.mach.row("devbox"); r.kind != mAdded || r.target != "me@devbox" {
		t.Fatalf("row %+v", r)
	}
	m.Update(machinesFoundMsg{[]machines.Found{{Name: "devbox", Target: "devbox", Open: true}}})
	if len(m.mach.rows) != 4 {
		t.Fatalf("%d rows", len(m.mach.rows))
	}
	// A host that left the config leaves the list or goes back to found.
	m.Update(machineHealthMsg{[]api.ProviderCheck{{Name: "me@devbox", State: "ok"}}})
	if m.mach.row("mini.local") != nil {
		t.Fatal("mini.local stayed")
	}
}

func TestMachinesNarrow(t *testing.T) {
	m, _ := machinesModel(t)
	m.width, m.height = 80, 24
	s := screen(m)
	if !strings.Contains(s, "MACHINES") || !strings.Contains(s, "DEVBOX") {
		t.Fatalf("narrow:\n%s", s)
	}
}

func TestMachinesKeyOpens(t *testing.T) {
	m := New(nil, Options{})
	m.width, m.height = 120, 40
	m.snap = &Snapshot{}
	press(m, "m")
	if m.mach.open {
		t.Fatal("opened with no daemon")
	}
}
