package harness

import (
	"strings"
	"testing"
	"time"
)

type plain struct {
	name     string
	verified bool
	sessions []Session
	reading  Reading
}

func (p plain) Info() Info {
	return Info{Name: p.name, Process: p.name, Verified: p.verified}
}
func (p plain) Discover() []Session      { return p.sessions }
func (p plain) Read(string, int) Reading { return p.reading }

type stateful struct{ plain }

func (stateful) State(s Session) (string, time.Time) { return s.Status, s.StatusAt }

type hooked struct{ plain }

func (hooked) HookEvents() []string { return []string{"Stop"} }

func TestRegistry(t *testing.T) {
	a := plain{name: "a", sessions: []Session{{SessionID: "s1"}}}
	b := plain{name: "b", sessions: []Session{{SessionID: "s2"}}, reading: Reading{OK: true, WroteAt: time.Unix(5, 0)}}
	r := NewRegistry(a, b)
	if r.Get("b") == nil || r.Get("c") != nil || len(r.All()) != 2 {
		t.Fatalf("registry: %+v", r.All())
	}
	ss := r.Discover()
	if len(ss) != 2 || ss[0].Harness != "a" || ss[1].Harness != "b" {
		t.Fatalf("discover: %+v", ss)
	}
	if rd := r.Read("s2", 4); !rd.OK || !rd.WroteAt.Equal(time.Unix(5, 0)) {
		t.Fatalf("read: %+v", rd)
	}
	r.Register(plain{name: "b"})
	if len(r.All()) != 2 || len(r.Discover()) != 1 {
		t.Fatalf("a second provider with a name must replace the first: %+v", r.All())
	}
	if r.Read("s2", 4).OK {
		t.Fatal("no provider has s2 now")
	}
}

// The safety rule of ADR-0019: read only unless the provider is verified
// and has a state source.
func TestReadOnly(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Provider
		ok   bool
	}{
		{"no provider", nil, false},
		{"unverified", plain{name: "x"}, false},
		{"unverified with state", stateful{plain{name: "x"}}, false},
		{"unverified with hooks", hooked{plain{name: "x"}}, false},
		{"verified, no state source", plain{name: "x", verified: true}, false},
		{"verified with state", stateful{plain{name: "x", verified: true}}, true},
		{"verified with hooks", hooked{plain{name: "x", verified: true}}, true},
	} {
		if got := ReadOnly("x", c.p) == ""; got != c.ok {
			t.Errorf("%s: may type = %v, want %v (%s)", c.name, got, c.ok, ReadOnly("x", c.p))
		}
	}
}

func TestLastAnswer(t *testing.T) {
	at := time.Unix(9, 0)
	ts := []Turn{{Role: RoleAgent, Text: "old"}, {Role: RoleAgent, Text: "new", At: at}, {Role: RoleHuman, Text: "q"}}
	if text, when := LastAnswer(ts); text != "new" || !when.Equal(at) {
		t.Fatalf("got %q %v", text, when)
	}
	if text, _ := LastAnswer(nil); text != "" {
		t.Fatalf("got %q", text)
	}
}

func TestValidID(t *testing.T) {
	for id, ok := range map[string]bool{"0a1b2c3d-1111": true, "ses_0a1b2c3dXyZ": true, "short": false, "../../etc/pw": false, "..%2f..%2fetc": false} {
		if ValidID(id) != ok {
			t.Errorf("%q valid != %v", id, ok)
		}
	}
}

// Names from herdr's process table (a source reading, not real sessions).
func TestListedProcess(t *testing.T) {
	for _, c := range []struct {
		argv string
		want string
	}{
		{"codex -v", "codex"},
		{"/opt/homebrew/bin/gemini", "gemini"},
		{"node /Users/a/.npm/bin/codex", "codex"},
		{"node --no-warnings /x/node_modules/@qwen-code/qwen-code/dist/index.js", "qwen"},
		{"python3.12 -m hermes_cli.main", "hermes"},
		{"bun /x/.cursor/cursor-agent/versions/1.2/index.js", "cursor"},
		{"muse-bin-123", "muse"},
		{"muse-bin-x", ""},
		{"kiro-cli-chat", "kiro"},
		{"/x/@opencode/cli-darwin-arm64/.opencode", "opencode"},
		{"node /x/lib/node_modules/@github/copilot/index.js", "copilot"},
		{"node /x/lib/node_modules/@github/copilot/npm-loader.js", ""}, // the wrapper, its child is the session
		{"claude", ""},
		{"Kiro", ""},
		{"node /srv/app/server.js", ""},
		{"vim codex", ""},
		{"zsh", ""},
	} {
		if got := ListedProcess(strings.Fields(c.argv)); got != c.want {
			t.Errorf("%q: %q, want %q", c.argv, got, c.want)
		}
	}
	if ListedProcess(nil) != "" {
		t.Error("nil argv")
	}
}

// The table lets through only what the research report allows. A name it
// does not hold counts as no rule.
func TestHerdrTellsIdle(t *testing.T) {
	for _, name := range []string{"claude", "codex", "qwen", "opencode", "kimi", "pi", "maki"} {
		if !HerdrTellsIdle(name) {
			t.Errorf("%s has an idle rule or a permission source", name)
		}
	}
	for _, name := range []string{"gemini", "copilot", "cursor", "droid", "qoder", "antigravity", "crush", "madeupagent", "Codex", ""} {
		if HerdrTellsIdle(name) {
			t.Errorf("%s has no idle rule", name)
		}
	}
}
