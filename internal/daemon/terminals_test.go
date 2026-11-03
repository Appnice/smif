package daemon

import (
	"slices"
	"testing"

	"github.com/appnice/mailroom/internal/store"
)

func TestTerminalSupport(t *testing.T) {
	for term, ok := range map[string]bool{"Apple_Terminal": true, "iTerm.app": true, "tmux": true, "WezTerm": false, "": false} {
		if got := termSupport(store.Presence{Term: term}) == ""; got != ok {
			t.Errorf("%q supported = %v, want %v", term, got, ok)
		}
	}
}

func TestTmuxPaneAndKeys(t *testing.T) {
	listing := "/dev/ttys001 %0\n/dev/ttys004 %3\n\n/dev/ttys009 %7\n"
	if got := paneForTTY(listing, "/dev/ttys004"); got != "%3" {
		t.Fatalf("pane %q", got)
	}
	if got := paneForTTY(listing, "/dev/ttys099"); got != "" {
		t.Fatalf("missing tty gave %q", got)
	}
	// The text goes as literal keys after --, so a leading "-" or a key
	// name in it is typed, not read as a flag or a key; Enter goes apart.
	calls := tmuxTypeArgs("%3", "-n Enter run the tests")
	if len(calls) != 2 || !slices.Equal(calls[0], []string{"send-keys", "-t", "%3", "-l", "--", "-n Enter run the tests"}) ||
		!slices.Equal(calls[1], []string{"send-keys", "-t", "%3", "Enter"}) {
		t.Fatalf("send-keys calls %q", calls)
	}
}

// A new session's command line for Terminal.app and iTerm2: every word
// quoted for sh, so a folder or name with spaces or quotes stays one word.
func TestShellQuote(t *testing.T) {
	if got := "cd " + shellQuote("/Users/a b/it's") + " && " + shellJoin([]string{"/opt/claude", "--name", "api work"}); got !=
		`cd '/Users/a b/it'\''s' && '/opt/claude' '--name' 'api work'` {
		t.Fatalf("line: %s", got)
	}
}
