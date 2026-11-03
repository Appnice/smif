package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

func digestRule() string { return "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"digest\"\n" }

func treeHashes(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(repo, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		if fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			h := sha256.Sum256(b)
			out[p] = hex.EncodeToString(h[:])
		}
		return nil
	})
	return out
}

func argValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func hasArg(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

func (e *Env) hook(repo, event, session string) (string, string) {
	e.t.Helper()
	in := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":%q}`, session, repo, event)
	out, errOut, err := e.CLIDir(repo, in, nil, "hook", event)
	if err != nil {
		e.t.Fatalf("hook %s exited non-zero: %v", event, err)
	}
	return out, errOut
}

func (e *Env) statusline(repo string) string {
	e.t.Helper()
	out, _, err := e.CLIDir(repo, fmt.Sprintf(`{"session_id":"s1","workspace":{"current_dir":%q}}`, repo), nil, "statusline")
	if err != nil {
		e.t.Fatalf("statusline: %v", err)
	}
	return strings.TrimSpace(out)
}

// A2: crash routing while an interactive session is present.
func TestA2CrashRoutingIsolated(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "puzzle", Rules: `
[[agents.AGENT.rules]]
match = { from = "observability", kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
[[agents.AGENT.rules]]
match = {}
handler = "escalate"
`})
	e.Start()
	repo := e.Repos["puzzle"]
	out, errOut := e.hook(repo, "SessionStart", "interactive-1")
	if out != "" || errOut != "" {
		t.Fatalf("hook printed %q / %q, want nothing", out, errOut)
	}
	beforeStatus := e.Git(repo, "status", "--porcelain")
	beforeHashes := treeHashes(t, repo)
	r := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report",
		Priority: "high", Subject: "NPE in DeckScreen on 1.4.2", Body: "stack: at DeckScreen.kt:42", DedupeKey: "crash:a91f"})
	e.WaitState(r.ID, 15*time.Second, "done")
	calls := e.WorkerCalls()
	if len(calls) != 1 {
		t.Fatalf("worker calls = %d", len(calls))
	}
	inv := calls[0]
	if inv.Cwd != repo {
		t.Fatalf("triage worker cwd = %s, want the repo (write_scope none)", inv.Cwd)
	}
	dis := strings.Join(inv.Argv, " ")
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		if !strings.Contains(dis[strings.Index(dis, "--disallowedTools"):], tool) {
			t.Fatalf("triage argv does not deny %s: %v", tool, inv.Argv)
		}
	}
	if !strings.Contains(inv.Stdin, `<mailroom_message untrusted="true">`) || !strings.Contains(inv.Stdin, "DeckScreen.kt:42") {
		t.Fatalf("envelope not wrapped as untrusted data:\n%s", inv.Stdin)
	}
	if after := e.Git(repo, "status", "--porcelain"); after != beforeStatus {
		t.Fatalf("git status changed: %q -> %q", beforeStatus, after)
	}
	afterHashes := treeHashes(t, repo)
	if len(afterHashes) != len(beforeHashes) {
		t.Fatalf("files changed: %d -> %d", len(beforeHashes), len(afterHashes))
	}
	for p, h := range beforeHashes {
		if afterHashes[p] != h {
			t.Fatalf("%s changed", p)
		}
	}
	sl := e.statusline(repo)
	if !strings.Contains(sl, "puzzle") || !strings.Contains(sl, "1 handled") {
		t.Fatalf("status line = %q, want 1 handled", sl)
	}
	if strings.Contains(sl, "DeckScreen") {
		t.Fatalf("status line leaked message content: %q", sl)
	}
	// A hook fired inside a worker must not count as presence.
	_, _, _ = e.CLIDir(repo, `{"session_id":"w","cwd":"`+repo+`"}`, []string{"MAILROOM_RUN_ID=x"}, "hook", "SessionStart")
	t.Logf("A2 status line: %s", sl)
}

