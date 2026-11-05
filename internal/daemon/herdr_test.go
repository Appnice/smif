package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/store"
)

// JSON as herdr 0.9.5 prints it (session list --json, pane list,
// pane process-info), trimmed to the fields Smif reads.
func TestHerdrParse(t *testing.T) {
	sessions, err := parseHerdrSessions([]byte(`{"sessions":[
		{"default":false,"name":"work","running":true},
		{"default":false,"name":"old","running":false},
		{"default":true,"name":"default","running":true}]}`))
	if err != nil || !slices.Equal(sessions, []string{"default", "work"}) {
		t.Fatalf("sessions %q %v", sessions, err)
	}
	panes, err := parseHerdrPanes([]byte(`{"panes":[{"agent":"claude","pane_id":"w2:p1","tab_id":"w2:t1","workspace_id":"w2"}],"type":"pane_list"}`))
	if err != nil || len(panes) != 1 || panes[0] != (herdrPane{pane: "w2:p1", tab: "w2:t1", workspace: "w2"}) {
		t.Fatalf("panes %+v %v", panes, err)
	}
	fg := herdrForeground([]byte(`{"process_info":{"foreground_process_group_id":83936,"foreground_processes":[{"argv0":"claude","name":"node","pid":83940}],"pane_id":"w1:p1","shell_pid":83900},"type":"pane_process_info"}`))
	if !slices.Equal(fg, []int{83936, 83940}) || slices.Contains(fg, 83900) {
		t.Fatalf("foreground %v (the shell is not the foreground)", fg)
	}
	if herdrVerb([]string{"--session", "work", "pane", "run", "w1:p1", "text"}) != "pane run" {
		t.Fatal("verb")
	}
}

// Clients are herdr processes on a tty; the server (no tty, or "server")
// and clients of other sessions do not count.
func TestHerdrClients(t *testing.T) {
	ps := `  101 ??       /opt/homebrew/bin/herdr server
  102 ttys003  herdr
  103 ttys004  /opt/homebrew/bin/herdr --session work
  104 ttys005  herdr --session work server
  105 ttys006  vim herdr.go
  106 ??       herdr --session work server`
	if got := parseHerdrClients(ps, "default"); !slices.Equal(got, []int{102}) {
		t.Fatalf("default clients %v", got)
	}
	if got := parseHerdrClients(ps, "work"); !slices.Equal(got, []int{103}) {
		t.Fatalf("work clients %v", got)
	}
}

// Live check against a real herdr (needs one running session and no real
// claude): SMIF_HERDR_REAL=1 go test -run TestHerdrLive ./internal/daemon
func TestHerdrLive(t *testing.T) {
	if os.Getenv("SMIF_HERDR_REAL") == "" {
		t.Skip("set SMIF_HERDR_REAL=1")
	}
	ctx := context.Background()
	if err := herdrOpen(ctx, t.TempDir(), "smif-live", "exec -a claude cat -v"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	sessions, err := herdrSessions(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatal(sessions, err)
	}
	var w struct {
		Workspaces []struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
		} `json:"workspaces"`
	}
	out, err := herdrRun(ctx, "--session", sessions[0], "workspace", "list")
	if err != nil || json.Unmarshal(out, &w) != nil {
		t.Fatal(string(out), err)
	}
	ws := ""
	for _, x := range w.Workspaces {
		if x.Label == "smif-live" {
			ws = x.ID
		}
	}
	if ws == "" {
		t.Fatalf("no workspace: %s", out)
	}
	t.Cleanup(func() { _, _ = herdrRun(ctx, "--session", sessions[0], "workspace", "close", ws) })
	pids, err := exec.Command("pgrep", "-n", "-f", "^claude -v").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pids)))
	h, err := findHerdrPane(ctx, pid)
	if err != nil || h.workspace != ws {
		t.Fatalf("pane %+v %v, want workspace %s", h, err, ws)
	}
	p := store.Presence{PID: pid, Term: termHerdr}
	if err := herdrType(ctx, p, "-n hello"); err != nil {
		t.Fatal(err)
	}
	if err := herdrEnter(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := herdrFocus(ctx, p); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	read, err := exec.Command(herdrPath(), "--session", sessions[0], "pane", "read", h.pane, "--source", "visible").Output()
	if err != nil || !strings.Contains(string(read), "-n hello") {
		t.Fatalf("pane shows %q %v", read, err)
	}
}
