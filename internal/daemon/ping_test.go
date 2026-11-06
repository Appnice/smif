package daemon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/notify"
)

// setNotifyURL gives a test daemon a phone ping URL.
func setNotifyURL(d *Daemon, url string) {
	c := *d.Config()
	c.Daemon.NotifyURL = url
	d.cfg.Store(&c)
}

func daemonLog(t *testing.T, d *Daemon) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.dataDir, "logs", "smifd.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The watcher's answer event reaches the server, with its name and no answer
// text. The mail route d.notify gets the same ping, with no subject.
func TestPingFromDaemon(t *testing.T) {
	d, _ := testDaemon(t)
	d.notifier = &watchNotes{}
	bodies := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
	}))
	defer srv.Close()
	setNotifyURL(d, srv.URL+"/topic")

	id := codexSession(t, d, "report-idle-paginated.jsonl", t.TempDir())
	v := viewOf(t, d, id)
	d.watchSessions(map[string]watchedSession{id: {turn: "busy", since: time.Unix(1, 0)}}, time.Now())
	select {
	case b := <-bodies:
		if b != viewName(v)+" answered" {
			t.Fatalf("body: %q", b)
		}
		if v.LastAnswer == "" || strings.Contains(b, firstLine(v.LastAnswer)) {
			t.Fatalf("answer text in the body: %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ping")
	}

	d.notify("needs_human", "shop", "SECRET-SUBJECT", "msg-1")
	select {
	case b := <-bodies:
		if b != "shop needs you" {
			t.Fatalf("body: %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ping from d.notify")
	}
}

// A hung server does not delay the watcher, by the 5 s of one POST or by the
// slots running out. A failing server writes a log line with no URL.
func TestPingServerDoesNotDelayWatcher(t *testing.T) {
	d, _ := testDaemon(t)
	d.notifier = &watchNotes{}
	release := make(chan struct{})
	hits := make(chan struct{}, 200)
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		<-release
	}))
	defer hung.Close()
	defer close(release)
	setNotifyURL(d, hung.URL+"/topic-SECRET")
	id := codexSession(t, d, "report-idle-paginated.jsonl", t.TempDir())

	start := time.Now()
	for range 40 {
		d.watchSessions(map[string]watchedSession{id: {turn: "busy", since: time.Unix(1, 0)}}, time.Now())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("40 looks took %s", took)
	}
	select {
	case <-hits:
	case <-time.After(5 * time.Second):
		t.Fatal("the hung server got no ping")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failing.Close()
	d.pinger = notify.NewPinger(func(kind, why string) { d.log.Warn("phone ping failed", "kind", kind, "why", why) })
	setNotifyURL(d, failing.URL+"/topic-SECRET")
	d.watchSessions(map[string]watchedSession{id: {turn: "busy", since: time.Unix(1, 0)}}, time.Now())
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(daemonLog(t, d), "answered 503"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no log line:\n%s", daemonLog(t, d))
		}
	}
	log := daemonLog(t, d)
	if !strings.Contains(log, "too many pings") {
		t.Fatalf("no line for a lost ping:\n%s", log)
	}
	for _, s := range []string{"topic-SECRET", strings.TrimPrefix(hung.URL, "http://"), strings.TrimPrefix(failing.URL, "http://")} {
		if strings.Contains(log, s) {
			t.Fatalf("the log names the URL (%s):\n%s", s, log)
		}
	}
}
