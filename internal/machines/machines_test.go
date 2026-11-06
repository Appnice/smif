package machines

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBrowse(t *testing.T) {
	out := `Browsing for _ssh._tcp.local.
DATE: ---Fri 06 Nov 2026---
12:00:00.000  ...STARTING...
Timestamp     A/R    Flags  if Domain               Service Type         Instance Name
12:00:00.123  Add        3   6 local.               _ssh._tcp.           mini
12:00:00.200  Add        2   6 local.               _ssh._tcp.           Sam's MacBook Pro
12:00:01.000  Rmv        2   6 local.               _ssh._tcp.           gone
`
	rows := parseBrowse(out)
	if len(rows) != 2 || rows[0].Target != "mini.local" || rows[1].Target != "Sam's-MacBook-Pro.local" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestParseAvahi(t *testing.T) {
	out := "+;eth0;IPv4;box;_ssh._tcp;local\n" +
		"=;eth0;IPv4;box;_ssh._tcp;local;box.local;192.168.1.5;22;\n" +
		"=;eth0;IPv6;box;_ssh._tcp;local;box.local;fe80::1;22;\n"
	rows := parseAvahi(out)
	if len(rows) != 1 || rows[0].Target != "box.local" || rows[0].addr != "192.168.1.5:22" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestParseTailscale(t *testing.T) {
	rows := parseTailscale(`{"Self":{"HostName":"me"},"Peer":{"k1":{"HostName":"nas","DNSName":"nas.tail1.ts.net."}}}`)
	if len(rows) != 1 || rows[0].Name != "nas" || rows[0].Target != "nas.tail1.ts.net" {
		t.Fatalf("rows %+v", rows)
	}
	if parseTailscale("not json") != nil {
		t.Fatal("rows from junk")
	}
}

func TestHostAliases(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".ssh", "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".ssh", "config"), []byte("# a comment\nHost mini devbox\n  User me\nHost *.corp !x\nHost=eq\nInclude conf.d/*\nInclude ~/.colima/ssh_config\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".ssh", "conf.d", "more"), []byte("host nas\n"), 0o600)
	os.MkdirAll(filepath.Join(dir, ".colima"), 0o700)
	os.WriteFile(filepath.Join(dir, ".colima", "ssh_config"), []byte("Host colima\n"), 0o600)
	got := strings.Join(hostAliases(filepath.Join(dir, ".ssh", "config"), 0), " ")
	if got != "mini devbox eq nas" {
		t.Fatalf("got %q", got)
	}
}

// git hosts leave, a machine that answers is open, and a name you added or
// this machine's own name stays out.
func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	self, _ := os.Hostname()
	os.WriteFile(filepath.Join(dir, ".ssh", "config"), []byte("Host up down github.com added "+shortName(self)+"\n"), 0o600)
	old := runner
	defer func() { runner = old }()
	runner = func(_ context.Context, name string, args ...string) string {
		if len(args) == 0 || args[0] != "-G" {
			return ""
		}
		switch args[len(args)-1] {
		case "up":
			return "hostname 127.0.0.1\nport " + port + "\nuser me\n"
		case "github.com":
			return "hostname github.com\nport 22\nuser git\n"
		}
		return "hostname 127.0.0.1\nport 1\nuser me\n"
	}
	rows := Discover(context.Background(), map[string]bool{"added": true})
	if len(rows) != 2 || rows[0].Name != "up" || !rows[0].Open || rows[1].Name != "down" || rows[1].Open {
		t.Fatalf("rows %+v", rows)
	}
}

func TestClassify(t *testing.T) {
	var denied *errDenied
	if err := classify("me@x: Permission denied (publickey,password).\n", errors.New("exit 255")); !errors.As(err, &denied) {
		t.Fatalf("denied: %v", err)
	}
	for _, s := range []string{"ssh: connect to host x port 22: Connection refused", "ssh: connect to host x port 22: Operation timed out", "kex_exchange_identification: read: Connection reset by peer"} {
		if err := classify(s, errors.New("x")); !isAway(err) {
			t.Fatalf("%s: %v", s, err)
		}
	}
	err := classify("ssh: Could not resolve hostname nope: nodename nor servname provided", errors.New("x"))
	if isAway(err) || !strings.Contains(err.Error(), "no machine has that name") {
		t.Fatalf("resolve: %v", err)
	}
	if err := classify("Host key verification failed.", errors.New("x")); isAway(err) || !strings.Contains(err.Error(), "ssh-keygen -R") {
		t.Fatalf("host key: %v", err)
	}
}

func TestIsThisMachine(t *testing.T) {
	old := runner
	defer func() { runner = old }()
	runner = func(context.Context, string, ...string) string { return "" }
	ctx := context.Background()
	if isThisMachine(ctx, "me@127.0.0.1") || isThisMachine(ctx, "me@192.0.2.1") {
		t.Fatal("loopback or another address read as this machine")
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			if !isThisMachine(ctx, "me@"+n.IP.String()) {
				t.Fatalf("%s is this machine", n.IP)
			}
			return
		}
	}
	t.Skip("no address but loopback")
}

func TestKeyLine(t *testing.T) {
	if got := keyLine("/x/smif", "k", true); got != `restrict,command="PATH=/x:$PATH /x/smif remote-read --write" k` {
		t.Fatalf("write: %s", got)
	}
	got := keyLine("/home/me/.local/bin/smif", "ssh-ed25519 AAAA smif-daemon", false)
	want := `restrict,command="PATH=/home/me/.local/bin:$PATH /home/me/.local/bin/smif remote-read" ssh-ed25519 AAAA smif-daemon`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

// The password reaches the helper over the socket, and only there.
func TestServePassword(t *testing.T) {
	env, stop, err := servePassword("pa ss")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	sock := ""
	for _, e := range env {
		if strings.Contains(e, "pa ss") {
			t.Fatalf("the password is in the environment: %v", env)
		}
		if v, ok := strings.CutPrefix(e, AskpassEnv+"="); ok {
			sock = v
		}
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := make([]byte, 16)
	n, _ := c.Read(b)
	if string(b[:n]) != "pa ss" {
		t.Fatalf("got %q", b[:n])
	}
	st, err := os.Stat(filepath.Dir(sock))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", st.Mode(), err)
	}
}
