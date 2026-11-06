package daemon

import (
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

func remoteDaemon(t *testing.T, rows ...api.SessionView) *Daemon {
	t.Helper()
	d, _ := testDaemon(t)
	c := *d.Config()
	c.Daemon.Remotes = []string{"mini"}
	d.cfg.Store(&c)
	d.remotes.hosts = map[string]remote{"mini": {rows: rows, at: time.Now()}}
	return d
}

func TestRemoteWaitNotifiesOnce(t *testing.T) {
	v := api.SessionView{SessionID: "mini/a", Host: "mini", Repo: "mini:/srv/shop", Turn: "busy"}
	d := remoteDaemon(t, v)
	notes := &watchNotes{}
	d.notifier = notes
	seen := d.watchSessions(nil, time.Now())
	if len(notes.kinds) != 0 {
		t.Fatalf("a first look is no news: %v", notes.kinds)
	}
	v.Turn = "waiting"
	d.remotes.hosts["mini"] = remote{rows: []api.SessionView{v}, at: time.Now()}
	seen = d.watchSessions(seen, time.Now())
	d.watchSessions(seen, time.Now())
	if len(notes.kinds) != 1 || notes.kinds[0] != "session_waiting" {
		t.Fatalf("notifications: %v", notes.kinds)
	}
}

func TestStaleRemoteRowsLeave(t *testing.T) {
	d := remoteDaemon(t, api.SessionView{SessionID: "mini/a"})
	now := time.Now()
	if len(d.remoteRows(now)) != 1 || len(d.remoteRows(now.Add(remoteStale+time.Second))) != 0 {
		t.Fatal("stale rows stayed")
	}
}
