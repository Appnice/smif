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
