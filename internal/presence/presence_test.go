package presence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptReadsNewestTitleAndPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"type":"custom-title","customTitle":"old","sessionId":"s"}`,
		`{"type":"last-prompt","lastPrompt":"first","sessionId":"s"}`,
		`not json with "custom-title"`,
		`{"type":"user","message":{"content":"mentions \"custom-title\" in text"}}`,
		`{"type":"custom-title","customTitle":"rfc","sessionId":"s"}`,
		`{"type":"last-prompt","lastPrompt":"run the tests","sessionId":"s"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if title, prompt := Transcript(path); title != "rfc" || prompt != "run the tests" {
		t.Fatalf("got %q %q", title, prompt)
	}
	// Only the end of a long transcript is read; the newest records are there.
	big := strings.Repeat(`{"type":"assistant","x":"`+strings.Repeat("a", 1000)+`"}`+"\n", 1500)
	if err := os.WriteFile(path, []byte(`{"type":"custom-title","customTitle":"lost"}`+"\n"+big+lines[4]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if title, _ := Transcript(path); title != "rfc" {
		t.Fatalf("long transcript title %q", title)
	}
	for _, p := range []string{"", filepath.Join(dir, "missing.jsonl"), filepath.Join(dir, "x.txt")} {
		if a, b := Transcript(p); a != "" || b != "" {
			t.Errorf("%q gave %q %q", p, a, b)
		}
	}
}

func TestConversationFinalMessage(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		t.Helper()
		p := filepath.Join(dir, "c.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long := strings.Repeat("The release is live in both stores. ", 6)
	// Steps between tool calls are dropped; the text after the last one is the answer.
	p := write(
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"live?"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"text","text":"Checking the stores."},{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:05Z","message":{"content":[{"type":"text","text":"`+long+`"}]}}`,
	)
	if ts := Conversation(p, 5); len(ts) != 2 || ts[1].Text != strings.TrimSpace(long) || ts[1].Tools != 1 {
		t.Fatalf("final message: %+v", ts)
	}
	// Answered, then saved a note and ended short: both show, the long one first.
	p = write(
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"live?"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:05Z","message":{"content":[{"type":"text","text":"`+long+`"},{"type":"tool_use"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:06Z","message":{"content":[{"type":"text","text":"Saved."}]}}`,
	)
	if ts := Conversation(p, 5); len(ts) != 2 || ts[1].Text != strings.TrimSpace(long)+"\n\nSaved." {
		t.Fatalf("short final message: %q", ts[1].Text)
	}
}

// The newest exchange's activity: the prompt, each tool call with its
// target, which ones are done, and the pending one (what runs now, or
// what a waiting session asks for in its tab).
func TestReadActivity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-11-03T08:00:00Z","message":{"content":"old prompt"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T08:00:01Z","message":{"content":[{"type":"tool_use","id":"t0","name":"Read","input":{"file_path":"/x/old.md"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:00:00Z","message":{"content":"push the fix"}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:00:02Z","message":{"content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make check\nmake e2e","description":"run checks"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:01:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-03T09:01:02Z","message":{"content":[{"type":"tool_use","id":"t2","name":"mcp__claude_ai_Slack__slack_send_message","input":{"text":"hi"}},{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"git push origin main"}}]}}`,
		`{"type":"user","timestamp":"2026-11-03T09:01:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"sent"}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-03T09:01:04Z","message":{"content":[{"type":"tool_use","id":"s1","name":"Grep","input":{"pattern":"x"}}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, act := Read(p, 10)
	if len(turns) != 4 || act.Prompt != "push the fix" || !act.PromptAt.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("turns %d, activity %+v", len(turns), act)
	}
	if act.Tools != 3 || len(act.Steps) != 3 {
		t.Fatalf("steps: %+v", act.Steps)
	}
	s := act.Steps
	if s[0].Tool != "Bash" || s[0].Target != "make check …" || !s[0].Done ||
		s[1].Tool != "slack_send_message" || s[1].Target != "" || !s[1].Done ||
		s[2].Target != "git push origin main" || s[2].Done {
		t.Fatalf("steps: %+v", s)
	}
	if act.Pending == nil || act.Pending.Target != "git push origin main" {
		t.Fatalf("pending: %+v", act.Pending)
	}

	// A question in its tab: the pending step carries the question.
	q := `{"type":"assistant","timestamp":"2026-11-03T09:02:00Z","message":{"content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"question":"Which store first?","options":[]}]}}]}}`
	if err := os.WriteFile(p, []byte(lines[2]+"\n"+q+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, act := Read(p, 10); act.Pending == nil || act.Pending.Tool != "AskUserQuestion" || act.Pending.Target != "Which store first?" {
		t.Fatalf("question: %+v", act.Pending)
	}
}