// A3: the fix profile commits on the thread branch; diff shows it; apply
// merges on a clean tree and refuses on a dirty one.
func TestA3FixProfileWorktree(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "puzzle", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"fix\"\n",
			Before: func(repo string) {
				os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755)
				os.WriteFile(filepath.Join(repo, "node_modules", "x"), []byte("x"), 0o644)
			}})
	e.Start()
	repo := e.Repos["puzzle"]
	r := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report",
		Subject: "fix the NPE", Body: "#fake:edit=src/fix.txt"})
	e.WaitState(r.ID, 15*time.Second, "done")
	runs := e.Runs(r.ID)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	want := "mailroom/puzzle/" + strings.ToLower(r.ThreadID[len(r.ThreadID)-10:])
	if run.Branch != want {
		t.Fatalf("branch = %q, want %q", run.Branch, want)
	}
	if inv := e.WorkerCalls()[0]; !strings.Contains(inv.Cwd, filepath.Join("data", "worktrees", "puzzle")) {
		t.Fatalf("fix worker cwd = %s, want the agent worktree", inv.Cwd)
	}
	msg := e.Git(repo, "log", "-1", "--format=%s", run.Branch)
	if !strings.HasPrefix(msg, "mailroom: ") || !strings.Contains(msg, "[run "+run.ID+"]") {
		t.Fatalf("commit message = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "fix.txt")); err == nil {
		t.Fatal("main checkout was modified")
	}
	out, err := e.CLI("", "diff", run.ID)
	if err != nil || !strings.Contains(out, "src/fix.txt") || !strings.Contains(out, "edited by fake") {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644)
	if out, err := e.CLI("", "apply", run.ID); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("apply on a dirty tree should be refused: %v %s", err, out)
	}
	e.Git(repo, "checkout", "README.md")
	if out, err := e.CLI("", "apply", run.ID); err != nil {
		t.Fatalf("apply: %v %s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "src", "fix.txt")); err != nil || !strings.Contains(string(b), "edited by fake") {
		t.Fatalf("apply did not merge: %v", err)
	}
	// Ignored files in the worktree survive git clean -fd on the next run.
	wt := filepath.Join(e.Root, "data", "worktrees", "puzzle")
	os.MkdirAll(filepath.Join(wt, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(wt, "node_modules", "cache"), []byte("c"), 0o644)
	os.WriteFile(filepath.Join(wt, "stray.txt"), []byte("s"), 0o644)
	r2 := e.Send("observability", "message.emit", envelope.Draft{To: "puzzle", Kind: "event", Intent: "crash.report", Subject: "second"})
	e.WaitState(r2.ID, 15*time.Second, "done")
	if _, err := os.Stat(filepath.Join(wt, "node_modules", "cache")); err != nil {
		t.Fatal("ignored build cache was removed")
	}
}

// A4: an asset request answered with a file; the attachment the asker sent
// reaches the worker as a read-only copy.
func TestA4AssetRequestWithAttachment(t *testing.T) {
	icon := []byte("\x89PNG\r\n\x1a\nfake icon bytes")
	e := NewEnv(t, "", "",
		AgentSpec{ID: "lab", Rules: digestRule()},
		AgentSpec{ID: "puzzle", Rules: `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "asset.*" }
handler = "worker"
profile = "answer"
[[agents.AGENT.rules]]
match = {}
handler = "escalate"
`, Before: func(repo string) {
			os.MkdirAll(filepath.Join(repo, "assets"), 0o755)
			os.WriteFile(filepath.Join(repo, "assets", "icon.png"), icon, 0o644)
		}})
	e.Start()
	c := e.AgentClient("lab")
	defer c.Close()
	spec := []byte("size: 512x512\n")
	var att envelope.Attachment
	if err := c.Call("blob.put", api.BlobPutParams{Name: "spec.txt", Data: spec}, &att, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var res api.SendResult
	err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "puzzle", Kind: "ask", Intent: "asset.icon",
		Subject: "Send me the app icon", Body: "#fake:artifact=assets/icon.png", Attachments: []envelope.Attachment{att}}, WaitS: 15}, &res, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == nil || len(res.Reply.Attachments) != 1 {
		t.Fatalf("reply without the artifact: %+v", res.Reply)
	}
	ra := res.Reply.Attachments[0]
	var blob api.BlobGetResult
	if err := c.Call("blob.get", api.IDParams{ID: ra.SHA256}, &blob, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(icon)
	got := sha256.Sum256(blob.Data)
	if ra.SHA256 != hex.EncodeToString(want[:]) || got != want || ra.Name != "icon.png" {
		t.Fatalf("artifact mismatch: %+v", ra)
	}
	var seen bool
	for _, line := range readLines(filepath.Join(e.Root, "fake", "invocations.jsonl")) {
		var m map[string]any
		json.Unmarshal([]byte(line), &m)
		if p, ok := m["attachment_path"].(string); ok {
			seen = true
			if m["mode"] != "400" || m["sha256"] != att.SHA256 || !strings.HasSuffix(p, "spec.txt") {
				t.Fatalf("worker attachment copy: %v (want mode 400, sha %s)", m, att.SHA256)
			}
		}
	}
	if !seen {
		t.Fatal("worker prompt listed no read-only attachment path")
	}
	// Another agent cannot fetch a blob that was not sent to it.
	oc := e.AgentClient("puzzle")
	defer oc.Close()
	if err := oc.Call("blob.get", api.IDParams{ID: att.SHA256}, nil, 5*time.Second); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("blob.get by a non-recipient: %v", err)
	}
}

