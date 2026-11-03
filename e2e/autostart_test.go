package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// AUTOSTART: with no config and no service, `smif sessions` starts the
// daemon in the background and lists the sessions; the next call finds it
// running. A second daemon on the same data dir stops on daemon.lock.
func TestAutostart(t *testing.T) {
	e := NewEnv(t, "", "")
	if err := os.Remove(filepath.Join(e.Root, "cfg", "config.toml")); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range e.env {
		if !strings.HasPrefix(kv, "SMIF_NO_AUTOSTART=") {
			env = append(env, kv)
		}
	}
	e.env = env
	_, stderr, err := e.CLIDir(e.Root, "", nil, "sessions")
	m := regexp.MustCompile(`started the daemon \(pid (\d+)\)`).FindStringSubmatch(stderr)
	if m != nil {
		pid, _ := strconv.Atoi(m[1])
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM); time.Sleep(200 * time.Millisecond) })
	}
	if err != nil || m == nil {
		t.Fatalf("sessions with no setup: %v\n%s", err, stderr)
	}
	out, stderr, err := e.CLIDir(e.Root, "", nil, "sessions")
	if err != nil || strings.Contains(stderr, "started the daemon") {
		t.Fatalf("second call: %v %q %q", err, out, stderr)
	}
	_, stderr, err = e.CLIDir(e.Root, "", []string{"SMIF_LOG_STDERR=1"}, "daemon")
	if err == nil || !strings.Contains(stderr, "another daemon runs with the data dir") {
		t.Fatalf("a second daemon started: %v %q", err, stderr)
	}
}
