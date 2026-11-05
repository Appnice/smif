package opencode

import (
	"crypto/sha256"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

// The rows in testdata are built from the research report (OpenCode source
// at commit 1e4ad075), not captured from a real session: see
// testdata/README.md. A green test here proves only this reader.

const (
	sessionID = "ses_0a1b2c3d4e5f"
)

// database builds a WAL database from the schema and fixtures. The writer
// comes back open, so that a test can write past the reader.
func database(t *testing.T, cwd string, fixtures ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, name := range fixtures {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		q := strings.ReplaceAll(strings.ReplaceAll(string(b), "CWD", cwd), "NOW", strconv.FormatInt(time.Now().UnixMilli(), 10))
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return path, db
}

func tui(cwd string, argv ...string) harness.Proc {
	return harness.Proc{PID: os.Getpid(), TTY: "/dev/ttys900", Argv: append([]string{"opencode"}, argv...), Start: time.Now().Add(-time.Hour), Cwd: cwd}
}

// server is the shared server's process, as the report describes it.
func server() harness.Proc {
	return harness.Proc{PID: os.Getpid(), Argv: []string{"opencode", "serve", "--service"}, Start: time.Now().Add(-time.Hour)}
}

func provider(db string, procs ...harness.Proc) *Provider {
	return NewWithProcs(db, func() []harness.Proc { return procs })
}

func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

func TestUnverifiedAndNoWriteRoute(t *testing.T) {
	var p harness.Provider = New(filepath.Join(t.TempDir(), "x.db"))
	if p.Info().Verified || p.Info().Name != "opencode" {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("an unverified provider must have no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("an unverified provider must have no hooks")
	}
	if why := harness.ReadOnly("opencode", p); !strings.Contains(why, "unverified") {
		t.Fatalf("read only: %q", why)
	}
}

func TestReadFixtures(t *testing.T) {
	for _, c := range []struct {
		fixture, turn, answer, pending string
		tools                          int
	}{
		{"report-idle.sql", "idle", "The test passes now.", "", 1},
		{"report-working.sql", "busy", "", "", 1},
		{"report-pending.sql", "", "", "bash: go test ./...", 1}, // a tool or an approval: unknown
		{"report-no-claim.sql", "", "", "", 0},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			cwd := t.TempDir()
			path, _ := database(t, cwd, "schema.sql", c.fixture)
			p := provider(path, tui(cwd), server())
			found := p.Discover()
			if len(found) != 1 || found[0].SessionID != sessionID || found[0].Cwd != cwd || found[0].Name != "Fix the test" {
				t.Fatalf("discover: %+v", found)
			}
			turn, at := p.State(found[0])
			if turn != c.turn {
				t.Fatalf("state %q, want %q", turn, c.turn)
			}
			if turn != "" && at.IsZero() {
				t.Fatal("no state time")
			}
			r := p.Read(sessionID, 4)
			if !r.OK || r.WroteAt.IsZero() || r.Activity.Prompt != "fix the test" || r.Activity.Tools != c.tools {
				t.Fatalf("reading: %+v", r)
			}
			if got, _ := harness.LastAnswer(r.Turns); got != c.answer {
				t.Fatalf("answer %q, want %q", got, c.answer)
			}
			now := ""
			if s := r.Activity.Pending; s != nil {
				now = harness.StepText(s.Tool, s.Target)
			}
			if now != c.pending {
				t.Fatalf("step %q, want %q", now, c.pending)
			}
		})
	}
}

// A crash leaves the run's claim in the database: with a dead server or no
// server file nothing counts, also an idle mark.
func TestDeadServerGivesNoState(t *testing.T) {
	cwd := t.TempDir()
	for _, fx := range []string{"report-working.sql", "report-idle.sql"} {
		path, _ := database(t, cwd, "schema.sql", fx)
		for name, procs := range map[string][]harness.Proc{
			"no server":   {tui(cwd)},
			"plain serve": {tui(cwd), {PID: os.Getpid(), Argv: []string{"opencode", "serve"}}},
			"a run":       {tui(cwd), {PID: os.Getpid(), Argv: []string{"opencode", "run", "--service"}}},
			"failed ps":   nil,
		} {
			p := provider(path, procs...)
			if turn, _ := p.State(harness.Session{SessionID: sessionID}); turn != "" {
				t.Fatalf("%s %s: state %q", fx, name, turn)
			}
			if !p.Read(sessionID, 4).OK {
				t.Fatalf("%s %s: no reading", fx, name)
			}
		}
	}
}

// A TUI needs a session in its folder that changed since it started.
func TestMatchNeedsFolderAndUpdateSinceStart(t *testing.T) {
	cwd, other := t.TempDir(), t.TempDir()
	path, db := database(t, cwd, "schema.sql", "report-idle.sql")
	if got := provider(path, tui(other)).Discover(); len(got) != 0 {
		t.Fatalf("other folder: %+v", got)
	}
	late := tui(cwd)
	late.Start = time.Now().Add(time.Hour)
	if got := provider(path, late).Discover(); len(got) != 0 {
		t.Fatalf("started after the last update: %+v", got)
	}
	if _, err := db.Exec(`UPDATE session_v2 SET parent_id = 'ses_parent1234'`); err != nil {
		t.Fatal(err)
	}
	if got := provider(path, tui(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("a subagent's session: %+v", got)
	}
	if _, err := db.Exec(`UPDATE session_v2 SET parent_id = NULL, time_archived = 1`); err != nil {
		t.Fatal(err)
	}
	if got := provider(path, tui(cwd)).Discover(); len(got) != 0 {
		t.Fatalf("an archived session: %+v", got)
	}
}

func TestDeadProcessShowsNoSession(t *testing.T) {
	cwd := t.TempDir()
	path, _ := database(t, cwd, "schema.sql", "report-idle.sql")
	pr := tui(cwd)
	pr.PID = deadPID(t)
	if got := provider(path, pr).Discover(); len(got) != 0 {
		t.Fatalf("dead: %+v", got)
	}
}

func TestSessionProcFilter(t *testing.T) {
	for _, c := range []struct {
		tty  string
		argv []string
		want bool
	}{
		{"/dev/ttys1", []string{"opencode"}, true},
		{"/dev/ttys1", []string{"/x/@opencode/cli-darwin-arm64/.opencode"}, true},
		{"/dev/ttys1", []string{"opencode2", "--model", "x"}, true},
		{"/dev/ttys1", []string{"opencode", "serve", "--service"}, false},
		{"/dev/ttys1", []string{"opencode", "run", "go"}, false},
		{"/dev/ttys1", []string{"node", "/x/packages/cli/bin/opencode.cjs"}, false}, // the wrapper, its child is the session
		{"", []string{"opencode"}, false},
	} {
		if got := sessionProc(harness.Proc{TTY: c.tty, Argv: c.argv}); got != c.want {
			t.Errorf("%v on %q: %v", c.argv, c.tty, got)
		}
	}
}

// V1 has no session_v2: no row and no error, so the Listed level stays.
func TestV1StaysListed(t *testing.T) {
	cwd := t.TempDir()
	path, _ := database(t, cwd, "report-v1.sql")
	p := provider(path, tui(cwd), server())
	if got := p.Discover(); len(got) != 0 {
		t.Fatalf("v1: %+v", got)
	}
	if r := p.Read(sessionID, 4); r.OK {
		t.Fatalf("v1 read: %+v", r)
	}
	if st, d := p.Check(); st != "none" || !strings.Contains(d, "V1") {
		t.Fatalf("v1 check: %s %s", st, d)
	}
}

// mode=ro, not immutable=1: a row that sits only in the -wal file shows,
// the connection refuses a write, and the database file stays the same.
func TestDatabaseIsReadOnlyAndSeesTheWal(t *testing.T) {
	cwd := t.TempDir()
	path, db := database(t, cwd, "schema.sql", "report-working.sql")
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := provider(path, tui(cwd), server())
	if turn, _ := p.State(harness.Session{SessionID: sessionID}); turn != "busy" {
		t.Fatalf("state %q", turn)
	}
	stamp := p.Stamp(sessionID)
	if _, err := db.Exec(`UPDATE session_v2 SET time_idle = time_busy + 1`); err != nil {
		t.Fatal(err)
	}
	if p.Stamp(sessionID) == stamp {
		t.Fatal("a write to the -wal file left the stamp")
	}
	if turn, _ := p.State(harness.Session{SessionID: sessionID}); turn != "idle" {
		t.Fatalf("the -wal file was skipped: state %q", turn)
	}
	ro, err := p.open()
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec(`UPDATE session_v2 SET title = 'written'`); err == nil {
		t.Fatal("the connection took a write")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("the database file changed")
	}
}

// service.json holds the server's password, so this package opens no file
// itself: only the database driver opens one, with the address in dsn.
func TestNoFileOpenInThisPackage(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.SelectorExpr); ok {
				if pkg, ok := call.X.(*ast.Ident); ok && pkg.Name == "os" && call.Sel.Name != "Stat" {
					t.Errorf("%s: os.%s: this package may only stat files", name, call.Sel.Name)
				}
			}
			if lit, ok := n.(*ast.BasicLit); ok && strings.Contains(lit.Value, ".json") {
				t.Errorf("%s: a string names a json file: %s", name, lit.Value)
			}
			return true
		})
	}
}

