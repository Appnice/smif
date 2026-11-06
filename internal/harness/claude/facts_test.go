package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appnice/smif/internal/harness"
)

// Transcript records, as Claude Code 2.1.318 writes them.

func human(text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"user","timestamp":"2026-11-05T08:00:00Z","message":{"role":"user","content":` + string(b) + `}}`
}

func toolCall(id, name, input string) string {
	return `{"type":"assistant","timestamp":"2026-11-05T08:00:01Z","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}],` +
		`"usage":{"input_tokens":1,"cache_read_input_tokens":1000,"cache_creation_input_tokens":10,"output_tokens":5}}}`
}

func edit(id, file string) string { return toolCall(id, "Edit", `{"file_path":"`+file+`"}`) }

func bash(id, command string) string {
	b, _ := json.Marshal(map[string]string{"command": command})
	return toolCall(id, "Bash", string(b))
}

func result(id, text string, isError bool, extra ...string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-11-05T08:00:02Z"%s,"message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":%v,"content":%s}]}}`,
		strings.Join(extra, ""), id, isError, b)
}

func ok(id string) string { return result(id, "done", false) }

func interrupt(text string) string {
	return `{"type":"user","timestamp":"2026-11-05T08:00:03Z","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}`
}

// queued is the attachment of a prompt that arrives while a turn runs.
func queued(mode, origin string) string {
	return `{"type":"attachment","timestamp":"2026-11-05T08:00:03Z","attachment":{"type":"queued_command","prompt":"next","commandMode":"` + mode + `"` + origin + `}}`
}

// marked is a user record with Claude Code's prompt marks.
func marked(marks, content string) string {
	return `{"type":"user","timestamp":"2026-11-05T08:00:03Z"` + marks + `,"message":{"role":"user","content":` + content + `}}`
}

