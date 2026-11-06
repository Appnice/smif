package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// remoteStub is an ssh and an ssh-copy-id that behave like a machine with no
// key and no smif yet. The files keyed and installed are its state.
func remoteStub(t *testing.T) (ssh, copyID, dir string) {
	t.Helper()
	dir = t.TempDir()
	ssh, copyID = filepath.Join(dir, "ssh"), filepath.Join(dir, "ssh-copy-id")
	script := `#!/bin/sh
for last; do :; done
[ -e ` + dir + `/down ] && { echo 'ssh: connect to host mini port 22: Connection refused' >&2; exit 255; }
[ -e ` + dir + `/keyed ] || { echo 'me@mini: Permission denied (publickey,password).' >&2; exit 255; }
case "$last" in
  true) exit 0 ;;
  curl*) touch ` + dir + `/installed; echo 'Installed smif'; exit 0 ;;
  *smif\ sessions*) [ -e ` + dir + `/installed ] || exit 127
    echo '[{"session_id":"abc","repo":"/srv/shop"}]' ;;
esac
`
	if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyID, []byte("#!/bin/sh\necho \"$1\" > "+dir+"/copied\ntouch "+dir+"/keyed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return ssh, copyID, dir
}

// From no key and no smif to rows in the list, with one yes; a no changes
// nothing; a machine that is off gets the Remote Login hint; a second add
// leaves the config alone (ADR-0023).
func TestRemoteAdd(t *testing.T) {
	ssh, copyID, dir := remoteStub(t)
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+ssh, "SMIF_SSH_COPY_ID="+copyID)
	e.Start()
	config := func() string {
		b, _ := os.ReadFile(e.Config())
		return string(b)
	}
	before := config()

	if _, err := e.CLI("", "remote", "add", "-oProxyCommand=x"); err == nil {
		t.Fatal("took an ssh option as a host")
	}
	if _, err := os.Stat(filepath.Join(dir, "copied")); err == nil {
		t.Fatal("reached ssh with a bad host")
	}

	os.WriteFile(filepath.Join(dir, "down"), nil, 0o600)
	_, err := e.CLI("", "remote", "add", "me@mini")
	if err == nil || !strings.Contains(err.Error(), "Remote Login") {
		t.Fatalf("machine off: %v", err)
	}
	os.Remove(filepath.Join(dir, "down"))

	_, err = e.CLI("n\n", "remote", "add", "me@mini")
	if err == nil || !strings.Contains(err.Error(), "nothing changed") || config() != before {
		t.Fatalf("no: %v\n%s", err, config())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "copied")); strings.TrimSpace(string(b)) != "me@mini" {
		t.Fatalf("ssh-copy-id got %q", b)
	}

	out, err := e.CLI("y\n", "remote", "add", "me@mini")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "with 1 sessions") || !strings.Contains(out, "Done:") {
		t.Fatalf("out:\n%s", out)
	}
	if !strings.Contains(config(), `remotes = ["me@mini"]`) || !strings.Contains(config(), "[daemon]") {
		t.Fatalf("config:\n%s", config())
	}
	doctor, _ := e.CLI("", "doctor")
	if !strings.Contains(doctor, "remote me@mini") {
		t.Fatalf("doctor:\n%s", doctor)
	}

	saved := config()
	out, err = e.CLI("", "remote", "add", "me@mini")
	if err != nil || !strings.Contains(out, "already in daemon.remotes") || config() != saved {
		t.Fatalf("second add: %v\n%s", err, out)
	}
}

// A login that works in this shell and not for the daemon is the one failure
// the first checks cannot see, so the command says so.
func TestRemoteAddDaemonCannotReach(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the daemon")
	}
	ssh, copyID, dir := remoteStub(t)
	os.WriteFile(filepath.Join(dir, "keyed"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "installed"), nil, 0o600)
	blocked := filepath.Join(dir, "blocked")
	os.WriteFile(blocked, []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0o700)
	e := NewEnv(t, "", "", AgentSpec{ID: "app", Rules: digestRule()})
	e.env = append(e.env, "SMIF_SSH="+blocked)
	e.Start()
	start := time.Now()
	_, stderr, err := e.CLIDir(e.Root, "", []string{"SMIF_SSH=" + ssh, "SMIF_SSH_COPY_ID=" + copyID}, "remote", "add", "mini")
	if err == nil || !strings.Contains(stderr, "the daemon cannot reach it") || !strings.Contains(stderr, "Permission denied") {
		t.Fatalf("err %v, stderr:\n%s", err, stderr)
	}
	if time.Since(start) > 2*time.Minute {
		t.Fatalf("took %s", time.Since(start))
	}
}
