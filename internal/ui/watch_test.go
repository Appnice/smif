package ui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWatchSummary(t *testing.T) {
	got := WatchSummary(sample(), t0, 5*time.Second, 2)
	golden(t, "watch_summary.golden", got)
	if strings.Contains(got, "\x1b") {
		t.Fatal("the summary has escape codes; it must be plain text")
	}
	if strings.Contains(got, "#40") || !strings.Contains(got, "event 13:09:00 #42 run.started") {
		t.Fatalf("--last 2 not applied:\n%s", got)
	}
	down := WatchSummary(Snapshot{At: t0, Err: errors.New("smif daemon not reachable")}, t0, 5*time.Second, 2)
	if down != "== 13:10:05 · DAEMON DOWN: smif daemon not reachable · retrying every 5s\n" {
		t.Fatalf("down: %q", down)
	}
}
