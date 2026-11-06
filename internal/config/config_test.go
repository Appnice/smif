package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRFCExampleValidates(t *testing.T) {
	c, err := Load("../../testdata/rfc-example.toml")
	if err != nil {
		t.Fatalf("RFC 7.6 example must validate: %v", err)
	}
	if got := len(c.Agents); got != 5 {
		t.Fatalf("agents = %d, want 5", got)
	}
	w := c.Agents["puzzle"]
	if w.Rules[1].Cooldown.Hours() != 6 || w.Rules[1].ReopenAfter != 10 {
		t.Fatalf("cooldown/reopen not decoded: %+v", w.Rules[1])
	}
	if got := w.Rules[1].Match.FromList(); len(got) != 1 || got[0] != "observability" {
		t.Fatalf("from = %v", got)
	}
	if c.Profiles["answer"].MaxTurns != 8 {
		t.Fatalf("built-in answer profile missing")
	}
	if c.Profiles["triage"].Model != "sonnet" {
		t.Fatalf("triage model = %q", c.Profiles["triage"].Model)
	}
}

func TestMissingCatchAllFails(t *testing.T) {
	_, err := Load("../../testdata/no-catchall.toml")
	if err == nil || !strings.Contains(err.Error(), "agents.cafe: the last rule must be a catch-all") {
		t.Fatalf("want catch-all error, got %v", err)
	}
}

const minimal = `
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = {}
handler = "digest"
`

