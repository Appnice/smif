//go:build darwin || linux

package e2e

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// lockedBuffer collects the terminal output while the reader goroutine
// writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// runInSilentPTY starts the smif binary on a new pseudo terminal (its
// controlling terminal, 80x24, TERM=xterm-256color). Nothing answers the
// terminal queries, as under macOS `script` or a slow ssh link. After
// typeAfter it types keys (if not empty). It returns the terminal output,
// the time from start to exit and the exit error; limit stops the program.
func runInSilentPTY(t *testing.T, env []string, keys string, typeAfter, limit time.Duration, args ...string) (string, time.Duration, error) {
	t.Helper()
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo terminal: %v", err)
	}
	defer master.Close()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatalf("set window size: %v", err)
	}
	cmd := exec.Command(smifBin, args...)
	// A -race binary sleeps 1 s at exit (GORACE atexit_sleep_ms, default
	// 1000); turn that off so the time is the program's own.
	cmd.Env = append(append([]string{}, env...), "TERM=xterm-256color", "GORACE=atexit_sleep_ms=0")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	var out lockedBuffer
	readDone := make(chan struct{})
	go func() { // drain the master so that the program never blocks on a write
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close() // only the child holds the slave now
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var typed <-chan time.Time
	if keys != "" {
		typed = time.After(typeAfter)
	}
	deadline := time.After(limit)
	for {
		select {
		case <-typed:
			if _, err := master.Write([]byte(keys)); err != nil {
				t.Fatalf("type %q: %v", keys, err)
			}
			typed = nil
		case err := <-exited:
			took := time.Since(start)
			select { // the reader ends when the slave closes
			case <-readDone:
			case <-time.After(time.Second):
			}
			return out.String(), took, err
		case <-deadline:
			cmd.Process.Kill()
			<-exited
			return out.String(), time.Since(start), errors.New("killed: still running after the limit")
		}
	}
}

// UI-START: in a terminal that answers no queries (OSC 11, DSR), the UI must
// still start, take the keys typed early and quit fast. Bubble Tea v1 waited
// 5 s there and ate the keys (task #002); v2 does not query at start. This
// test runs the real binary in a silent PTY.
func TestStartupSendsNoQuery(t *testing.T) {
	e := NewEnv(t, "", "") // no daemon: the UI shows DAEMON DOWN
	// The first start of a new binary is slow on macOS (code signature
	// check, about 1 s for the -race build); keep it out of the timings.
	if _, err := e.CLI("", "--help"); err != nil {
		t.Fatal(err)
	}

	t.Run("ui quits on q typed after 500ms", func(t *testing.T) {
		out, took, err := runInSilentPTY(t, e.env, "q", 500*time.Millisecond, 6*time.Second, "ui")
		t.Logf("smif ui: exit %v after %v", err, took.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("smif ui did not exit 0 on q: %v\noutput: %q", err, out)
		}
		if took > 3*time.Second {
			t.Fatalf("smif ui took %v to quit on q, want under 3s", took)
		}
		if strings.Contains(out, "\x1b]11;?") || strings.Contains(out, "\x1b[6n") {
			t.Fatalf("smif ui queried the terminal: %q", out)
		}
		if !strings.Contains(out, "\x1b[?1049h") || !strings.Contains(out, "DAEMON DOWN") {
			t.Fatalf("smif ui did not draw the screen: %q", out)
		}
	})

	t.Run("plain smif opens the ui", func(t *testing.T) {
		out, took, err := runInSilentPTY(t, e.env, "q", 500*time.Millisecond, 6*time.Second)
		t.Logf("smif: exit %v after %v", err, took.Round(time.Millisecond))
		if err != nil || !strings.Contains(out, "\x1b[?1049h") || !strings.Contains(out, "DAEMON DOWN") {
			t.Fatalf("plain smif did not open the ui: %v\n%q", err, out)
		}
	})

	t.Run("any command is fast on a terminal", func(t *testing.T) {
		out, took, err := runInSilentPTY(t, e.env, "", 0, 6*time.Second, "--help")
		t.Logf("smif --help: exit %v after %v", err, took.Round(time.Millisecond))
		if err != nil {
			t.Fatalf("smif --help: %v\n%q", err, out)
		}
		if took > 2*time.Second || strings.Contains(out, "\x1b]11;?") {
			t.Fatalf("smif --help took %v (want under 2s), output %q", took, out)
		}
	})
}
