// Package supervisor runs handler processes in their own process group so a
// timeout or cancel kills the whole tree with kill(-pgid) (RFC 4.2, 6.7).
package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Spec describes one process.
type Spec struct {
	Argv      []string
	Dir       string
	Env       []string
	Stdin     []byte
	Timeout   time.Duration
	MaxOutput int64
	// OnStart is called with pid and pgid right after start.
	OnStart func(pid, pgid int)
	// OnLine receives every stdout line (without the newline).
	OnLine func([]byte)
	// IdleGrace: after a machine sleep, a run past its deadline is killed
	// only if it produced no output in the last IdleGrace (RFC 8.4).
	IdleGrace time.Duration
}

// Outcome is how the process ended.
type Outcome struct {
	ExitCode       int
	TimedOut       bool
	OutputExceeded bool
	Cancelled      bool
	Stdout         []byte // only kept when OnLine is nil
	Stderr         string
	Err            error
	Duration       time.Duration
}

// Run starts the process and waits for it.
func Run(ctx context.Context, s Spec) Outcome {
	start := time.Now()
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = bytes.NewReader(s.Stdin)
	cmd.WaitDelay = 3 * time.Second
	var stderr limitedBuffer
	stderr.max = 256 << 10
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{Err: err, ExitCode: -1}
	}
	if err := cmd.Start(); err != nil {
		return Outcome{Err: err, ExitCode: -1}
	}
	pgid := cmd.Process.Pid
	if s.OnStart != nil {
		s.OnStart(cmd.Process.Pid, pgid)
	}

	var (
		total      atomic.Int64
		lastOutput atomic.Int64
		exceeded   atomic.Bool
		kept       bytes.Buffer
		readDone   = make(chan struct{})
	)
	lastOutput.Store(time.Now().UnixNano())
	go func() {
		defer close(readDone)
		r := bufio.NewReaderSize(stdout, 256<<10)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				lastOutput.Store(time.Now().UnixNano())
				if s.MaxOutput > 0 && total.Add(int64(len(line))) > s.MaxOutput {
					exceeded.Store(true)
					KillGroup(pgid)
					io.Copy(io.Discard, r)
					return
				}
				line = bytes.TrimRight(line, "\r\n")
				if s.OnLine != nil {
					s.OnLine(line)
				} else {
					kept.Write(line)
					kept.WriteByte('\n')
				}
			}
			if err != nil {
				return
			}
		}
	}()

	waitDone := make(chan error, 1)
	go func() {
		<-readDone
		waitDone <- cmd.Wait()
	}()

	var out Outcome
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	wallStart := start.Round(0) // wall clock, keeps counting through sleep
	grace := s.IdleGrace
	if grace == 0 {
		grace = 60 * time.Second
	}
loop:
	for {
		select {
		case err := <-waitDone:
			out.Err = err
			break loop
		case <-ctx.Done():
			out.Cancelled = true
			KillGroup(pgid)
			out.Err = <-waitDone
			break loop
		case <-ticker.C:
			if s.Timeout <= 0 {
				continue
			}
			mono := time.Since(start)
			if mono < s.Timeout {
				continue
			}
			wall := time.Now().Round(0).Sub(wallStart)
			slept := wall-mono > grace
			idle := time.Since(time.Unix(0, lastOutput.Load()))
			if slept && idle < grace {
				continue // woke from sleep and still producing output
			}
			out.TimedOut = true
			KillGroup(pgid)
			out.Err = <-waitDone
			break loop
		}
	}
	// Whatever the reason, make sure nothing in the group survives.
	KillGroup(pgid)
	out.Duration = time.Since(start)
	out.OutputExceeded = exceeded.Load()
	out.Stderr = stderr.String()
	if s.OnLine == nil {
		out.Stdout = kept.Bytes()
	}
	out.ExitCode = cmd.ProcessState.ExitCode()
	var ee *exec.ExitError
	if errors.As(out.Err, &ee) {
		out.Err = nil // a non-zero exit is reported through ExitCode
	}
	return out
}

// KillGroup sends SIGTERM then SIGKILL to the process group.
func KillGroup(pgid int) {
	if pgid <= 1 {
		return
	}
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !GroupAlive(pgid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(-pgid, syscall.SIGKILL)
}

// GroupAlive reports whether any process in the group still exists.
func GroupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < b.max {
		room := b.max - b.buf.Len()
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
