//go:build darwin || linux

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/ui"
)

const (
	histDead  = "aaaaaaaa-0000-4000-8000-000000000001"
	histBusy  = "bbbbbbbb-0000-4000-8000-000000000002"
	histAlone = "cccccccc-0000-4000-8000-000000000003"
	histTabA  = "dddddddd-0000-4000-8000-000000000004"
	histTabB  = "eeeeeeee-0000-4000-8000-000000000005"
	histSDK   = "ffffffff-0000-4000-8000-000000000006"
)

// historyFile writes a transcript under the fixture's CLAUDE_CONFIG_DIR.
func historyFile(t *testing.T, e *Env, id, entry, cwd, prompt, answer string) {
	t.Helper()
	dir := filepath.Join(e.Home, ".claude", "projects", "-repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	line := func(typ string, content any) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "entrypoint": entry, "cwd": cwd, "timestamp": stamp,
			"message": map[string]any{"role": typ, "content": content}})
		return string(b) + "\n"
	}
	body := line("user", prompt) + line("assistant", []map[string]any{{"type": "text", "text": answer}, {"type": "tool_use", "id": "t", "name": "Bash", "input": map[string]string{"command": "echo hidden-word"}}})
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// liveTab is a process with a session file, as a Claude Code tab has.
func liveTab(t *testing.T, e *Env, id, cwd string) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "300")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.ProcessState == nil {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	})
	dir := filepath.Join(e.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{"pid": c.Process.Pid, "sessionId": id, "cwd": cwd, "kind": "interactive", "name": "tab-" + id[:4],
		"status": "busy", "startedAt": now, "statusUpdatedAt": now})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func closeTab(c *exec.Cmd) {
	_ = c.Process.Kill()
	_ = c.Wait()
}

func closedOf(e *Env) map[string]api.ClosedSession {
	var l api.ClosedList
	e.Call("session.closed", nil, &l)
	out := map[string]api.ClosedSession{}
	for _, s := range l.Sessions {
		out[s.SessionID] = s
	}
	return out
}

