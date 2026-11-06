package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/appnice/smif/internal/store"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func TestGitDirtyCounts(t *testing.T) {
	dir := gitRepo(t)
	if n := gitDirty(dir); n == nil || *n != 0 {
		t.Fatalf("clean repo: %v", n)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n := gitDirty(dir); n == nil || *n != 2 {
		t.Fatalf("two new files: %v", n)
	}
}

// No repo, no folder and no git each give no count and no error.
func TestGitDirtyFailuresGiveNothing(t *testing.T) {
	if n := gitDirty(t.TempDir()); n != nil {
		t.Fatalf("no repo: %d", *n)
	}
	if n := gitDirty(filepath.Join(t.TempDir(), "gone")); n != nil {
		t.Fatalf("no folder: %d", *n)
	}
	dir := gitRepo(t)
	t.Setenv("PATH", t.TempDir())
	if n := gitDirty(dir); n != nil {
		t.Fatalf("no git: %d", *n)
	}
}

// slowGit puts a git on PATH that outlasts the timeout.
func slowGit(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 6\necho ' M a.go'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// A slow git holds up no caller: get returns at once with no count, and
// the timeout ends the read with no count.
func TestDirtySlowGit(t *testing.T) {
	slowGit(t)
	var c dirtyCache
	start := time.Now()
	if n := c.get("s", "stamp-1", t.TempDir(), true); n != nil {
		t.Fatalf("first get: %d", *n)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("get took %s", d)
	}
	end := time.Now().Add(gitStatusWait + 2*time.Second)
	for {
		c.mu.Lock()
		running := c.m["s"].running
		c.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the read outlived its timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := time.Since(start); d >= 5*time.Second {
		t.Fatalf("the read took %s", d)
	}
	if n := c.get("s", "stamp-1", t.TempDir(), true); n != nil {
		t.Fatalf("after the timeout: %d", *n)
	}
}

// The cache reads when the stamp changes and the session is not busy, and
// keeps the last count otherwise.
func TestDirtyReadsOnStampChange(t *testing.T) {
	dir := gitRepo(t)
	var c dirtyCache
	wait := func(want int) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			c.mu.Lock()
			e := c.m["s"]
			done := e != nil && !e.running && e.count != nil && *e.count == want
			c.mu.Unlock()
			if done {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no count %d", want)
	}
	if n := c.get("s", "1", dir, false); n != nil {
		t.Fatalf("busy: read git, %d", *n)
	}
	c.get("s", "1", dir, true)
	wait(0)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := c.get("s", "1", dir, true); n == nil || *n != 0 {
		t.Fatalf("same stamp: %v", n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := c.get("s", "2", dir, false); n == nil || *n != 0 {
		t.Fatalf("new stamp, busy: %v", n)
	}
	c.get("s", "2", dir, true)
	wait(1)
}

// The whole route on the real provider: session.list carries the facts
// from the transcript and git, context_warn sets ContextHigh, a slow git
// leaves out only Dirty. A session with no repo has no Dirty and no error.
func TestSessionFactsInList(t *testing.T) {
	d, _ := testDaemon(t)
	cfgDir := os.Getenv("CLAUDE_CONFIG_DIR")
	cwd := gitRepo(t)
	folder := filepath.Join(cfgDir, "projects", "-r-app")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	id, noRepo := "facts-session-1", "facts-session-2"
	transcript := `{"type":"user","message":{"content":"fix"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"e","name":"Edit","input":{"file_path":"/r/a.go"}}],"usage":{"input_tokens":1,"cache_read_input_tokens":250000}}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e","content":"ok"}]}}
`
	for _, s := range []string{id, noRepo} {
		if err := os.WriteFile(filepath.Join(folder, s+".jsonl"), []byte(transcript), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "a.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := d.Config()
	ti := d.answers.get(d.harnesses.Get("claude"), id, cfg.IsTestCommand)
	idle := store.Presence{SessionID: id, Cwd: cwd}
	f := d.factsView(cfg, idle, ti, "idle")
	if f == nil || f.Files != 1 || f.Tests != "none" || f.ContextTokens != 250001 || !f.ContextHigh {
		t.Fatalf("facts: %+v", f)
	}
	end := time.Now().Add(5 * time.Second)
	for f.Dirty == nil && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
		f = d.factsView(cfg, idle, ti, "idle")
	}
	if f.Dirty == nil || *f.Dirty != 1 {
		t.Fatalf("dirty: %+v", f.Dirty)
	}

	cfg.Daemon.ContextWarn = "off"
	if f := d.factsView(cfg, idle, ti, "idle"); f.ContextHigh {
		t.Fatalf("context_warn off: %+v", f)
	}
	cfg.Daemon.ContextWarn = "300k"
	if f := d.factsView(cfg, idle, ti, "idle"); f.ContextHigh {
		t.Fatalf("under context_warn: %+v", f)
	}

	ti2 := d.answers.get(d.harnesses.Get("claude"), noRepo, cfg.IsTestCommand)
	gone := store.Presence{SessionID: noRepo, Cwd: t.TempDir()}
	d.factsView(cfg, gone, ti2, "idle")
	time.Sleep(300 * time.Millisecond)
	if f := d.factsView(cfg, gone, ti2, "idle"); f == nil || f.Dirty != nil || f.Files != 1 {
		t.Fatalf("no repo: %+v", f)
	}
}
