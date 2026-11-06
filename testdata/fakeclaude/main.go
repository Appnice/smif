// Command fakeclaude is a scripted stand-in for `claude -p` (RFC 9.4).
//
// It accepts the real flag set, appends one JSON record per invocation to
// $SMIF_FAKE_LOG (argv, cwd, selected env, stdin, pid), reads stdin and
// writes stream JSON: an init event with session_id, assistant messages and a
// final result with usage and cost. Behaviour is scripted by directives in
// the prompt text:
//
//	#fake:result=needs_human|failed     result status
//	#fake:sleep=<seconds>               sleep before answering
//	#fake:sleep1=<seconds>              sleep only on attempt 1
//	#fake:bad_output[=always]           invalid result (once, or also on repair)
//	#fake:edit=<path>                   write a file in the cwd
//	#fake:spawn_child                   start a child in the same process group
//	#fake:exit=<code>                   exit with code after init, no result
//	#fake:followup=<agent>|sender       ask <agent> (or the sender) the same thing again
//	#fake:tokens=<n>                    input tokens to report
//	#fake:cost=<usd>                    cost to report
//	#fake:artifact=<path>               return path as an artifact
//	#fake:overload                      report an API overload error
//	#fake:context_overflow              report a context-length failure
//	#fake:reply=<text>                  reply text for an ask
//	#fake:noresume                      pretend --resume is unknown (exit 1)
//
// The prompt the runner sends for a compaction turn contains SMIF_COMPACT.
//
// `agents --json --all` prints $SMIF_FAKE_LOG/agents.json, or [] with no such
// file, and appends the time to agents.calls. It writes no invocation
// record. A file agents.fail makes it exit 3, and agents.sleep holds the
// seconds to sleep first.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var directiveRe = regexp.MustCompile(`#fake:([a-z0-9_]+)(?:=([^\s"\\]+))?`)

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Println("2.1.316 (Claude Code) [fake]")
		return
	}
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println(helpText)
		return
	}
	if len(args) > 0 && args[0] == "mcp" {
		fakeMCP(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "agents" {
		fakeAgents()
		return
	}
	flags := parseFlags(args)
	stdin, _ := io.ReadAll(os.Stdin)
	prompt := string(stdin)
	if p, ok := flags["_prompt"]; ok {
		prompt = p + "\n" + prompt
	}
	dirs := map[string]string{}
	for _, m := range directiveRe.FindAllStringSubmatch(prompt, -1) {
		if _, seen := dirs[m[1]]; !seen {
			dirs[m[1]] = m[2]
		}
	}
	cwd, _ := os.Getwd()
	session := flags["--resume"]
	if session == "" {
		session = flags["--session-id"]
	}
	if _, fork := flags["--fork-session"]; fork || session == "" {
		session = uuid() // --fork-session: a new id for the resumed context
	}
	record(map[string]any{
		"argv": os.Args, "cwd": cwd, "pid": os.Getpid(), "stdin": string(stdin),
		"session_id": session,
		"env": map[string]string{
			"SMIF_AGENT":     os.Getenv("SMIF_AGENT"),
			"SMIF_RUN_ID":    os.Getenv("SMIF_RUN_ID"),
			"SMIF_RUN_TOKEN": os.Getenv("SMIF_RUN_TOKEN"),
		},
		"time": time.Now().UTC().Format(time.RFC3339Nano),
	})
	for _, m := range regexp.MustCompile(`"local_read_only_path":\s*"([^"]+)"`).FindAllStringSubmatch(prompt, -1) {
		rec := map[string]any{"attachment_path": m[1]}
		if fi, err := os.Stat(m[1]); err == nil {
			rec["mode"] = fmt.Sprintf("%o", fi.Mode().Perm())
			b, _ := os.ReadFile(m[1])
			h := sha256.Sum256(b)
			rec["sha256"] = hex.EncodeToString(h[:])
		}
		record(rec)
	}
	if _, ok := dirs["noresume"]; ok && flags["--resume"] != "" {
		fmt.Fprintln(os.Stderr, "error: unknown option '--resume'")
		os.Exit(1)
	}

	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session,
		"model": flags["--model"], "permissionMode": flags["--permission-mode"],
		"claude_code_version": "2.1.316", "tools": []string{"Read", "Grep"}, "mcp_servers": []any{}})

	if _, ok := dirs["spawn_child"]; ok {
		child := exec.Command("sleep", "300")
		_ = child.Start() // inherits our process group
		record(map[string]any{"child_pid": child.Process.Pid, "pgid": pgid()})
	}
	if v, ok := dirs["sleep1"]; ok && regexp.MustCompile(`"attempt":\s*1\b`).MatchString(prompt) {
		n, _ := strconv.ParseFloat(v, 64)
		time.Sleep(time.Duration(n * float64(time.Second)))
	}
	if v, ok := dirs["sleep"]; ok {
		n, _ := strconv.ParseFloat(v, 64)
		time.Sleep(time.Duration(n * float64(time.Second)))
	}
	if v, ok := dirs["exit"]; ok {
		code, _ := strconv.Atoi(v)
		fmt.Fprintln(os.Stderr, "fake: exiting with", code)
		os.Exit(code)
	}
	if p, ok := dirs["edit"]; ok && !strings.Contains(prompt, "SMIF_REPAIR") {
		full := filepath.Join(cwd, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("edited by fake "+os.Getenv("SMIF_RUN_ID")+"\n"), 0o644)
	}

	tokens := 1000
	if v, ok := dirs["tokens"]; ok {
		tokens, _ = strconv.Atoi(v)
	}
	cost := 0.001
	if v, ok := dirs["cost"]; ok {
		cost, _ = strconv.ParseFloat(v, 64)
	}
	usage := map[string]any{"input_tokens": tokens, "output_tokens": 50, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}

	if _, ok := dirs["overload"]; ok {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": "API Error: 529 Overloaded", "session_id": session, "num_turns": 1,
			"total_cost_usd": 0, "usage": usage})
		os.Exit(1)
	}
	if _, ok := dirs["context_overflow"]; ok {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": "Prompt is too long: context length exceeded", "session_id": session, "num_turns": 1,
			"total_cost_usd": cost, "usage": usage})
		os.Exit(1)
	}

	var result map[string]any
	switch {
	case strings.Contains(prompt, "SMIF_COMPACT"):
		result = map[string]any{"memory": "fake memory carried from session " + session}
	default:
		status := "done"
		if v, ok := dirs["result"]; ok {
			status = v
		}
		intent := field(prompt, "intent")
		subject := field(prompt, "subject")
		result = map[string]any{"status": status, "summary": "fake handled " + intent}
		if strings.Contains(prompt, `"kind": "ask"`) || strings.Contains(prompt, `"kind":"ask"`) {
			reply := "fake reply to " + subject
			if v, ok := dirs["reply"]; ok {
				reply = v
			}
			result["reply"] = reply
		}
		if status == "needs_human" {
			result["human_question"] = "fake needs a decision on " + subject
		}
		if a, ok := dirs["artifact"]; ok {
			result["artifacts"] = []any{map[string]any{"path": a, "description": "requested asset"}}
		}
		if to, ok := dirs["followup"]; ok {
			if to == "sender" {
				to = field(prompt, "agent") // from.agent is the first "agent" in the envelope
			}
			body := field(prompt, "body")
			result["followups"] = []any{map[string]any{"to": to, "kind": "ask", "intent": nonEmpty(intent, "loop.ping"),
				"subject": "follow-up: " + subject, "body": body}}
		}
	}
	bad := false
	if v, ok := dirs["bad_output"]; ok && !strings.Contains(prompt, "SMIF_REPAIR") {
		bad = true
		if v == "always" {
			markBad(session) // the repair turn on this session fails too
		}
	}
	if strings.Contains(prompt, "SMIF_REPAIR") && lastBad(session) {
		bad = true
	}

	resText, _ := json.Marshal(result)
	_, schema := flags["--json-schema"]
	final := map[string]any{"type": "result", "subtype": "success", "is_error": false,
		"session_id": session, "num_turns": 2, "total_cost_usd": cost, "usage": usage, "duration_ms": 10}
	switch {
	case bad:
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": "I handled it but forgot the JSON block."}}}})
		final["result"] = "I handled it but forgot the JSON block."
	case schema:
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": "Done."}}}})
		final["result"] = string(resText)
		final["structured_output"] = result
	default:
		text := "Done.\n\n```json\n" + string(resText) + "\n```"
		emit(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}}}})
		final["result"] = text
	}
	emit(final)
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// field pulls "name": "value" out of the rendered envelope in the prompt.
func field(prompt, name string) string {
	re := regexp.MustCompile(`"` + name + `":\s*"((?:[^"\\]|\\.)*)"`)
	m := re.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &s); err != nil {
		return m[1]
	}
	return s
}