// HISTORY AND RESTART (ADR-0021) on the real daemon with a fixture
// CLAUDE_CONFIG_DIR: the index and the search leave out SDK runs and tool
// text; a dead pid in open-sessions.json gives a restore list; one session
// closing alone does not; two within 10 s do; the home line and R show
// them; A reopens through the log terminal, 500 ms apart.
func TestHistoryRestart(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	repo := e.Repos["app"]
	historyFile(t, e, histDead, "cli", repo, "fix the login form", "the login form works now")
	historyFile(t, e, histBusy, "cli", repo, "write the migration", "still on it")
	historyFile(t, e, histAlone, "cli", repo, "closed alone", "bye")
	historyFile(t, e, histTabA, "cli", repo, "first tab", "ok")
	historyFile(t, e, histTabB, "cli", repo, "second tab", "ok")
	historyFile(t, e, histSDK, "sdk-cli", repo, "a login worker run", "login done")
	sub := filepath.Join(e.Home, ".claude", "projects", "-repo", histDead, "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-1.jsonl"), []byte(`{"type":"user","entrypoint":"cli","cwd":"/x","message":{"content":"login"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The daemon died with two sessions open, then their processes went.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"open":[{"session_id":%q,"pid":%d,"dir":%q,"name":"login"},{"session_id":%q,"pid":%d,"dir":%q,"name":"migration","busy":true}]}`,
		histDead, dead.Process.Pid, repo, histBusy, dead.Process.Pid, repo)
	if err := os.WriteFile(filepath.Join(e.Root, "data", "open-sessions.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	e.Start()

	closed := closedOf(e)
	if len(closed) != 2 || !closed[histBusy].DiedWorking || closed[histDead].DiedWorking {
		t.Fatalf("restart list: %+v", closed)
	}
	var rows []api.HistoryRow
	e.Call("session.history", nil, &rows)
	if len(rows) != 5 {
		t.Fatalf("history: %+v", rows)
	}
	for _, r := range rows {
		if r.SessionID == histSDK || r.Dir != repo {
			t.Errorf("row: %+v", r)
		}
	}
	var res api.SessionSearchResult
	e.Call("session.search", api.SessionSearchParams{Query: "LOGIN"}, &res)
	if len(res.Hits) != 2 || res.Stopped != "" {
		t.Fatalf("search: %+v", res)
	}
	e.Call("session.search", api.SessionSearchParams{Query: "hidden-word"}, &res)
	if len(res.Hits) != 0 {
		t.Fatalf("a tool call was searched: %+v", res)
	}

	// Refused calls open nothing, and an agent token reaches none of this.
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("session.resume", api.SessionResumeParams{SessionID: histSDK}, nil, 5*time.Second); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("an id not in the index: %v", err)
	}
	if err := c.Call("session.resume", map[string]any{"session_id": "--help"}, nil, 5*time.Second); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("a flag as id: %v", err)
	}
	ac := e.AgentClient("app")
	defer ac.Close()
	for _, m := range []string{"session.history", "session.search", "session.resume", "session.closed", "session.reopen"} {
		if err := ac.Call(m, map[string]any{"query": "x", "session_id": histDead}, nil, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s with an agent token: %v", m, err)
		}
	}

	// One session closing alone stays off the list; two within 10 s join.
	alone := liveTab(t, e, histAlone, repo)
	tabA := liveTab(t, e, histTabA, repo)
	tabB := liveTab(t, e, histTabB, repo)
	file := filepath.Join(e.Root, "data", "open-sessions.json")
	e.WaitFor("the open list", 10*time.Second, func() bool {
		b, _ := os.ReadFile(file)
		return strings.Contains(string(b), histAlone) && strings.Contains(string(b), histTabA) && strings.Contains(string(b), histTabB)
	})
	closeTab(alone)
	time.Sleep(12 * time.Second) // past the 10 s window, with a look every 2 s
	if b, _ := os.ReadFile(file); strings.Contains(string(b), `"session_id": "`+histAlone) {
		t.Errorf("a closed session stays in the file:\n%s", b)
	}
	if _, ok := closedOf(e)[histAlone]; ok {
		t.Fatal("one session closing alone joined the list")
	}
	closeTab(tabA)
	closeTab(tabB)
	e.WaitFor("two sessions vanished", 10*time.Second, func() bool {
		c := closedOf(e)
		return len(c) == 4 && c[histTabA].Name == "tab-dddd" && c[histTabB].DiedWorking
	})
	if _, ok := closedOf(e)[histAlone]; ok {
		t.Fatal("the session that closed alone joined the list")
	}

	// The home line, the R screen and A.
	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return e.Client() }}
	defer src.Close()
	u := startUI(t, ui.New(src, ui.Options{}))
	u.until("the home line", 10*time.Second, has("4 sessions closed · R reopens"))
	u.press("R")
	u.until("the closed rows", 10*time.Second, has("HISTORY · 5 sessions · 4 closed", "died working", "closed alone"))
	u.press("/", "l", "o", "g", "i", "n", "ctrl+s")
	u.until("the hits", 10*time.Second, has(`SEARCH "login" · 2 hits`, "the login form works now"))
	u.press("esc", "esc")
	u.press("A")
	u.until("the list to empty", 20*time.Second, has("HISTORY · 5 sessions \n"))

	b, err := os.ReadFile(typed)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var ats []int64
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var o struct {
			Open bool     `json:"open"`
			Dir  string   `json:"dir"`
			Argv []string `json:"argv"`
			At   int64    `json:"at_ms"`
		}
		if json.Unmarshal([]byte(l), &o) != nil || !o.Open {
			continue
		}
		if len(o.Argv) != 3 || o.Argv[0] != fakeBin || o.Argv[1] != "--resume" || o.Dir != repo {
			t.Errorf("open: %+v", o)
		}
		ids, ats = append(ids, o.Argv[2]), append(ats, o.At)
	}
	if len(ids) != 4 {
		t.Fatalf("opened: %v", ids)
	}
	for i := 1; i < len(ats); i++ {
		if gap := ats[i] - ats[i-1]; gap < 490 {
			t.Errorf("gap %d: %d ms", i, gap)
		}
	}
	t.Logf("opened %d sessions, %d ms from first to last", len(ids), ats[len(ats)-1]-ats[0])
	opens := 0
	for _, a := range e.Audit() {
		if a["event"] == "session.resume" {
			opens++
		}
	}
	if opens != 4 {
		t.Errorf("audit rows: %d", opens)
	}

	// The list is empty after a restart too.
	e.Stop()
	e.Start()
	if c := closedOf(e); len(c) != 0 {
		t.Errorf("after a restart: %+v", c)
	}
}
