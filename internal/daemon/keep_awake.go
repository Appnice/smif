package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/appnice/smif/internal/api"
)

// keepAwakeQuiet ends the hold for a session that wrote nothing for this
// long: a turn that hangs must not keep the Mac up all night.
const keepAwakeQuiet = 30 * time.Minute

// keepAwake holds the Mac awake while a session works (daemon.keep_awake).
// Only the watcher goroutine touches it.
type keepAwake struct {
	start func() (stop func())
	stop  func()
}

func (k *keepAwake) set(on bool) {
	switch {
	case on && k.stop == nil:
		k.stop = k.start()
	case !on && k.stop != nil:
		k.stop()
		k.stop = nil
	}
}

// sessionWorks says whether a session is busy and still writes.
func sessionWorks(v api.SessionView, now time.Time) bool {
	if v.Turn != "busy" {
		return false
	}
	return v.Activity == nil || v.Activity.WroteAt.IsZero() || now.Sub(v.Activity.WroteAt) < keepAwakeQuiet
}

// startCaffeinate holds off idle sleep. -w ends caffeinate with the daemon,
// also after a kill -9. nil on a system with no caffeinate.
func startCaffeinate() func() {
	if runtime.GOOS != "darwin" {
		return nil
	}
	c := exec.Command("caffeinate", "-i", "-w", fmt.Sprint(os.Getpid()))
	if c.Start() != nil {
		return nil
	}
	go func() { _ = c.Wait() }()
	return func() { _ = c.Process.Kill() }
}