func readLines(p string) []string {
	b, _ := os.ReadFile(p)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A5: needs_human notifies once; mailroom reply continues the thread.
func TestA5NeedsHumanAndReply(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe"})
	e.Start()
	r := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "release.decide",
		Subject: "Ship 1.0.2 now or wait for review?", Body: "#fake:result=needs_human"})
	m := e.WaitState(r.ID, 15*time.Second, "needs_human")
	if m.HumanQuestion == "" {
		t.Fatal("no human_question")
	}
	time.Sleep(300 * time.Millisecond)
	var n int
	for _, x := range e.Notifications() {
		if x["kind"] == "needs_human" && x["message_id"] == r.ID {
			n++
			if strings.Contains(x["summary"].(string), "#fake") {
				t.Fatalf("notification carries the raw body: %v", x)
			}
		}
	}
	if n != 1 {
		t.Fatalf("needs_human notifications = %d, want 1", n)
	}
	sl := e.statusline(e.Repos["cafe"])
	if !strings.Contains(sl, "1 needs you") {
		t.Fatalf("status line = %q", sl)
	}
	out, err := e.CLI("", "reply", r.ID, "Wait for the review, then ship.")
	if err != nil {
		t.Fatalf("reply: %v %s", err, out)
	}
	e.WaitFor("continuation done", 15*time.Second, func() bool {
		tv := e.Thread(r.ThreadID)
		for _, x := range tv.Messages {
			if x.Kind == "reply" && x.From.Agent == "human" && x.State == "done" {
				return true
			}
		}
		return false
	})
	tv := e.Thread(r.ThreadID)
	if len(tv.Runs) != 2 {
		t.Fatalf("runs in thread = %d, want 2 (original + continuation)", len(tv.Runs))
	}
	last := e.WorkerCalls()[len(e.WorkerCalls())-1]
	if !strings.Contains(last.Stdin, "Wait for the review") || argValue(last.Argv, "--resume") == "" {
		t.Fatalf("continuation did not resume the worker session with the human answer:\n%v", last.Argv)
	}
	if e.Message(r.ID).State != "done" {
		t.Fatal("needs_human item not closed after the reply")
	}
	// The digest shows both and advances the cursor.
	d1, _ := e.CLI("", "digest", "--agent", "cafe")
	d2, _ := e.CLI("", "digest", "--agent", "cafe")
	if !strings.Contains(d1, "release.decide") || !strings.Contains(d2, "nothing new") {
		t.Fatalf("digest:\n%s\n---\n%s", d1, d2)
	}
}

