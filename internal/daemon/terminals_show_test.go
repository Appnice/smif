package daemon

import (
	"strings"
	"testing"
)

func TestShowArgv(t *testing.T) {
	got := strings.Join(showArgv("/usr/bin/gnome-terminal", "/usr/bin/tmux", "worker", true), " ")
	if got != "/usr/bin/gnome-terminal -- /usr/bin/tmux attach -t =worker" {
		t.Fatalf("named: %s", got)
	}
	got = strings.Join(showArgv("gt", "tmux", "worker", false), " ")
	if got != "gt -- tmux attach" {
		t.Fatalf("newest: %s", got)
	}
}
