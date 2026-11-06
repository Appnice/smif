package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every kind that raises an OS notification gives a body of a name and what
// happened. The summary and the message id stay home.
func TestPingBody(t *testing.T) {
	type got struct {
		method, body, title, tags string
	}
	seen := make(chan got, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- got{r.Method, string(b), r.Header.Get("Title"), r.Header.Get("Tags")}
	}))
	defer srv.Close()
	p := NewPinger(func(kind, why string) { t.Errorf("%s: %s", kind, why) })
	for kind, want := range map[string]string{
		"session_waiting":    "shop waits for you",
		"session_answered":   "shop answered",
		"session_reminder":   "shop still waits for you",
		"session_silent":     "shop is silent",
		"session_long":       "shop works long",
		"session_queue_gone": "shop ended",
		"needs_human":        "shop needs you",
		"dead_letter":        "shop dead letter",
		"budget":             "shop budget exhausted",
		"claude_unavailable": "Claude Code is unavailable",
	} {
		p.Send(srv.URL, Event{Kind: kind, Agent: "shop", Summary: "SECRET-SUMMARY", MessageID: "SECRET-ID", At: time.Now()})
		g := <-seen
		if g.method != http.MethodPost || g.body != want || g.title != "smif" || g.tags != "bell" {
			t.Fatalf("%s: %+v", kind, g)
		}
	}
}

func TestPingEmptyURLSendsNothing(t *testing.T) {
	var hits atomic.Int32
	p := NewPinger(func(kind, why string) { hits.Add(1) })
	p.Send("", Event{Kind: "session_waiting", Agent: "shop"})
	if len(p.slots) != 0 || hits.Load() != 0 {
		t.Fatal("an empty URL did something")
	}
}

// No failure text names the URL, its host, its port or its secret path.
func TestPingErrorsNameNoURL(t *testing.T) {
	const topic = "/topic-SECRET"
	check := func(name, url string, want string) {
		t.Helper()
		err := NewPinger(nil).Post(url, "x")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", name, err)
		}
		if host := strings.TrimPrefix(strings.TrimSuffix(url, topic), "http://"); strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), host) {
			t.Fatalf("%s names the URL: %v", name, err)
		}
	}
	status := func(code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://example.invalid/")
			w.WriteHeader(code)
		}))
	}
	failing := status(500)
	defer failing.Close()
	check("500", failing.URL+topic, "answered 500")
	moved := status(302)
	defer moved.Close()
	check("redirect", moved.URL+topic, "answered 302") // not followed
	gone := status(200)
	url := gone.URL + topic
	gone.Close()
	check("closed", url, "connection failed")
}

// A hung server takes the slots, and Send still returns at once. The lost
// pings say so, with no URL.
func TestPingHungServerNeverWaits(t *testing.T) {
	release := make(chan struct{})
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	var mu sync.Mutex
	var why []string
	p := NewPinger(func(kind, w string) {
		mu.Lock()
		defer mu.Unlock()
		why = append(why, w)
	})
	start := time.Now()
	for range pingSlots + 50 {
		p.Send(srv.URL, Event{Kind: "session_waiting", Agent: "shop"})
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Send waited %s", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(why) != 50 {
		t.Fatalf("lost pings: %d", len(why))
	}
	for _, w := range why {
		if strings.Contains(w, "127.0.0.1") {
			t.Fatal(w)
		}
	}
}

// The timeout ends a POST to a hung server.
func TestPingTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 5 s")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	err := NewPinger(nil).Post(srv.URL+"/topic-SECRET", "x")
	if err == nil || err.Error() != "no answer in 5 s" {
		t.Fatalf("err: %v", err)
	}
	if d := time.Since(start); d < 4*time.Second || d > 8*time.Second {
		t.Fatalf("took %s", d)
	}
}