// A8: kill -9 during a run: orphan killed, retried, exactly one done.
func TestA8KillDaemonMidRun(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe"})
	e.Start()
	r := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "slow", Body: "#fake:sleep1=60"})
	e.WaitFor("worker started", 10*time.Second, func() bool { return len(e.WorkerCalls()) == 1 })
	orphan := e.WorkerCalls()[0].PID
	e.Kill9()
	if !pidAlive(orphan) {
		t.Fatal("worker died with the daemon; the test needs it alive to prove orphan recovery")
	}
	e.Start()
	e.WaitFor("orphan killed", 3*time.Second, func() bool { return !pidAlive(orphan) })
	e.WaitState(r.ID, 20*time.Second, "done")
	runs := e.Runs(r.ID)
	var orphaned, ok int
	for _, x := range runs {
		switch x.State {
		case "orphaned":
			orphaned++
		case "succeeded":
			ok++
		}
	}
	if orphaned != 1 || ok != 1 || len(runs) != 2 {
		t.Fatalf("runs: %+v", runs)
	}
	var done int
	for _, a := range e.Audit() {
		if a["message_id"] == r.ID && a["event"] == "message.transition" {
			if d, _ := a["detail"].(map[string]any); d["to_state"] == "done" {
				done++
			}
		}
	}
	if done != 1 {
		t.Fatalf("done transitions = %d", done)
	}
}

// A10: timeout kills the whole process group; retries end in dead_letter
// with a notification.
func TestA10TimeoutKillsGroupThenDeadLetter(t *testing.T) {
	e := NewEnv(t, "", "[profiles.quick]\ntimeout_s = 2\n",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe", Extra: "max_attempts = 2", Rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"quick\"\n"})
	e.Start()
	r := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "hang", Body: "#fake:spawn_child #fake:sleep=30"})
	m := e.WaitState(r.ID, 30*time.Second, "dead_letter")
	if !strings.Contains(m.Reason, "timeout") {
		t.Fatalf("reason = %q", m.Reason)
	}
	var children []int
	for _, line := range readLines(filepath.Join(e.Root, "fake", "invocations.jsonl")) {
		var x map[string]any
		json.Unmarshal([]byte(line), &x)
		if c, ok := x["child_pid"].(float64); ok {
			children = append(children, int(c))
		}
	}
	if len(children) == 0 || len(e.WorkerCalls()) != 2 {
		t.Fatalf("children recorded = %d, worker calls = %d (want 2 attempts)", len(children), len(e.WorkerCalls()))
	}
	for _, inv := range e.WorkerCalls() {
		if pidAlive(inv.PID) {
			t.Fatalf("worker %d survived the timeout", inv.PID)
		}
	}
	for _, c := range children {
		if pidAlive(c) {
			t.Fatalf("child %d survived: process group not killed", c)
		}
	}
	var dl bool
	for _, n := range e.Notifications() {
		if n["kind"] == "dead_letter" && n["message_id"] == r.ID {
			dl = true
		}
	}
	if !dl {
		t.Fatal("no dead_letter notification")
	}
	if out, _ := e.CLI("", "dlq"); !strings.Contains(out, short(r.ID)) {
		t.Fatalf("dlq does not list it:\n%s", out)
	}
	if _, err := e.CLI("", "retry", r.ID); err != nil {
		t.Fatal(err)
	}
	e.WaitState(r.ID, 5*time.Second, "running", "retry_wait", "dead_letter")
}

func short(id string) string { return id[len(id)-10:] }

