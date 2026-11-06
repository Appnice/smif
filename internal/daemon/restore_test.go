package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

const (
	histA = "aaaaaaaa-0000-4000-8000-000000000001"
	histB = "bbbbbbbb-0000-4000-8000-000000000002"
	histC = "cccccccc-0000-4000-8000-000000000003"
)

// transcriptIn writes a session's transcript in the fixture's config folder.
func transcriptIn(t *testing.T, id, dir, entry string, text string) {
	t.Helper()
	folder := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-x")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"type": "user", "entrypoint": entry, "cwd": dir, "timestamp": time.Now().UTC().Format(time.RFC3339),
		"message": map[string]any{"content": text}})
	if err := os.WriteFile(filepath.Join(folder, id+".jsonl"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// deadPID is a pid with no process.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// openLines is the terminal log's open lines.
func openLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(l), &v) == nil && v["open"] == true {
			out = append(out, v)
		}
	}
	return out
}

// session.resume refuses an id not in the index and a missing folder, and
// argv holds only the configured claude, --resume and the checked id.
func TestSessionResumeRefusals(t *testing.T) {
	d, typed := testDaemon(t)
	d.terminal = newTerminal("log:"+typed, d.harnesses)
	dir := t.TempDir()
	transcriptIn(t, histA, dir, "cli", "hello")
	transcriptIn(t, histB, filepath.Join(dir, "gone"), "cli", "hello")
	transcriptIn(t, histC, dir, "sdk-cli", "a run")
	ctx := context.Background()
	for _, id := range []string{"", "--dangerously-skip-permissions", "../" + histA, histA + " -p", "dddddddd-0000-4000-8000-000000000004", histC} {
		if err := d.resumeSession(ctx, id, ""); !api.IsCode(err, api.CodeNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
	if err := d.resumeSession(ctx, histB, ""); !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "gone") {
		t.Errorf("missing folder: %v", err)
	}
	if got := openLines(t, typed); len(got) != 0 {
		t.Fatalf("a refused call opened a tab: %+v", got)
	}
	if err := d.resumeSession(ctx, histA, ""); err != nil {
		t.Fatal(err)
	}
	got := openLines(t, typed)
	if len(got) != 1 {
		t.Fatalf("opened: %+v", got)
	}
	if argv := fmt.Sprint(got[0]["argv"]); argv != "[claude --resume "+histA+"]" || got[0]["dir"] != dir {
		t.Errorf("argv %s in %v", argv, got[0]["dir"])
	}
}

// A live session is refused: two processes would write one transcript.
func TestSessionResumeRefusesLive(t *testing.T) {
	d, _ := testDaemon(t)
	dir := t.TempDir()
	transcriptIn(t, histA, dir, "cli", "hello")
	claudeFile(t, histA, dir, os.Getpid())
	if err := d.resumeSession(context.Background(), histA, ""); !api.IsCode(err, api.CodeRejected) || !strings.Contains(err.Error(), "open") {
		t.Errorf("live: %v", err)
	}
	rows, err := d.sessionHistory()
	if err != nil || len(rows) != 1 || !rows[0].Open {
		t.Errorf("rows %+v, %v", rows, err)
	}
}

// claudeFile writes a session file, as a running Claude Code does.
func claudeFile(t *testing.T, id, dir string, pid int) {
	t.Helper()
	folder := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "sessions")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "cwd": dir, "kind": "interactive", "status": "idle", "startedAt": now, "statusUpdatedAt": now})
	if err := os.WriteFile(filepath.Join(folder, id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// session.search stops at 50 hits and at its limit of time, and says so.
func TestSessionSearchLimits(t *testing.T) {
	d, _ := testDaemon(t)
	if searchHits != 50 || searchWithin != 10*time.Second {
		t.Fatalf("the limits are %d hits, %v", searchHits, searchWithin)
	}
	dir := t.TempDir()
	folder := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-x")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for i := range 60 {
		fmt.Fprintf(&lines, `{"type":"user","entrypoint":"cli","cwd":%q,"timestamp":"2026-11-05T10:00:00Z","message":{"content":"needle %d"}}`+"\n", dir, i)
	}
	if err := os.WriteFile(filepath.Join(folder, histA+".jsonl"), []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := d.sessionSearch(ctx, api.SessionSearchParams{Query: "needle"})
	if err != nil || len(res.Hits) != 50 || res.Stopped != "hits" {
		t.Fatalf("%d hits, stopped %q, %v", len(res.Hits), res.Stopped, err)
	}
	res, err = d.sessionSearch(ctx, api.SessionSearchParams{Query: "needle 7"})
	if err != nil || len(res.Hits) != 1 || res.Stopped != "" {
		t.Fatalf("%+v, %v", res, err)
	}
	d.restore.searchWithin = time.Nanosecond
	res, err = d.sessionSearch(ctx, api.SessionSearchParams{Query: "needle"})
	if err != nil || len(res.Hits) >= 50 || res.Stopped != "time" {
		t.Fatalf("%d hits, stopped %q, %v", len(res.Hits), res.Stopped, err)
	}
	for _, q := range []string{"", "   ", strings.Repeat("x", maxQueryLen+1)} {
		if _, err := d.sessionSearch(ctx, api.SessionSearchParams{Query: q}); !api.IsCode(err, api.CodeParams) {
			t.Errorf("%q: %v", q, err)
		}
	}
}

func closedIDs(d *Daemon) string {
	var ids []string
	for _, c := range d.closedList().Sessions {
		ids = append(ids, c.SessionID[:1])
	}
	return strings.Join(ids, "")
}

// Two sessions that vanish within 10 s join the list. One alone does not,
// and neither do two sessions further apart, or a session whose process
// still runs (/clear gave it a new id).
func TestVanishRules(t *testing.T) {
	d, _ := testDaemon(t)
	dir := t.TempDir()
	for _, id := range []string{histA, histB, histC} {
		transcriptIn(t, id, dir, "cli", "hello")
	}
	dead := deadPID(t)
	a := openSession{SessionID: histA, PID: dead, Dir: dir, Busy: true}
	b := openSession{SessionID: histB, PID: dead, Dir: dir}
	c := openSession{SessionID: histC, PID: dead, Dir: dir}
	t0 := time.Now()

	d.trackOpen([]openSession{a, b, c}, t0)
	d.trackOpen([]openSession{b, c}, t0.Add(2*time.Second)) // a closes alone
	if got := closedIDs(d); got != "" {
		t.Fatalf("one alone: %q", got)
	}
	d.trackOpen([]openSession{c}, t0.Add(14*time.Second)) // b, 12 s later
	if got := closedIDs(d); got != "" {
		t.Fatalf("12 s apart: %q", got)
	}
	d.trackOpen([]openSession{a, b, c}, t0.Add(30*time.Second))
	d.trackOpen([]openSession{c}, t0.Add(32*time.Second)) // a and b at once
	if got := closedIDs(d); got != "ab" && got != "ba" {
		t.Fatalf("two at once: %q", got)
	}
	list := d.closedList().Sessions
	for _, s := range list {
		if s.DiedWorking != (s.SessionID == histA) {
			t.Errorf("died working: %+v", s)
		}
	}
	d.trackOpen(nil, t0.Add(40*time.Second)) // c, 8 s later, in the same crash
	if got := closedIDs(d); len(got) != 3 {
		t.Fatalf("a third within 10 s: %q", got)
	}
	d.forgetClosed(histC) // reopened within the window: it stays off the list
	d.trackOpen(nil, t0.Add(41*time.Second))
	if got := closedIDs(d); strings.Contains(got, "c") {
		t.Fatalf("a reopened session came back: %q", got)
	}
	d.trackOpen([]openSession{a}, t0.Add(42*time.Second)) // a came back
	if got := closedIDs(d); strings.Contains(got, "a") {
		t.Fatalf("a live session stays listed: %q", got)
	}
	d.trackOpen(nil, t0.Add(42*time.Second+closedKept))
	if got := closedIDs(d); strings.Contains(got, "b") {
		t.Fatalf("an old entry stays listed: %q", got)
	}

	// A process that runs on is no vanish.
	e, _ := testDaemon(t)
	for _, id := range []string{histA, histB} {
		transcriptIn(t, id, dir, "cli", "hello")
	}
	me := os.Getpid()
	e.trackOpen([]openSession{{SessionID: histA, PID: me}, {SessionID: histB, PID: me}}, t0)
	e.trackOpen(nil, t0.Add(2*time.Second))
	if got := closedIDs(e); got != "" {
		t.Fatalf("/clear: %q", got)
	}
}

// open-sessions.json follows the live sessions. At a start, a session in it
// whose process is gone joins the list, a live one does not, and the list
// survives a second restart.
func TestRestartList(t *testing.T) {
	d, _ := testDaemon(t)
	dir := t.TempDir()
	transcriptIn(t, histA, dir, "cli", "hello")
	transcriptIn(t, histB, dir, "cli", "hello")
	d.trackOpen([]openSession{{SessionID: histA, PID: deadPID(t), Dir: dir, Name: "shop", Busy: true}, {SessionID: histB, PID: os.Getpid(), Dir: dir}}, time.Now())
	path := filepath.Join(d.dataDir, openFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	d.restore = restoreState{}
	d.loadOpen(time.Now())
	list := d.closedList().Sessions
	if len(list) != 1 || list[0].SessionID != histA || !list[0].DiedWorking || list[0].Name != "shop" {
		t.Fatalf("list: %+v", list)
	}
	d.restore = restoreState{}
	d.loadOpen(time.Now())
	if got := closedIDs(d); got != "a" {
		t.Fatalf("after a second restart: %q", got)
	}
	// A bad file is no list and no failure.
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.restore = restoreState{}
	d.loadOpen(time.Now())
	if got := closedIDs(d); got != "" {
		t.Fatalf("bad file: %q", got)
	}
}

// reopen opens at most 20, 500 ms apart. A session that cannot come back
// leaves the list, and the rest wait for the next call.
func TestReopenLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 10 s")
	}
	d, typed := testDaemon(t)
	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()
	dir := t.TempDir()
	dead := deadPID(t)
	var open []openSession
	now := time.Now()
	for i := range 23 {
		id := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", i)
		transcriptIn(t, id, dir, "cli", "hello")
		open = append(open, openSession{SessionID: id, PID: dead, Dir: dir})
	}
	d.trackOpen(open, now)
	d.trackOpen(nil, now.Add(time.Second))
	if n := len(d.closedList().Sessions); n != 23 {
		t.Fatalf("list of %d", n)
	}
	res, err := d.sessionReopen(api.SessionReopenParams{})
	if err != nil || res.Opening != 20 || res.Left != 3 {
		t.Fatalf("%+v, %v", res, err)
	}
	if _, err := d.sessionReopen(api.SessionReopenParams{}); !api.IsCode(err, api.CodeRejected) {
		t.Errorf("a second run: %v", err)
	}
	if _, err := d.sessionReopen(api.SessionReopenParams{Term: "xterm"}); !api.IsCode(err, api.CodeParams) {
		t.Errorf("a bad terminal: %v", err)
	}
	start := time.Now()
	d.wg.Wait()
	if took := time.Since(start); took < 19*reopenGap {
		t.Errorf("20 opens took %v", took)
	}
	if got := openLines(t, typed); len(got) != 20 {
		t.Errorf("opened %d", len(got))
	}
	if n := len(d.closedList().Sessions); n != 3 {
		t.Errorf("left %d", n)
	}
}
