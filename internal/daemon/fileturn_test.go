package daemon

import "testing"

func TestFileTurnShellIsBusy(t *testing.T) {
	for in, want := range map[string]string{"shell": "busy", "busy": "busy", "idle": "idle", "waiting": "waiting"} {
		if got := fileTurn(in); got != want {
			t.Errorf("fileTurn(%q) = %q, want %q", in, got, want)
		}
	}
}