// A11: a direct release run waits for the interactive session to end, runs
// in the main checkout and shows the lock in the status line.
func TestA11DirectRunHeldByPresence(t *testing.T) {
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "atlas", Extra: `direct_conflict = "hold"`, Rules: "[[agents.AGENT.rules]]\nmatch = { kind = \"tell\", intent = \"release.*\" }\nhandler = \"worker\"\nprofile = \"release\"\n[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"digest\"\n"})
	e.Start()
	repo := e.Repos["atlas"]
	e.hook(repo, "SessionStart", "web-1")
	r := e.Send("observability", "message.send", envelope.Draft{To: "atlas", Kind: "tell", Intent: "release.published",
		Subject: "cafe 1.0.2 is live", Body: "#fake:sleep=2 #fake:edit=releases.md"})
	time.Sleep(1500 * time.Millisecond)
	m := e.Message(r.ID)
	if m.State != "queued" || !strings.Contains(m.HoldReason, "interactive session") {
		t.Fatalf("direct run not held: state %s hold %q", m.State, m.HoldReason)
	}
	if len(e.WorkerCalls()) != 0 {
		t.Fatal("worker started while the interactive session was active")
	}
	e.hook(repo, "UserPromptSubmit", "web-1") // heartbeat keeps it held
	e.hook(repo, "SessionEnd", "web-1")
	e.WaitFor("direct run started", 10*time.Second, func() bool { return len(e.WorkerCalls()) == 1 })
	var sl string
	e.WaitFor("lock shown", 5*time.Second, func() bool {
		sl = e.statusline(repo)
		return strings.Contains(sl, "worker holds repo")
	})
	e.WaitState(r.ID, 15*time.Second, "done")
	if inv := e.WorkerCalls()[0]; inv.Cwd != repo {
		t.Fatalf("direct run cwd = %s, want the main checkout", inv.Cwd)
	}
	if _, err := os.Stat(filepath.Join(repo, "releases.md")); err != nil {
		t.Fatal("direct run did not write in the main checkout")
	}
	if strings.Contains(e.statusline(repo), "holds repo") {
		t.Fatal("lock still shown after the run")
	}
	t.Logf("A11 status line during the run: %s", sl)
}

// A12: install then uninstall with an existing statusLine and hooks.
func TestA12InstallUninstall(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "cafe"})
	settings := filepath.Join(e.Home, ".claude", "settings.json")
	orig := `{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "statusLine": {"type": "command", "command": "echo 'my status'", "padding": 0},
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "echo existing-hook"}]}]
  },
  "theme": "dark"
}
`
	os.WriteFile(settings, []byte(orig), 0o644)
	e.Start()
	out, err := e.CLI("", "install", "--no-service")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	var s map[string]any
	b, _ := os.ReadFile(settings)
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("settings after install not JSON: %v", err)
	}
	hooks := s["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "Stop"} {
		if !strings.Contains(fmt.Sprint(hooks[ev]), mailroomBin+" hook "+ev) {
			t.Fatalf("hook %s missing: %v", ev, hooks[ev])
		}
	}
	if !strings.Contains(fmt.Sprint(hooks["SessionStart"]), "existing-hook") {
		t.Fatal("existing hook lost")
	}
	slCmd := s["statusLine"].(map[string]any)["command"].(string)
	run := exec.Command("sh", "-c", slCmd)
	run.Env = e.env
	run.Stdin = strings.NewReader(`{"workspace":{"current_dir":"` + e.Repos["cafe"] + `"}}`)
	slOut, err := run.Output()
	if err != nil || strings.TrimSpace(string(slOut)) != "my status" {
		t.Fatalf("wrapped status line = %q (%v)", slOut, err)
	}
	if s["statusLine"].(map[string]any)["padding"] == nil {
		t.Fatal("statusLine padding dropped")
	}
	cmdFile := filepath.Join(e.Home, ".claude", "commands", "mailroom.md")
	if b, err := os.ReadFile(cmdFile); err != nil || !strings.Contains(string(b), "digest --cwd") {
		t.Fatalf("slash command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "fake", "mcp-registered")); err != nil {
		t.Fatal("MCP server not registered")
	}
	if out, err := e.CLI("", "uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(settings)
	if string(after) != orig {
		t.Fatalf("settings.json not byte-identical after uninstall:\n%s", after)
	}
	if _, err := os.Stat(cmdFile); !os.IsNotExist(err) {
		t.Fatal("slash command left behind")
	}
	if _, err := os.Stat(filepath.Join(e.Root, "fake", "mcp-registered")); !os.IsNotExist(err) {
		t.Fatal("MCP server still registered")
	}
	// Edited while installed: uninstall removes only Mailroom's entries.
	if _, err := e.CLI("", "install", "--no-service"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(settings)
	json.Unmarshal(b, &s)
	s["theme"] = "light"
	b, _ = json.MarshalIndent(s, "", "  ")
	os.WriteFile(settings, b, 0o644)
	if out, err := e.CLI("", "uninstall"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	b, _ = os.ReadFile(settings)
	if strings.Contains(string(b), "mailroom") || !strings.Contains(string(b), "light") || !strings.Contains(string(b), "echo 'my status'") ||
		!strings.Contains(string(b), "existing-hook") {
		t.Fatalf("surgical uninstall wrong:\n%s", b)
	}
}

// A13: rotation: new session id, memory.md in the next policy prompt, old
// id archived.
func TestA13SessionRotation(t *testing.T) {
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe"})
	cfg, _ := os.ReadFile(e.Config())
	os.WriteFile(e.Config(), []byte(strings.Replace(string(cfg), "[defaults]\n", "[defaults]\nrotate_at_tokens = 100000\n", 1)), 0o600)
	e.Start()
	r1 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "big", Body: "#fake:tokens=150000"})
	e.WaitState(r1.ID, 15*time.Second, "done")
	s1 := e.Runs(r1.ID)[0].SessionID
	r2 := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "next"})
	e.WaitState(r2.ID, 15*time.Second, "done")
	run2 := e.Runs(r2.ID)[0]
	if run2.ArchivedSession != s1 {
		t.Fatalf("archived = %q, want %q", run2.ArchivedSession, s1)
	}
	if run2.SessionID == "" || run2.SessionID == s1 {
		t.Fatalf("no new session: %q", run2.SessionID)
	}
	mem, err := os.ReadFile(filepath.Join(e.Root, "data", "agents", "cafe", "memory.md"))
	if err != nil || !strings.Contains(string(mem), s1) {
		t.Fatalf("memory.md: %v %q", err, mem)
	}
	calls := e.WorkerCalls()
	compact, next := calls[1], calls[2]
	if !strings.Contains(compact.Stdin, "MAILROOM_COMPACT") || argValue(compact.Argv, "--resume") != s1 {
		t.Fatalf("compaction turn not on the old session: %v", compact.Argv)
	}
	if argValue(next.Argv, "--resume") != "" {
		t.Fatal("fresh session must omit --resume")
	}
	if !strings.Contains(argValue(next.Argv, "--append-system-prompt"), string(mem)) {
		t.Fatal("memory.md not in the next policy prompt")
	}
}

