package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appnice/smif/internal/api"
)

// typedRows reads the log terminal: one row per text typed, in order.
func typedRows(t *testing.T, path string) []struct{ SessionID, Text string } {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []struct{ SessionID, Text string }
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var r struct {
			SessionID string  `json:"session_id"`
			Text      *string `json:"text"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("log terminal row %q: %v", sc.Text(), err)
		}
		if r.Text != nil {
			rows = append(rows, struct{ SessionID, Text string }{r.SessionID, *r.Text})
		}
	}
	return rows
}

// With --session the id prefix picks the session, so every positional word
// is prompt text. Without it, the first word names the session. A lost word
// can change what a prompt means ("do not delete it"), and the text goes
// into a real session.
func TestPromptWords(t *testing.T) {
	typed := filepath.Join(t.TempDir(), "typed.jsonl")
	e := NewEnv(t, `terminal = "log:`+typed+`"`, "", AgentSpec{ID: "app", Rules: digestRule()})
	e.Start()
	// Two idle sessions of one agent: the case --session exists for. The
	// test process stands in for claude.
	idle := func(session, title string) {
		t.Helper()
		e.Call("presence.update", api.PresenceParams{Event: "idle", Agent: "app", SessionID: session, PID: os.Getpid(),
			TTY: "/dev/ttys999", Term: "Apple_Terminal", Title: title}, nil)
	}
	const flagged, named = "s-flag-0001", "s-name-0002"
	idle(flagged, "")
	idle(named, "release train")

	sent := 0
	for _, c := range []struct {
		name    string
		args    []string
		session string
		want    string
	}{
		{"flag_two_words", []string{"--session", "s-flag", "one", "two"}, flagged, "one two"},
		{"flag_one_word", []string{"--session", "s-flag", "hello"}, flagged, "hello"},
		{"flag_keeps_the_negation", []string{"--session", "s-flag", "do", "not", "delete", "it"}, flagged, "do not delete it"},
		{"name_two_words", []string{"release train", "one", "two"}, named, "one two"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := e.CLI("", append([]string{"prompt"}, c.args...)...)
			if err != nil || !strings.Contains(out, "sent to app (session "+api.ShortSession(c.session)) {
				t.Fatalf("smif prompt %q: %v %s", c.args, err, out)
			}
			sent++
			rows := typedRows(t, typed)
			if len(rows) != sent {
				t.Fatalf("%d rows typed, want %d: %+v", len(rows), sent, rows)
			}
			if got := rows[sent-1]; got.Text != c.want || got.SessionID != c.session {
				t.Fatalf("typed %+v, want %q into %s", got, c.want, c.session)
			}
			title := ""
			if c.session == named {
				title = "release train"
			}
			idle(c.session, title)
		})
	}

	// --session with no words is an empty prompt: nothing gets typed.
	if out, err := e.CLI("", "prompt", "--session", "s-flag"); err == nil {
		t.Fatalf("a prompt with no text was sent: %s", out)
	}
	if rows := typedRows(t, typed); len(rows) != sent {
		t.Fatalf("%d rows typed, want %d: %+v", len(rows), sent, rows)
	}
}