// Without mode=ro a read can write to a live database. immutable would skip
// a live -wal file.
func TestDatabaseAddressIsReadOnly(t *testing.T) {
	got := dsn("/x/opencode.db")
	if !strings.Contains(got, "mode=ro") || !strings.Contains(got, "query_only(1)") || strings.Contains(got, "immutable") {
		t.Fatalf("address: %s", got)
	}
}

func TestServerProcFilter(t *testing.T) {
	for _, c := range []struct {
		tty  string
		argv []string
		want bool
	}{
		{"", []string{"opencode", "serve", "--service"}, true},
		{"/dev/ttys1", []string{"/x/.opencode", "serve", "--service"}, true},
		{"", []string{"opencode", "serve"}, false},
		{"", []string{"opencode", "run", "--service"}, false},
		{"", []string{"opencode"}, false},
		{"", []string{"claude", "serve", "--service"}, false},
	} {
		if got := serverProc(harness.Proc{TTY: c.tty, Argv: c.argv}); got != c.want {
			t.Errorf("%v on %q: %v", c.argv, c.tty, got)
		}
	}
}

func TestStampAndCheck(t *testing.T) {
	missing := provider(filepath.Join(t.TempDir(), "none.db"))
	if st, _ := missing.Check(); st != "none" {
		t.Fatalf("no database: %s", st)
	}
	if missing.Stamp(sessionID) != "" || missing.Read(sessionID, 4).OK {
		t.Fatal("a stamp or a reading with no database")
	}
	if got := missing.Discover(); got != nil {
		t.Fatalf("discover: %+v", got)
	}
	cwd := t.TempDir()
	empty, _ := database(t, cwd, "schema.sql")
	if st, _ := provider(empty).Check(); st != "none" {
		t.Fatalf("empty database: %s", st)
	}
	path, db := database(t, cwd, "schema.sql", "report-idle.sql")
	p := provider(path)
	if st, d := p.Check(); st != "ok" {
		t.Fatalf("fixture: %s %s", st, d)
	}
	if p.Stamp("../../x") != "" {
		t.Fatal("a stamp for a bad id")
	}
	if _, err := db.Exec(`UPDATE session_message SET type = 'turn'`); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Check(); st != "changed" {
		t.Fatalf("new message kinds: %s", st)
	}
	if _, err := db.Exec(`ALTER TABLE session_v2 DROP COLUMN time_busy`); err != nil {
		t.Fatal(err)
	}
	if st, d := p.Check(); st != "changed" || !strings.Contains(d, "session_v2") {
		t.Fatalf("dropped column: %s %s", st, d)
	}
}