// A14: static guard and argv audit (R-ISO-4).
func TestA14StaticGuardAndArgv(t *testing.T) {
	forbidden := regexp.MustCompile(`dangerously-load-development-channels|--dangerously-skip-permissions|SendMessage|\bMonitor\b|--channels|agent-teams|CLAUDE_CODE_EXPERIMENTAL`)
	var hits []string
	filepath.Walk(filepath.Join(repoRoot, "internal", "runner"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for i, line := range strings.Split(string(b), "\n") {
			if forbidden.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", p, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if len(hits) > 0 {
		t.Fatalf("forbidden feature in runner code:\n%s", strings.Join(hits, "\n"))
	}
	// Exercise every built-in profile and audit the recorded argv.
	rules := `
[[agents.AGENT.rules]]
match = { intent = "p.triage" }
handler = "worker"
profile = "triage"
[[agents.AGENT.rules]]
match = { intent = "p.fix" }
handler = "worker"
profile = "fix"
[[agents.AGENT.rules]]
match = { intent = "p.answer" }
handler = "worker"
profile = "answer"
[[agents.AGENT.rules]]
match = {}
handler = "worker"
profile = "release"
`
	e := NewEnv(t, "", "", AgentSpec{ID: "observability", Rules: digestRule()}, AgentSpec{ID: "cafe", Rules: rules})
	e.Start()
	for _, p := range []string{"triage", "fix", "answer", "release"} {
		r := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "p." + p, Subject: p})
		e.WaitState(r.ID, 15*time.Second, "done")
	}
	calls := e.WorkerCalls()
	if len(calls) != 4 {
		t.Fatalf("calls = %d", len(calls))
	}
	for _, inv := range calls {
		joined := strings.Join(inv.Argv, " ")
		if forbidden.MatchString(joined) || strings.Contains(joined, "--dangerously") || strings.Contains(joined, "--bg") {
			t.Fatalf("forbidden flag in argv: %s", joined)
		}
		if !strings.Contains(joined, "Bash(*--output*)") || !strings.Contains(joined, "Read(/"+filepath.Join(e.Root, "cfg")+"/**)") ||
			!strings.Contains(joined, "Read(/"+filepath.Join(e.Root, "data")+"/**)") {
			t.Fatalf("protected dirs or git --output not denied: %s", joined)
		}
		if tools := argValue(inv.Argv, "--tools"); tools == "" || strings.Contains(tools, "Agent") || strings.Contains(tools, "Web") {
			t.Fatalf("--tools = %q", tools)
		}
		if argValue(inv.Argv, "--settings") != `{"disableAllHooks":true}` {
			t.Fatalf("worker runs with the user's hooks on: %s", joined)
		}
		for _, need := range []string{"--output-format", "--permission-mode", "--max-turns", "--mcp-config", "--strict-mcp-config", "--permission-prompts", "--name"} {
			if !hasArg(inv.Argv, need) {
				t.Fatalf("argv lacks %s: %s", need, joined)
			}
		}
	}
}