func parseFlags(args []string) map[string]string {
	valued := map[string]bool{"--output-format": true, "--resume": true, "--model": true, "--permission-mode": true,
		"--max-turns": true, "--append-system-prompt": true, "--mcp-config": true, "--json-schema": true,
		"--effort": true, "--max-budget-usd": true, "--session-id": true, "--permission-prompts": true,
		"--settings": true, "--name": true, "-n": true, "--system-prompt": true, "--fallback-model": true}
	multi := map[string]bool{"--allowedTools": true, "--allowed-tools": true, "--disallowedTools": true,
		"--disallowed-tools": true, "--add-dir": true, "--tools": true}
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
			out[k] = v
			continue
		}
		switch {
		case valued[a] && i+1 < len(args):
			out[a] = args[i+1]
			i++
		case multi[a]:
			var vals []string
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				vals = append(vals, args[i+1])
				i++
			}
			out[a] = strings.Join(vals, " ")
		case strings.HasPrefix(a, "-"):
			out[a] = ""
		default:
			out["_prompt"] = a
		}
	}
	return out
}

func logDir() string { return os.Getenv("SMIF_FAKE_LOG") }

// fakeAgents stands in for `claude agents --json --all`.
func fakeAgents() {
	dir := logDir()
	if dir == "" {
		fmt.Println("[]")
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "agents.calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, time.Now().UnixMilli())
		f.Close()
	}
	if b, err := os.ReadFile(filepath.Join(dir, "agents.sleep")); err == nil {
		s, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		time.Sleep(time.Duration(s * float64(time.Second)))
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.fail")); err == nil {
		fmt.Fprintln(os.Stderr, "agents failed")
		os.Exit(3)
	}
	b, err := os.ReadFile(filepath.Join(dir, "agents.json"))
	if err != nil {
		fmt.Println("[]")
		return
	}
	os.Stdout.Write(b)
}

