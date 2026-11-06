package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

// A remote's sessions show read only, a new wait on it notifies, a name that
// reads as an ssh option fails validation, and a host that goes away is a
// warning (ADR-0023).
func TestRemoteSessions(t *testing.T) {
	bin := t.TempDir()
	list := filepath.Join(bin, "list.json")
	write := func(turn string) {
		t.Helper()
		b, _ := json.Marshal([]api.SessionView{{SessionID: "abc-1", Repo: "/srv/shop", Title: "deploy", Turn: turn, CanPrompt: true, TTY: "/dev/ttys001"},
			{SessionID: "mini/old", Repo: "mini:/x", Host: "mini"}})
		if err := os.WriteFile(list, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("idle")
	fake := filepath.Join(bin, "ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+bin+"/argv\ncat "+list+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := NewEnv(t, `remotes = ["mini"]`, "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+fake)
	e.Start()

	row := func() *api.SessionView {
		var ss []api.SessionView
		e.Call("session.list", nil, &ss)
		for i := range ss {
			if ss[i].Host == "mini" {
				return &ss[i]
			}
		}
		return nil
	}
	e.WaitFor("the remote row", 10*time.Second, func() bool { return row() != nil })
	var all []api.SessionView
	e.Call("session.list", nil, &all)
	for _, x := range all {
		if strings.Contains(x.SessionID, "mini/mini/") {
			t.Fatalf("a remote's remote row came back: %+v", x)
		}
	}
	v := row()
	if v.SessionID != "mini/abc-1" || v.Repo != "mini:/srv/shop" || v.CanPrompt || v.Why != "on mini: read only" || v.TTY != "" {
		t.Fatalf("row %+v", v)
	}
	argv, _ := os.ReadFile(filepath.Join(bin, "argv"))
	if !strings.Contains(string(argv), "BatchMode=yes") || !strings.Contains(string(argv), "--\nmini\n") {
		t.Fatalf("argv:\n%s", argv)
	}
	c, err := e.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("session.prompt", api.SessionPromptParams{SessionID: v.SessionID, Text: "yes"}, nil, 5*time.Second); err == nil {
		t.Fatal("typed into a remote session")
	}
	doctor, _ := e.CLI("", "doctor")
	if !strings.Contains(doctor, "remote mini") {
		t.Fatalf("doctor:\n%s", doctor)
	}
	if _, err := e.CLI("", "remote", "name", "nobody", "x"); err == nil {
		t.Fatal("named a host that is not in the list")
	}
	if out, err := e.CLI("", "remote", "name", "mini", "tower"); err != nil {
		t.Fatalf("name: %v\n%s", err, out)
	}
	e.WaitFor("the name in the row", 20*time.Second, func() bool {
		r := row()
		return r != nil && r.HostName == "tower" && r.Repo == "tower:/srv/shop" && r.Why == "on tower: read only" && r.Host == "mini"
	})
	if _, err := e.CLI("", "remote", "name", "mini"); err != nil {
		t.Fatal(err)
	}
	e.WaitFor("the address back", 20*time.Second, func() bool {
		r := row()
		return r != nil && r.HostName == "" && r.Repo == "mini:/srv/shop"
	})

	cfg, _ := os.ReadFile(e.Config())
	bad := filepath.Join(bin, "bad.toml")
	os.WriteFile(bad, []byte(strings.Replace(string(cfg), `remotes = ["mini"]`, `remotes = ["-oProxyCommand=x"]`, 1)), 0o600)
	if out, err := e.CLI("", "config", "validate", "--config", bad); err == nil {
		t.Fatalf("validated an ssh option as a host: %s", out)
	}
}