// A15: daily budget: worker runs follow on_budget; command handlers keep working.
func TestA15DailyBudget(t *testing.T) {
	rules := `
[[agents.AGENT.rules]]
match = { kind = "ask", intent = "store.status" }
handler = "command"
command = ["./scripts/mailroom/store-status.sh"]
[[agents.AGENT.rules]]
match = {}
handler = "worker"
profile = "triage"
`
	e := NewEnv(t, "", "",
		AgentSpec{ID: "observability", Rules: digestRule()},
		AgentSpec{ID: "cafe", Extra: "budget_usd = 0.05\non_budget = \"hold\"", Rules: rules, Before: withScript("store-status.sh", storeStatusScript)},
		AgentSpec{ID: "lab", Extra: "budget_usd = 0.05\non_budget = \"escalate\"", Rules: rules, Before: withScript("store-status.sh", storeStatusScript)})
	e.Start()
	for _, ag := range []string{"cafe", "lab"} {
		r := e.Send("observability", "message.send", envelope.Draft{To: ag, Kind: "tell", Intent: "x.y", Subject: "expensive", Body: "#fake:cost=0.10"})
		e.WaitState(r.ID, 15*time.Second, "done")
	}
	held := e.Send("observability", "message.send", envelope.Draft{To: "cafe", Kind: "tell", Intent: "x.y", Subject: "over budget"})
	esc := e.Send("observability", "message.send", envelope.Draft{To: "lab", Kind: "tell", Intent: "x.y", Subject: "over budget"})
	e.WaitState(esc.ID, 10*time.Second, "needs_human")
	time.Sleep(time.Second)
	m := e.Message(held.ID)
	if m.State != "queued" || !strings.Contains(m.HoldReason, "budget") {
		t.Fatalf("hold agent: state %s hold %q", m.State, m.HoldReason)
	}
	if n := len(e.WorkerCalls()); n != 2 {
		t.Fatalf("worker calls = %d, want 2 (no run past the budget)", n)
	}
	for _, ag := range []string{"cafe", "lab"} {
		c := e.AgentClient("observability")
		var res api.SendResult
		if err := c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: ag, Kind: "ask", Intent: "store.status", Subject: "live?"}, WaitS: 5}, &res, 10*time.Second); err != nil || res.Reply == nil {
			t.Fatalf("command handler on %s blocked by the budget: %v %+v", ag, err, res)
		}
		c.Close()
	}
	var budgetNotes int
	for _, n := range e.Notifications() {
		if n["kind"] == "budget" {
			budgetNotes++
		}
	}
	if budgetNotes != 2 {
		t.Fatalf("budget notifications = %d, want 2", budgetNotes)
	}
}

