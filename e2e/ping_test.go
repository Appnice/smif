package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// PHONE PING (ADR-0021 section 6): the real daemon posts waiting, reminder
// and answered events with the session's name and what happened. A prompt, an
// answer and a command with a marker stay home, and the URL reaches no log,
// no audit row and no doctor line.
func TestPhonePing(t *testing.T) {
	const prompt, command, answer = "PROMPT-MARKER ship it", "COMMAND-MARKER git push", "ANSWER-MARKER pushed"
	type ping struct {
		body, title, tags, method string
	}
	var mu sync.Mutex
	var pings []ping
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		pings = append(pings, ping{string(b), r.Header.Get("Title"), r.Header.Get("Tags"), r.Method})
	}))
	defer srv.Close()
	url := srv.URL + "/topic-URL-MARKER"
	seen := func(body string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, p := range pings {
			if p.body == body {
				n++
			}
		}
		return n
	}

	e := NewEnv(t, fmt.Sprintf("notify_url = %q\nremind_waiting = \"2s\"", url), "", AgentSpec{ID: "app", Rules: digestRule()})
	out, err := e.CLI("", "config", "validate", "--config", e.Config())
	if err != nil || strings.Contains(out, "URL-MARKER") {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	// `smif notify test` needs no daemon.
	if out, err := e.CLI("", "notify", "test"); err != nil || !strings.Contains(out, "ping sent") || seen("smif test ping") != 1 {
		t.Fatalf("notify test: %v\n%s", err, out)
	}
	e.Start()
	dir := filepath.Join(e.Home, ".claude", "sessions")
	pdir := filepath.Join(e.Home, ".claude", "projects", "-r-app")
	for _, d := range []string{dir, pdir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	status := func(s string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": "ping-session-1", "cwd": e.Repos["app"], "kind": "interactive",
			"name": "shop", "status": s, "startedAt": time.Now().UnixMilli() - 60000, "statusUpdatedAt": time.Now().UnixMilli()})
		if err := os.WriteFile(filepath.Join(dir, "p.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	lines := []string{
		`{"type":"user","timestamp":"` + at + `","message":{"content":"` + prompt + `"}}`,
		`{"type":"assistant","timestamp":"` + at + `","message":{"content":[{"type":"tool_use","id":"p1","name":"Bash","input":{"command":"` + command + `"}}]}}`,
	}
	transcript := func(ls ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pdir, "ping-session-1.jsonl"), []byte(strings.Join(ls, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	transcript(lines...)
	status("busy")
	time.Sleep(5 * time.Second) // the watcher sees it busy first
	status("waiting")
	e.WaitFor("waiting ping", 15*time.Second, func() bool { return seen("app waits for you") == 1 })
	e.WaitFor("reminder ping", 15*time.Second, func() bool { return seen("app still waits for you") >= 1 })
	status("busy")
	time.Sleep(3 * time.Second)
	transcript(append(lines,
		`{"type":"user","timestamp":"`+at+`","message":{"content":[{"type":"tool_result","tool_use_id":"p1","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"`+time.Now().UTC().Format(time.RFC3339)+`","message":{"content":[{"type":"text","text":"`+answer+`"}]}}`)...)
	status("idle")
	e.WaitFor("answered ping", 15*time.Second, func() bool { return seen("app answered") == 1 })

	// The OS route got the text, so the markers were there to leak.
	notes, _ := os.ReadFile(filepath.Join(e.Root, "notify.jsonl"))
	if !strings.Contains(string(notes), "COMMAND-MARKER") || !strings.Contains(string(notes), "ANSWER-MARKER") {
		t.Fatalf("notifications:\n%s", notes)
	}
	mu.Lock()
	got := append([]ping(nil), pings...)
	mu.Unlock()
	for _, p := range got {
		if p.method != http.MethodPost || p.title != "smif" || p.tags != "bell" || regexp.MustCompile(`MARKER`).MatchString(p.body) {
			t.Fatalf("ping: %+v", p)
		}
	}

	// A bad URL fails a reload and keeps the old config, and the error
	// names no URL.
	cfg, err := os.ReadFile(e.Config())
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(cfg), url, strings.Replace(url, "127.0.0.1", "example.com", 1), 1)
	if err := os.WriteFile(e.Config(), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = e.CLI("", "reload")
	if err == nil || strings.Contains(out, "URL-MARKER") || strings.Contains(err.Error(), "URL-MARKER") {
		t.Fatalf("reload: %v\n%s", err, out)
	}
	stdout, stderr, verr := e.CLIDir(e.Root, "", nil, "config", "validate", "--config", e.Config())
	if verr == nil || !strings.Contains(stderr, "http is allowed only to localhost") || strings.Contains(stdout+stderr, "URL-MARKER") {
		t.Fatalf("validate a bad URL: %v\n%s%s", verr, stdout, stderr)
	}
	if err := os.WriteFile(e.Config(), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	doctor, _ := e.CLI("", "doctor") // it can fail on checks the test host lacks
	if !strings.Contains(doctor, "phone ping is on") {
		t.Fatalf("doctor:\n%s", doctor)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	check := func(what, text string) {
		t.Helper()
		if strings.Contains(text, "URL-MARKER") || strings.Contains(text, host) {
			t.Fatalf("%s names the URL:\n%s", what, text)
		}
	}
	check("doctor", doctor)
	check("audit", fmt.Sprint(e.Audit()))
	logs, _ := os.ReadFile(filepath.Join(e.Root, "data", "logs", "smifd.jsonl"))
	check("log", string(logs))
}
