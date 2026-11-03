package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ensureDaemon starts the daemon in the background when none answers, so
// `mailroom` and `mailroom sessions` work with no setup (no init, no
// service). The started daemon runs until a reboot; `mailroom install`
// makes it a service. MAILROOM_NO_AUTOSTART=1 turns it
// off. One daemon per data dir: a second one stops on daemon.lock.
func ensureDaemon() {
	if os.Getenv("MAILROOM_NO_AUTOSTART") != "" {
		return
	}
	if c, err := humanClient(300 * time.Millisecond); err == nil {
		c.Close()
		return
	}
	cmd := exec.Command(absBinary(), "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "mailroom: cannot start the daemon:", err)
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c, err := humanClient(300 * time.Millisecond); err == nil {
			c.Close()
			fmt.Fprintf(os.Stderr, "mailroom: started the daemon (pid %d); it runs until a reboot, `mailroom install` makes it a service\n", pid)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "mailroom: started the daemon, but it does not answer yet; `mailroom doctor` says why")
}
