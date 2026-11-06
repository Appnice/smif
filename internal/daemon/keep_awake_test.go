package daemon

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

func TestKeepAwakeFollowsWork(t *testing.T) {
	d := bgDaemon(t, bgRow(bgSession, "busy", t.TempDir()))
	starts, stops := 0, 0
	d.awake.start = func() func() { starts++; return func() { stops++ } }
	c := *d.Config()

	d.watchSessions(nil, time.Now())
	if starts != 0 {
		t.Fatal("off by default, and it held")
	}
	c.Daemon.KeepAwake = true
	d.cfg.Store(&c)
	d.watchSessions(nil, time.Now())
	d.watchSessions(nil, time.Now())
	if starts != 1 || stops != 0 {
		t.Fatalf("while busy: %d starts, %d stops", starts, stops)
	}
	c2 := c
	c2.Daemon.KeepAwake = false
	d.cfg.Store(&c2)
	d.watchSessions(nil, time.Now())
	if stops != 1 {
		t.Fatalf("%d stops", stops)
	}
}

func TestQuietSessionStopsCounting(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		turn  string
		wrote time.Duration
		want  bool
	}{{"busy", time.Minute, true}, {"busy", keepAwakeQuiet + time.Minute, false}, {"idle", time.Minute, false}, {"waiting", time.Minute, false}} {
		v := api.SessionView{Turn: c.turn, Activity: &api.SessionActivity{WroteAt: now.Add(-c.wrote)}}
		if sessionWorks(v, now) != c.want {
			t.Errorf("%+v", c)
		}
	}
}

func TestCaffeinateRunsAndStops(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS")
	}
	stop := startCaffeinate()
	if stop == nil {
		t.Fatal("no caffeinate")
	}
	defer stop()
	out, _ := exec.Command("pmset", "-g", "assertions").Output()
	if !strings.Contains(string(out), "caffeinate") {
		t.Skip("pmset names no caffeinate yet")
	}
}