const (
	pastedText = `[{"type":"text","text":"<pasted_content lines=\"40\">x</pasted_content>"}]`
	imageOnly  = `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]`
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const factSession = "fact-session-01"

// factsProvider is a provider with one empty transcript for factSession.
func factsProvider(t *testing.T) (*Provider, string) {
	t.Helper()
	dir := t.TempDir()
	folder := filepath.Join(dir, "projects", "-r-app")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return New(dir), filepath.Join(folder, factSession+".jsonl")
}

func TestFactsTable(t *testing.T) {
	fix := "/r/app/main.go"
	for _, c := range []struct {
		name    string
		lines   []string
		want    harness.Facts
		isCheck func(string) bool
	}{
		{name: "no edits", lines: []string{human("what is this?"), toolCall("r", "Read", `{"file_path":"/r/a.go"}`), ok("r")},
			want: harness.Facts{}},
		{name: "a test with no edit", lines: []string{human("run it"), bash("t", "go test ./..."), ok("t")},
			want: harness.Facts{}},
		{name: "edit then passing test", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), result("t", "ok  \tapp\t0.1s", false)},
			want: harness.Facts{Files: 1, Tests: harness.TestsPassed}},
		{name: "test then edit", lines: []string{human("fix"), edit("e0", fix), ok("e0"), bash("t", "make check"), ok("t"), edit("e", "/r/app/b.go"), ok("e")},
			want: harness.Facts{Files: 2, Tests: harness.TestsNone}},
		{name: "edit and no test", lines: []string{human("fix"), edit("e", fix), ok("e")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "failing test", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), result("t", "Exit code 1\n--- FAIL: TestX", true)},
			want: harness.Facts{Files: 1, Tests: harness.TestsFailed}},
		{name: "a pipe hides the exit status", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./... | tail -5"), result("t", "--- FAIL: TestX\nFAIL\napp\t0.1s", false)},
			want: harness.Facts{Files: 1, Tests: harness.TestsFailed}},
		{name: "0 failed is a pass", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "npm test | tail"), result("t", "Tests  0 failed | 5 passed", false)},
			want: harness.Facts{Files: 1, Tests: harness.TestsPassed}},
		{name: "a failure, then a pass", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), result("t", "Exit code 1", true), bash("u", "go test ./..."), ok("u")},
			want: harness.Facts{Files: 1, Tests: harness.TestsPassed}},
		{name: "doc-only edit", lines: []string{human("docs"), edit("a", "/r/app/README.md"), ok("a"), edit("b", "/r/app/docs/guide.go"), ok("b"), edit("c", "/r/app/notes.txt"), ok("c")},
			want: harness.Facts{Files: 3}},
		{name: "a doc edit after the test", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), ok("t"), edit("d", "/r/app/README.md"), ok("d")},
			want: harness.Facts{Files: 2, Tests: harness.TestsPassed}},
		{name: "a new prompt resets", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("c", `git commit -m "fix"`), ok("c"), bash("t", "go test ./..."), ok("t"), human("thanks, now explain")},
			want: harness.Facts{ContextTokens: 1011}},
		{name: "system text is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), human("<system-reminder>x</system-reminder>")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "an interrupt keeps the facts", lines: []string{human("fix"), edit("e", fix), ok("e"), interrupt("[Request interrupted by user]")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "an interrupt in a tool call keeps the facts", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("c", "git commit -m a"), ok("c"), interrupt("[Request interrupted by user for tool use]")},
			want: harness.Facts{Files: 1, Commits: 1, Tests: harness.TestsNone}},
		{name: "a queued prompt resets", lines: []string{human("fix"), edit("e", fix), ok("e"), queued("prompt", `,"origin":{"kind":"human"},"humanTurn":true`)},
			want: harness.Facts{}},
		{name: "a queued prompt with no origin resets", lines: []string{human("fix"), edit("e", fix), ok("e"), queued("prompt", "")},
			want: harness.Facts{}},
		{name: "facts after a queued prompt count", lines: []string{human("fix"), edit("e", fix), ok("e"), queued("prompt", `,"origin":{"kind":"human"}`), edit("f", "/r/app/b.go"), ok("f")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a peer's queued message is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), queued("prompt", `,"origin":{"kind":"peer","from":"x"},"isMeta":true`)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a task notification is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), queued("task-notification", `,"origin":{"kind":"human"}`)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a typed paste resets", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"typed","origin":{"kind":"human"}`, pastedText)},
			want: harness.Facts{}},
		{name: "a queued image with no text resets", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"queued","origin":{"kind":"human"}`, imageOnly)},
			want: harness.Facts{}},
		{name: "local command output is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), human("<local-command-stdout>done</local-command-stdout>")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a local command is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), human("<command-name>/mcp</command-name><command-message>mcp</command-message>")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a project command resets", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"origin":{"kind":"human"}`, `"<command-message>design</command-message>"`)},
			want: harness.Facts{}},
		{name: "an sdk paste resets", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"sdk"`, pastedText)},
			want: harness.Facts{}},
		{name: "an accepted suggestion resets", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"suggestion_accepted","origin":{"kind":"human"}`, pastedText)},
			want: harness.Facts{}},
		{name: "a system record is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"system","origin":{"kind":"task-notification"}`, `"<task-notification>x</task-notification>"`)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "an image with no mark is no prompt", lines: []string{human("fix"), edit("e", fix), ok("e"), marked("", imageOnly)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a marked interrupt keeps the facts", lines: []string{human("fix"), edit("e", fix), ok("e"), marked(`,"promptSource":"typed"`, `[{"type":"text","text":"[Request interrupted by user]"}]`)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "commits", lines: []string{human("ship"), bash("a", `git add a && git commit -m "a"`), ok("a"), bash("b", `git commit -m "make test passes"`), ok("b"), bash("c", "git status"), ok("c")},
			want: harness.Facts{Commits: 2}},
		{name: "a refused commit and edit did not happen", lines: []string{human("ship"), bash("a", "git commit -m a"), result("a", "Exit code 1\nnothing to commit", true),
			edit("e", fix), result("e", "<tool_use_error>File has not been read yet</tool_use_error>", true)},
			want: harness.Facts{}},
		{name: "a denied test did not run", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), result("t", "The user doesn't want to proceed with this tool use.", true)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a test timed out", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./..."), result("t", "Command timed out after 2m 0s", true)},
			want: harness.Facts{Files: 1, Tests: harness.TestsFailed}},
		{name: "a test in the background has no result", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "make check"),
			result("t", "Command running in background with ID: x", false, `,"toolUseResult":{"backgroundTaskId":"x"}`)},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "a test still runs", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "go test ./...")},
			want: harness.Facts{Files: 1, Tests: harness.TestsNone}},
		{name: "an edit still waits for its result", lines: []string{human("fix"), edit("e", fix)},
			want: harness.Facts{}},
		{name: "one file twice, a write, a notebook", lines: []string{human("fix"), edit("a", fix), ok("a"), edit("b", fix), ok("b"),
			toolCall("c", "Write", `{"file_path":"/r/new.go"}`), ok("c"), toolCall("d", "NotebookEdit", `{"notebook_path":"/r/n.ipynb"}`), ok("d"), toolCall("f", "MultiEdit", `{"file_path":"/r/m.go"}`), ok("f")},
			want: harness.Facts{Files: 4, Tests: harness.TestsNone}},
		{name: "a subagent's edit does not count", lines: []string{human("fix"),
			strings.Replace(edit("e", fix), `"type":"assistant"`, `"type":"assistant","isSidechain":true`, 1), ok("e")},
			want: harness.Facts{}},
		{name: "daemon.test_commands", lines: []string{human("fix"), edit("e", fix), ok("e"), bash("t", "./ci.sh"), ok("t")},
			isCheck: func(s string) bool { return s == "./ci.sh" },
			want:    harness.Facts{Files: 1, Tests: harness.TestsPassed}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, path := factsProvider(t)
			writeLines(t, path, c.lines...)
			got, ok := p.Facts(factSession, c.isCheck)
			if !ok {
				t.Fatal("no facts")
			}
			if c.want.ContextTokens == 0 {
				got.ContextTokens = 0 // TestFactsContextTokens checks them
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// ContextTokens are the newest assistant record's input tokens. A record
// with no usage says nothing, and a subagent's context is its own.
func TestFactsContextTokens(t *testing.T) {
	p, path := factsProvider(t)
	writeLines(t, path, human("hi"),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"a"}],"usage":{"input_tokens":3,"cache_read_input_tokens":400000,"cache_creation_input_tokens":12000,"output_tokens":99}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"sub"}],"usage":{"input_tokens":5,"cache_read_input_tokens":7}}}`,
		human("more"))
	if f, _ := p.Facts(factSession, nil); f.ContextTokens != 412003 {
		t.Fatalf("tokens %d", f.ContextTokens)
	}
}

// The cursor: an unchanged transcript parses no bytes, an appended record
// parses only itself, a half-written line waits for its newline, and a
// shorter file is read again.
func TestFactsReadOnlyAppendedBytes(t *testing.T) {
	p, path := factsProvider(t)
	first := []string{human("fix"), edit("e", "/r/a.go"), ok("e")}
	writeLines(t, path, first...)
	size := func() int64 {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	if f, _ := p.Facts(factSession, nil); f.Files != 1 || f.Tests != harness.TestsNone {
		t.Fatalf("first read: %+v", f)
	}
	if p.parsed != size() {
		t.Fatalf("first read parsed %d of %d bytes", p.parsed, size())
	}

	p.parsed = 0
	if f, _ := p.Facts(factSession, nil); f.Files != 1 || p.parsed != 0 {
		t.Fatalf("unchanged: %+v, %d bytes parsed", f, p.parsed)
	}

	add := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	rec := bash("t", "go test ./...") + "\n"
	add(rec)
	if f, _ := p.Facts(factSession, nil); f.Tests != harness.TestsNone || p.parsed != int64(len(rec)) {
		t.Fatalf("appended: %+v, %d bytes parsed, want %d", f, p.parsed, len(rec))
	}

	// The cursor keeps the call across reads: the result comes later, in
	// two writes.
	p.parsed = 0
	res := ok("t") + "\n"
	add(res[:20])
	if f, _ := p.Facts(factSession, nil); f.Tests != harness.TestsNone || p.parsed != 0 {
		t.Fatalf("half a line: %+v, %d bytes parsed", f, p.parsed)
	}
	add(res[20:])
	if f, _ := p.Facts(factSession, nil); f.Tests != harness.TestsPassed || p.parsed != int64(len(res)) {
		t.Fatalf("the rest of the line: %+v, %d bytes parsed, want %d", f, p.parsed, len(res))
	}

	writeLines(t, path, human("new start"))
	if f, _ := p.Facts(factSession, nil); f != (harness.Facts{}) {
		t.Fatalf("a shorter file: %+v", f)
	}
}

// A large transcript: the first read takes only its end, from a whole line.
func TestFactsLargeTranscript(t *testing.T) {
	p, path := factsProvider(t)
	filler := human(strings.Repeat("x", 1<<20))
	var b strings.Builder
	for b.Len() < factsTail+(4<<20) {
		b.WriteString(filler + "\n")
	}
	b.WriteString(human("fix") + "\n" + edit("e", "/r/a.go") + "\n" + ok("e") + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f, ok := p.Facts(factSession, nil)
	if !ok || f.Files != 1 || p.parsed > factsTail {
		t.Fatalf("facts %+v, %d bytes parsed of %d", f, p.parsed, b.Len())
	}
}

func TestFactsNoTranscript(t *testing.T) {
	p, _ := factsProvider(t)
	if _, ok := p.Facts("no-such-session", nil); ok {
		t.Fatal("facts with no transcript")
	}
	if _, ok := p.Facts("../../etc", nil); ok {
		t.Fatal("facts for a bad id")
	}
}
