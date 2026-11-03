package command

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProgram(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct{ in, want string }{
		{"~/.config/smif/handlers/status.sh", filepath.Join(home, ".config/smif/handlers/status.sh")},
		{"./scripts/smif/status.sh", "/repo/scripts/smif/status.sh"},
		{"scripts/x.sh", "/repo/scripts/x.sh"},
		{"/usr/local/bin/x", "/usr/local/bin/x"},
		{"jq", "jq"},
	} {
		if got := resolveProgram(c.in, "/repo"); got != c.want {
			t.Errorf("resolveProgram(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