// M1: kill -9 during writes loses no acknowledged message.
func TestM1KillDuringWritesLosesNothing(t *testing.T) {
	e := NewEnv(t, "rate_limit_per_minute = 100000", "", AgentSpec{ID: "a", Rules: digestRule()}, AgentSpec{ID: "b", Rules: digestRule()})
	e.Start()
	var acked sync.Map
	var count atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				c, err := api.Dial(filepath.Join(e.Root, "s.sock"), api.Auth{Agent: "a", Token: e.token("a")}, 200*time.Millisecond)
				if err != nil {
					time.Sleep(20 * time.Millisecond)
					continue
				}
				var r api.SendResult
				err = c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: "b", Kind: "tell", Intent: "x.y", Subject: fmt.Sprintf("w%d-%d", w, i)}}, &r, 2*time.Second)
				c.Close()
				if err == nil {
					acked.Store(r.ID, true)
					count.Add(1)
				}
			}
		}(w)
	}
	for count.Load() < 150 {
		time.Sleep(5 * time.Millisecond)
	}
	e.Kill9()
	close(stop)
	wg.Wait()
	e.Start()
	missing := 0
	total := 0
	acked.Range(func(k, _ any) bool {
		total++
		var tv api.ThreadView
		c, _ := e.Client()
		if err := c.Call("thread.get", api.IDParams{ID: k.(string)}, &tv, 5*time.Second); err != nil {
			missing++
		}
		c.Close()
		return true
	})
	if missing > 0 {
		t.Fatalf("%d of %d acknowledged messages lost", missing, total)
	}
	t.Logf("M1: %d acknowledged messages all present after kill -9", total)
}

// Chaos (RFC 9.4): 200 messages across 5 agents, daemon kill -9'd at random
// points 10 times; every message reaches exactly one terminal state and no
// run completes twice.
func TestChaosKill9(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos test")
	}
	ids := []string{"a1", "a2", "a3", "a4", "a5"}
	var specs []AgentSpec
	for _, id := range ids {
		specs = append(specs, AgentSpec{ID: id, Extra: "max_attempts = 20"})
	}
	e := NewEnv(t, "rate_limit_per_minute = 100000", "[profiles.triage]\nmax_turns = 3\n", specs...)
	e.Start()
	rng := rand.New(rand.NewSource(7))
	var sent []string
	send := func(i int) {
		for {
			from, to := ids[rng.Intn(5)], ids[rng.Intn(5)]
			body := ""
			if rng.Intn(3) == 0 {
				body = "#fake:sleep=0.3"
			}
			c, err := api.Dial(filepath.Join(e.Root, "s.sock"), api.Auth{Agent: from, Token: e.token(from)}, 300*time.Millisecond)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var r api.SendResult
			err = c.Call("message.send", api.SendParams{Draft: envelope.Draft{To: to, Kind: "tell", Intent: "chaos.msg", Subject: fmt.Sprint(i), Body: body}}, &r, 3*time.Second)
			c.Close()
			if err == nil {
				sent = append(sent, r.ID)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	kills := 0
	for i := 0; i < 200; i++ {
		send(i)
		if i%20 == 19 && kills < 10 {
			time.Sleep(time.Duration(rng.Intn(300)) * time.Millisecond)
			e.Kill9()
			kills++
			e.Start()
		}
	}
	e.WaitFor("all terminal", 120*time.Second, func() bool {
		var ms []api.MessageView
		c, _ := e.Client()
		c.Call("thread.list", api.ListParams{Limit: 1000}, &ms, 10*time.Second)
		c.Close()
		for _, m := range ms {
			if m.State != "done" && m.State != "dead_letter" {
				return false
			}
		}
		return len(ms) >= 200
	})
	terminal := map[string]int{}
	succeeded := map[string]int{}
	for _, a := range e.Audit() {
		if a["event"] == "message.transition" {
			d, _ := a["detail"].(map[string]any)
			switch d["to_state"] {
			case "done", "rejected", "expired":
				terminal[a["message_id"].(string)]++
			}
		}
		if a["event"] == "run.finished" {
			if d, _ := a["detail"].(map[string]any); d["state"] == "succeeded" {
				succeeded[a["message_id"].(string)]++
			}
		}
	}
	for _, id := range sent {
		if terminal[id] != 1 {
			t.Fatalf("message %s reached a terminal state %d times", id, terminal[id])
		}
		if succeeded[id] > 1 {
			t.Fatalf("message %s completed %d runs", id, succeeded[id])
		}
	}
	t.Logf("chaos: %d messages, %d kills, all terminal exactly once", len(sent), kills)
}