func record(v map[string]any) {
	dir := logDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "invocations.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(v)
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	f.Write(append(b, '\n'))
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func markBad(session string) {
	if d := logDir(); d != "" {
		_ = os.WriteFile(filepath.Join(d, session+".bad"), nil, 0o644)
	}
}

func lastBad(session string) bool {
	if d := logDir(); d != "" {
		_, err := os.Stat(filepath.Join(d, session+".bad"))
		return err == nil
	}
	return false
}

func pgid() int {
	g, _ := syscall.Getpgid(os.Getpid())
	return g
}

const helpText = `Usage: claude [options] [command] [prompt]
  --allowedTools, --allowed-tools <tools...>
  --append-system-prompt <prompt>
  --disallowedTools, --disallowed-tools <tools...>
  --effort <level>
  --json-schema <schema>
  --max-budget-usd <amount>
  --mcp-config <configs...>
  --model <model>
  --output-format <format>  (choices: "text", "json", "stream-json")
  --permission-mode <mode>  (choices: "acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan")
  --permission-prompts <target>
  -p, --print
  -r, --resume [value]
  --safe-mode
  --session-id <uuid>
  --tools <tools...>
  --settings <file-or-json>
  -n, --name <name>
  --strict-mcp-config
  --verbose`

// fakeMCP emulates `claude mcp get|add|remove` with a marker file.
func fakeMCP(args []string) {
	record(map[string]any{"mcp": args})
	marker := filepath.Join(logDir(), "mcp-registered")
	if len(args) == 0 {
		os.Exit(1)
	}
	switch args[0] {
	case "get":
		if _, err := os.Stat(marker); err != nil {
			fmt.Fprintln(os.Stderr, "No MCP server found with name: smif")
			os.Exit(1)
		}
	case "add":
		_ = os.WriteFile(marker, []byte(strings.Join(args, " ")), 0o644)
		fmt.Println("Added stdio MCP server smif to user config")
	case "remove":
		if err := os.Remove(marker); err != nil {
			os.Exit(1)
		}
		fmt.Println("Removed MCP server smif")
	}
}