func TestUnknownKeyRejected(t *testing.T) {
	_, err := Parse([]byte(minimal + "\n[daemon]\nbogus = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"extra_args": `
[profiles.p]
extra_args = ["--dangerously-skip-permissions"]
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = {}
handler = "worker"
profile = "p"`,
		"allow_bypass": `
[profiles.p]
permission_mode = "bypassPermissions"
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = {}
handler = "worker"
profile = "p"`,
		"needs a known profile": `
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = {}
handler = "worker"
profile = "nope"`,
		"must be one of": `
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = { kind = "shout" }
handler = "digest"
[[agents.a.rules]]
match = {}
handler = "digest"`,
		"cannot allow Edit": `
[profiles.p]
allowed_tools = ["Edit"]
[agents.a]
repo = "/tmp/a"
[[agents.a.rules]]
match = {}
handler = "digest"`,
		"reserved": `
[agents.human]
repo = "/tmp/a"
[[agents.human.rules]]
match = {}
handler = "digest"`,
	}
	for want, src := range cases {
		_, err := Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

func TestIntentMatches(t *testing.T) {
	cases := []struct {
		glob, intent string
		want         bool
	}{
		{"crash.*", "crash.report", true},
		{"crash.*", "crash.report.x", false},
		{"crash.**", "crash.report.x", true},
		{"store.status", "store.status", true},
		{"store.status", "store.statuses", false},
		{"*", "anything.at.all", true},
		{"", "x", true},
		{"asset.*", "assets.icon", false},
	}
	for _, c := range cases {
		if got := IntentMatches(c.glob, c.intent); got != c.want {
			t.Errorf("IntentMatches(%q,%q)=%v", c.glob, c.intent, got)
		}
	}
}

func TestProfileOverrides(t *testing.T) {
	c, err := Parse([]byte(`
[agents.a]
repo = "/tmp/a"
[agents.a.profile_overrides.triage]
model = "haiku"
max_turns = 5
[[agents.a.rules]]
match = {}
handler = "worker"
profile = "triage"`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.EffectiveProfile("a", "triage")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "haiku" || p.MaxTurns != 5 || p.WriteScope != "none" {
		t.Fatalf("override not applied: %+v", p)
	}
}

func TestAcceptsAndPath(t *testing.T) {
	c, err := Load("../../testdata/rfc-example.toml")
	if err != nil {
		t.Fatal(err)
	}
	w := c.Agents["puzzle"]
	if !c.Accepts(w, "observability") || c.Accepts(w, "stranger") || !c.Accepts(w, Human) {
		t.Fatal("accept_from wrong")
	}
	if a := c.AgentForPath(w.RepoPath() + "/src/x"); a == nil || a.ID != "puzzle" {
		t.Fatalf("AgentForPath = %v", a)
	}
}

func TestAgentForPathSymlinks(t *testing.T) {
	real := t.TempDir()
	os.MkdirAll(filepath.Join(real, "src"), 0o755)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]byte("[agents.web]\nrepo = \"" + real + "\"\n[[agents.web.rules]]\nmatch = {}\nhandler = \"digest\"\n" +
		"[agents.ws]\nrepo = \"" + filepath.Dir(link) + "\"\n[[agents.ws.rules]]\nmatch = {}\nhandler = \"digest\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a := c.AgentForPath(filepath.Join(link, "src")); a == nil || a.ID != "web" {
		t.Fatalf("symlinked cwd resolved to %v", a)
	}
	if a := c.AgentForPath(filepath.Dir(link)); a == nil || a.ID != "ws" {
		t.Fatalf("parent resolved to %v", a)
	}
}

// A custom harness needs a process name; a built-in provider sets command
// only; every name is one word, so nothing can smuggle a second argument.
func TestHarnessesValidate(t *testing.T) {
	ok := `
[harnesses.omnis]
process = ["omnis", "omnis-cli"]
command = "/opt/omnis/bin/omnis"
[harnesses.gemini]
process = ["gemini-beta"]
[harnesses.codex]
command = "/opt/homebrew/bin/codex"
[harnesses.claude]
command = "/opt/bin/claude"
`
	c, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ListedProcesses(); got["omnis-cli"] != "omnis" || got["gemini-beta"] != "gemini" || len(got) != 3 {
		t.Fatalf("processes %v", got)
	}
	for name, want := range map[string]string{"omnis": "/opt/omnis/bin/omnis", "gemini": "gemini", "codex": "/opt/homebrew/bin/codex",
		"claude": "/opt/bin/claude", "cursor": "cursor-agent", "nope": ""} {
		if got := c.HarnessCommand(name); got != want {
			t.Errorf("%s: command %q, want %q", name, got, want)
		}
	}
	if c, _ := Parse(nil); c.HarnessCommand("claude") != "claude" {
		t.Error("claude's command defaults to claude_command")
	}
	for _, bad := range []struct{ toml, want string }{
		{"[harnesses.Omnis]\nprocess = [\"omnis\"]", "a name is"},
		{"[harnesses.omnis]\ncommand = \"omnis\"", "process needs at least one name"},
		{"[harnesses.gemini]", "set process or command"},
		{"[harnesses.codex]\nprocess = [\"mycodex\"]", "a built-in provider may set command only"},
		{"[harnesses.claude]\nprocess = [\"claude2\"]", "a built-in provider may set command only"},
		{"[harnesses.omnis]\nprocess = [\"omnis\"]\ncommand = \"omnis --yolo\"", "one program name or path"},
		{"[harnesses.omnis]\nprocess = [\"omnis\"]\ncommand = \"--yolo\"", "one program name or path"},
		{"[harnesses.omnis]\nprocess = [\"a b\"]", "one process name"},
		{"[harnesses.omnis]\nprocess = [\"/bin/omnis\"]", "one process name"},
		{"[harnesses.omnis]\nprocess = [\"codex\"]", "codex already owns it"},
		{"[harnesses.omnis]\nprocess = [\"claude\"]", "claude already owns it"},
		{"[harnesses.a]\nprocess = [\"x\"]\n[harnesses.b]\nprocess = [\"x\"]", "harnesses.b.process \"x\": a already owns it"},
	} {
		if _, err := Parse([]byte(bad.toml)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: %v, want %q", bad.toml, err, bad.want)
		}
	}
}

func TestContextWarnAndTestCommands(t *testing.T) {
	for v, want := range map[string]int64{"off": 0, "200k": 200000, "1M": 1000000, "50000": 50000} {
		if got, err := ContextWarnTokens(v); err != nil || got != want {
			t.Errorf("ContextWarnTokens(%q) = %d, %v, want %d", v, got, err, want)
		}
	}
	for _, v := range []string{"", "0", "-5k", "lots", "2.5k", "9999999999999k"} {
		if _, err := ContextWarnTokens(v); err == nil {
			t.Errorf("ContextWarnTokens(%q) took a bad value", v)
		}
	}
	load := func(body string) (*Config, error) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p)
	}
	c, err := load("[daemon]\ntest_commands = ['^\\./ci\\.sh']\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Daemon.ContextWarn != "200k" {
		t.Fatalf("default context_warn %q", c.Daemon.ContextWarn)
	}
	if !c.IsTestCommand("./ci.sh --fast") || !c.IsTestCommand("go test ./...") || c.IsTestCommand("ls") {
		t.Fatal("test commands")
	}
	for body, want := range map[string]string{
		"[daemon]\ntest_commands = ['(']\n": "daemon.test_commands[0]",
		"[daemon]\ntest_commands = ['']\n":  "daemon.test_commands[0]",
		"[daemon]\ncontext_warn = 'lots'\n": "daemon.context_warn",
	} {
		if _, err := load(body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// A misspelt key gives its name and line, and never its value.
func TestUnknownKeyHidesValue(t *testing.T) {
	_, err := Parse([]byte("[daemon]\nnotify-url = \"https://ntfy.sh/SECRET\"\n"))
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "unknown keys: daemon.notify-url (line 2)") {
		t.Fatalf("err: %v", err)
	}
}

func TestNotifyURL(t *testing.T) {
	cfg := func(u string) string { return "[daemon]\nnotify_url = \"" + u + "\"\n" }
	for _, u := range []string{"https://ntfy.sh/shop-topic", "https://ntfy.example:8443/a?b=1", "http://localhost:8080/topic",
		"http://LOCALHOST/topic", "http://127.0.0.1:2586/topic", "http://[::1]:2586/topic", "https://user:pass@ntfy.example/topic"} {
		c, err := Parse([]byte(cfg(u)))
		if err != nil || c.Daemon.NotifyURL != u {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if c, err := Parse(nil); err != nil || c.Daemon.NotifyURL != "" {
		t.Fatalf("default: %v", err)
	}
	for u, want := range map[string]string{
		"http://ntfy.sh/SECRET":             "only to localhost",
		"http://localhost.evil.test/SECRET": "only to localhost",
		"http://localhost@evil.test/SECRET": "only to localhost",
		"http://127.0.0.1.evil.test/SECRET": "only to localhost",
		"http://10.0.0.1/SECRET":            "only to localhost",
		"http://0.0.0.0/SECRET":             "only to localhost",
		"ftp://ntfy.sh/SECRET":              "must be https",
		"file:///etc/SECRET":                "not a full URL",
		"ntfy.sh/SECRET":                    "not a full URL",
		"https:///SECRET":                   "not a full URL",
		"https://:443/SECRET":               "not a full URL",
		"https://ntfy.sh/a SECRET":          "not a full URL",
		" https://ntfy.sh/SECRET":           "not a full URL",
		"https://ntfy.sh:port/SECRET":       "not a full URL",
		"https://bad host/SECRET":           "not a full URL",
		"https://ntfy.sh/%zzSECRET":         "not a full URL",
	} {
		_, err := Parse([]byte(cfg(u)))
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "daemon.notify_url") {
			t.Fatalf("%q: %v", u, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("the error quotes the URL: %v", err)
		}
	}
}

// A Codex worker reads only, and no other harness runs a worker.
func TestWorkerHarnessValidate(t *testing.T) {
	base := "[agents.a]\nrepo = \"/tmp\"\n[[agents.a.rules]]\nmatch = {}\nhandler = \"digest\"\n"
	if _, err := Parse([]byte(base + "[profiles.r]\nharness = \"codex\"\n")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"harness = \"codex\"\nwrite_scope = \"worktree\"\n", "harness = \"codex\"\nonward_mcp = true\n", "harness = \"qwen\"\n"} {
		if _, err := Parse([]byte(base + "[profiles.r]\n" + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
