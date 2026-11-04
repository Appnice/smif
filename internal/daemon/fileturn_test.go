package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestFileTurnShellSaysNothing(t *testing.T) {
	for in, want := range map[string]string{"shell": "", "busy": "busy", "idle": "idle", "waiting": "waiting"} {
		if got := fileTurn(in); got != want {
			t.Errorf("fileTurn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnterDelayGrowsWithText(t *testing.T) {
	short, long := enterDelay("jak tam raport z dzisiaj"), enterDelay(strings.Repeat("x", 92))
	if short < 500*time.Millisecond || long <= short || enterDelay(strings.Repeat("x", 5000)) != 2*time.Second {
		t.Errorf("enterDelay: short %v, long %v", short, long)
	}
	if delayMS(strings.Repeat("x", 92)) != "1236" {
		t.Errorf("delayMS = %s", delayMS(strings.Repeat("x", 92)))
	}
}
