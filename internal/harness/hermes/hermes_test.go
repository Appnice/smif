package hermes

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/harness"
)

const sessionID = "local-0a1b2c3d4e5f"

// The columns are the ones v0.23.0 has, reduced to what smif reads.
const schema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, cwd TEXT, title TEXT, started_at REAL NOT NULL, ended_at REAL);
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT,
	tool_calls TEXT, tool_call_id TEXT, finish_reason TEXT, timestamp REAL NOT NULL, active INTEGER NOT NULL DEFAULT 1);
CREATE TABLE session_turn_leases (conversation_id TEXT PRIMARY KEY, holder TEXT NOT NULL, acquired_at REAL NOT NULL, expires_at REAL NOT NULL);`

func database(t *testing.T, cwd string, sqls ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := float64(time.Now().Unix())
	all := append([]string{schema, fmt.Sprintf(`INSERT INTO sessions VALUES ('%s', 'local', '%s', 'a title', %f, NULL)`, sessionID, cwd, now)}, sqls...)
	for _, q := range all {
		if _, err := db.Exec(strings.ReplaceAll(q, "NOW", fmt.Sprintf("%f", now))); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func msg(role, content, finish string) string {
	return fmt.Sprintf(`INSERT INTO messages (session_id, role, content, finish_reason, timestamp) VALUES ('%s', '%s', '%s', '%s', NOW)`,
		sessionID, role, content, finish)
}

const toolCall = `INSERT INTO messages (session_id, role, tool_calls, finish_reason, timestamp) VALUES ('` + sessionID + `', 'assistant',
	'[{"id":"call_1","function":{"name":"terminal","arguments":"{\"command\":\"make test\"}"}}]', 'tool_calls', NOW)`

func TestState(t *testing.T) {
	lease := func(offset string) string {
		return `INSERT INTO session_turn_leases VALUES ('` + sessionID + `', 'pid=1', NOW, NOW` + offset + `)`
	}
	for _, c := range []struct {
		name, want string
		sqls       []string
	}{
		{"answered", "idle", []string{msg("user", "hi", ""), msg("assistant", "done", "stop")}},
		{"a live lease on an old answer", "busy", []string{msg("assistant", "done", "stop"), lease("+300")}},
		// An approval or a question: the lease is live and a tool call is open.
		{"waiting in a turn", "busy", []string{msg("user", "hi", ""), toolCall, lease("+300")}},
		{"a lost lease on a tool call", "", []string{msg("user", "hi", ""), toolCall, lease("-300")}},
		{"no answer yet", "", []string{msg("user", "hi", "")}},
		{"no messages", "", nil},
		{"a dead lease on an answer", "idle", []string{msg("assistant", "done", "stop"), lease("-300")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := New(database(t, "/w", c.sqls...))
			got, _ := p.State(harness.Session{SessionID: sessionID})
			if got != c.want {
				t.Fatalf("state %q, want %q", got, c.want)
			}
		})
	}
}

func TestRead(t *testing.T) {
	p := New(database(t, "/w", msg("user", "run the tests", ""), toolCall,
		`INSERT INTO messages (session_id, role, tool_call_id, timestamp) VALUES ('`+sessionID+`', 'tool', 'call_1', NOW)`,
		msg("assistant", "all green", "stop")))
	r := p.Read(sessionID, 5)
	if !r.OK || len(r.Turns) == 0 {
		t.Fatalf("read: %+v", r)
	}
	last := r.Turns[len(r.Turns)-1]
	if !strings.Contains(fmt.Sprintf("%+v", last), "all green") {
		t.Fatalf("turn: %+v", last)
	}
	if p.Read("local-missing-id", 5).OK {
		t.Fatal("read an unknown session")
	}
}

func TestProcesses(t *testing.T) {
	proc := harness.Proc{PID: 1, TTY: "/dev/ttys999", Start: time.Now().Add(-time.Minute),
		Argv: []string{"/home/me/.hermes/tools/python-3.14/bin/python3", "-I", "-c", "import", "sys"}}
	gateway := harness.Proc{PID: 1, TTY: "/dev/ttys998", Start: proc.Start,
		Argv: []string{"/home/me/.hermes/tools/python-3.14/bin/python3", "-m", "hermes_cli.main", "gateway", "run"}}
	if !sessionProc(proc) || sessionProc(gateway) {
		t.Fatalf("session proc: %v %v", sessionProc(proc), sessionProc(gateway))
	}
	if got := harness.ListedProcess([]string{"/usr/bin/python3", "-c", "import"}); got != "" {
		t.Fatalf("another python: %q", got)
	}
}

func TestInfoTypesWithNoForkOrHooks(t *testing.T) {
	var p harness.Provider = New(filepath.Join(t.TempDir(), "x.db"))
	if !p.Info().Verified || p.Info().Name != Name {
		t.Fatalf("info: %+v", p.Info())
	}
	if _, ok := p.(harness.Forker); ok {
		t.Fatal("hermes has no fork")
	}
	if _, ok := p.(harness.Hooker); ok {
		t.Fatal("hermes has no hooks")
	}
	if why := harness.ReadOnly(Name, p); why != "" {
		t.Fatalf("read only: %q", why)
	}
	if !harness.ValidID("local-" + strings.Repeat("a", 64)) {
		t.Fatal("a real Hermes id is not valid")
	}
}

func TestCheck(t *testing.T) {
	if got, _ := New(filepath.Join(t.TempDir(), "x.db")).Check(); got != "none" {
		t.Fatalf("no database: %s", got)
	}
	if got, d := New(database(t, "/w", msg("user", "hi", ""))).Check(); got != "ok" {
		t.Fatalf("ok: %s %s", got, d)
	}
}
