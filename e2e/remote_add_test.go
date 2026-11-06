package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

// remoteStub is an ssh that behaves like a machine with no smif, a password
// and no key for us. It asks the real askpass helper, and the daemon gets in
// only with a line in authorized that restricts the key. The files in dir are
// its state: down, keyed, installed, master, lingered, authorized.
func remoteStub(t *testing.T) (ssh, dir string) {
	t.Helper()
	dir = t.TempDir()
	ssh = filepath.Join(dir, "ssh")
	script := `#!/bin/sh
d=` + dir + `
for last; do :; done
case " $* " in
  *smif-setup-*" -O exit "*) rm -f $d/master; exit 0 ;;
  *" -O exit "*) touch $d/dropped; exit 0 ;;
esac
[ -e $d/down ] && { echo 'ssh: connect to host mini port 22: Connection refused' >&2; exit 255; }
case " $* " in
  *" -N "*)
    [ -e $d/keyed ] && { touch $d/master; exit 0; }
    if [ -n "$SSH_ASKPASS" ]; then
      [ "$("$SSH_ASKPASS" 'password:')" = secret ] && { touch $d/master; exit 0; }
    fi
    echo 'me@mini: Permission denied (publickey,password).' >&2; exit 255 ;;
esac
case " $* " in
  *" ControlMaster=no "*) [ -e $d/master ] || { echo 'no master' >&2; exit 255; } ;;
  *) grep -q 'restrict,command=' $d/authorized 2>/dev/null && case " $* " in *" -i "*) ;; *) false ;; esac || {
       echo 'me@mini: Permission denied (publickey).' >&2; exit 255; }
     case "$last" in
       *'remote-read send'*|*'remote-read queue'*)
         grep -q 'remote-read --write' $d/authorized || { echo 'smif: writing is off for this machine' >&2; exit 1; }
         cat > $d/typed; echo '{"mode":"typed","agent":"shop","session_id":"x"}' ;;
       *'remote-read tail'*) echo '{"session_id":"x","turns":[{"role":"claude","text":"answer from there","at":"2026-11-06T10:00:00Z"}]}' ;;
       *) echo '[{"session_id":"11111111-2222-4333-8444-555555555555","repo":"/srv/shop","can_prompt":true,"turn":"idle"}]' ;;
     esac
     exit 0 ;;
esac
case "$last" in
  uname*) printf 'Linux\nx86_64\nUbuntu 24.04\n' ;;
  *'remote-read check') [ -e $d/installed ] ;;
  *'command -v smif'*) [ -e $d/installed ] && echo /home/me/.local/bin/smif; true ;;
  curl*) touch $d/installed; echo installed ;;
  *'sessions --json') echo '[{"session_id":"abc","repo":"/srv/shop"}]' ;;
  *'k=$(cat)'*) cat > $d/authorized ;;
  *'sudo -S'*) [ "$(cat)" = rootpw ] && touch $d/lingered ;;
  *loginctl*) if [ -e $d/lingered ]; then echo yes; else echo no; fi ;;
  *) echo "unknown command: $last" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return ssh, dir
}

// From no key and no smif to a row in the list: a wrong password, then the
// right one, the install, sudo for linger and the daemon's own key. A machine
// with ssh off waits and goes on. A second add changes nothing (ADR-0024).
func TestRemoteAdd(t *testing.T) {
	ssh, dir := remoteStub(t)
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+ssh)
	e.Start()
	config := func() string {
		b, _ := os.ReadFile(e.Config())
		return string(b)
	}
	if _, err := e.CLI("", "remote", "add", "-oProxyCommand=x"); err == nil {
		t.Fatal("took an ssh option as a host")
	}

	os.WriteFile(filepath.Join(dir, "down"), nil, 0o600)
	time.AfterFunc(time.Second, func() { os.Remove(filepath.Join(dir, "down")) })
	out, err := e.CLI("wrong\nsecret\ny\nbad\nrootpw\n", "remote", "add", "me@mini")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	for _, want := range []string{"Run this once", "did not work", "Ubuntu 24.04", "installed", "sudo said no", "Done:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "rootpw") {
		t.Fatalf("a password in the output:\n%s", out)
	}
	if !strings.Contains(config(), `remotes = ["me@mini"]`) {
		t.Fatalf("config:\n%s", config())
	}
	line, _ := os.ReadFile(filepath.Join(dir, "authorized"))
	if !strings.HasPrefix(string(line), `restrict,command="PATH=/home/me/.local/bin:$PATH /home/me/.local/bin/smif remote-read" ssh-ed25519 `) ||
		!strings.HasSuffix(strings.TrimSpace(string(line)), "smif-daemon") {
		t.Fatalf("authorized:\n%s", line)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".ssh", "smif_ed25519")); err != nil {
		t.Fatal(err)
	}
	doctor, _ := e.CLI("", "doctor")
	if !strings.Contains(doctor, "remote me@mini") {
		t.Fatalf("doctor:\n%s", doctor)
	}
	var tail api.SessionTail
	e.Call("session.tail", api.SessionTailParams{SessionID: "me@mini/11111111-2222-4333-8444-555555555555"}, &tail)
	if len(tail.Turns) != 1 || tail.Turns[0].Text != "answer from there" {
		t.Fatalf("tail %+v", tail)
	}

	id := "me@mini/11111111-2222-4333-8444-555555555555"
	send := func() error {
		c, err := e.Client()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.Call("session.send", api.SessionSendParams{SessionID: id, Text: "go on", Async: true}, &api.SessionSendResult{}, 10*time.Second)
	}
	if err := send(); err == nil || !strings.Contains(err.Error(), "read only") {
		t.Fatalf("read only machine: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "keyed"), nil, 0o600)
	if out, err := e.CLI("", "remote", "write", "me@mini", "on"); err != nil {
		t.Fatalf("write on: %v\n%s", err, out)
	}
	if line, _ = os.ReadFile(filepath.Join(dir, "authorized")); !strings.Contains(string(line), "remote-read --write") {
		t.Fatalf("authorized:\n%s", line)
	}
	// sshd reads a key's line at login, so the daemon's open link must go.
	if _, err := os.Stat(filepath.Join(dir, "dropped")); err != nil {
		t.Fatal("the daemon's connection stayed open across a change of rights")
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "typed")); string(b) != "go on" {
		t.Fatalf("typed %q", b)
	}
	if out, err := e.CLI("", "remote", "write", "me@mini", "off"); err != nil || strings.Contains(config(), "remote_write = [\"me@mini\"]") {
		t.Fatalf("write off: %v\n%s\n%s", err, out, config())
	}
	if err := send(); err == nil {
		t.Fatal("wrote to a read-only machine")
	}

	saved := config()
	out, err = e.CLI("", "remote", "add", "me@mini")
	if err != nil || !strings.Contains(out, "already there") || config() != saved {
		t.Fatalf("second add: %v\n%s", err, out)
	}

	out, err = e.CLI("", "remote", "rm", "me@mini")
	if err != nil || !strings.Contains(out, "left your list") || strings.Contains(config(), "me@mini") {
		t.Fatalf("rm: %v\n%s\n%s", err, out, config())
	}
	if _, err := e.CLI("", "remote", "rm", "me@mini"); err == nil {
		t.Fatal("removed a host twice")
	}
}

// A no to the install adds nothing.
func TestRemoteAddNo(t *testing.T) {
	ssh, dir := remoteStub(t)
	os.WriteFile(filepath.Join(dir, "keyed"), nil, 0o600)
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+ssh)
	e.Start()
	before, _ := os.ReadFile(e.Config())
	out, err := e.CLI("n\n", "remote", "add", "mini")
	if err == nil || !strings.Contains(err.Error(), "nothing was added") {
		t.Fatalf("no: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(e.Config())
	if string(before) != string(after) {
		t.Fatalf("config changed:\n%s", after)
	}
}

// The setup works, and the daemon still cannot get in: the command says so.
func TestRemoteAddDaemonCannotReach(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the daemon")
	}
	ssh, dir := remoteStub(t)
	for _, f := range []string{"keyed", "installed", "lingered"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o600)
	}
	blocked := filepath.Join(dir, "blocked")
	os.WriteFile(blocked, []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0o700)
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+blocked)
	e.Start()
	_, stderr, err := e.CLIDir(e.Root, "", []string{"SMIF_SSH=" + ssh}, "remote", "add", "mini")
	if err == nil || !strings.Contains(stderr, "daemon cannot reach it") || !strings.Contains(stderr, "Permission denied") {
		t.Fatalf("err %v, stderr:\n%s", err, stderr)
	}
}
