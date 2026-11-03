package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// sgrColor matches an SGR sequence that sets a foreground or background
// colour: 30-37, 38;..., 40-47, 48;..., 90-97, 100-107.
var sgrColor = regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?(?:3[0-8]|4[0-8]|9[0-7]|10[0-7])(?:;[0-9;]*)?m`)

// sgrAttr matches the reverse attribute (7) the cursor uses.
var sgrReverse = regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?7(?:;[0-9;]*)?m`)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// restoreColor puts back the colour styles after a test changed them.
func restoreColor(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { applyTheme(true) })
}

// screensForColor renders the overview with a dead letter and needs-you
// item (red and yellow), a transcript and a diff (green, red, cyan).
func screensForColor(t *testing.T) string {
	t.Helper()
	m, _ := newFullModel(t, sample(), 120, 40)
	out := m.Render()
	key(m, "tab", "enter", "2")
	out += m.Render()
	key(m, "3")
	return out + m.Render()
}

func TestNoColor(t *testing.T) {
	restoreColor(t)

	// Colours on: a 256-colour terminal without NO_COLOR.
	if off := SetupColor(env(nil)); off {
		t.Fatal("SetupColor reports colours off without NO_COLOR")
	}
	colour := screensForColor(t)
	if !sgrColor.MatchString(colour) {
		t.Fatalf("no colour sequence with colours on: %q", colour[:200])
	}

	// NO_COLOR=1: no colour sequence; bold and reverse stay, so the
	// cursor and the focused panel still show.
	if off := SetupColor(env(map[string]string{"NO_COLOR": "1"})); !off {
		t.Fatal("SetupColor ignores NO_COLOR=1")
	}
	mono := screensForColor(t)
	if loc := sgrColor.FindStringIndex(mono); loc != nil {
		t.Fatalf("colour sequence %q with NO_COLOR set", mono[loc[0]:loc[1]])
	}
	if !sgrReverse.MatchString(mono) {
		t.Fatal("NO_COLOR also dropped the reverse attribute of the cursor")
	}
	// The text is the same with and without colours.
	if ansi.Strip(mono) != ansi.Strip(colour) {
		t.Fatal("NO_COLOR changed the text of the screens")
	}

	// An empty NO_COLOR does not count (no-color.org).
	if off := SetupColor(env(map[string]string{"NO_COLOR": ""})); off {
		t.Fatal("an empty NO_COLOR turned colours off")
	}

	// A terminal without styles gets plain text: Bubble Tea writes through
	// a colorprofile writer, which drops every escape for such a terminal.
	// The focus mark, the cursor and the open tab still show as text.
	plainOf := func(screen string) string {
		var b strings.Builder
		w := &colorprofile.Writer{Forward: &b, Profile: colorprofile.NoTTY}
		w.WriteString(screen)
		return b.String()
	}
	m, _ := newFullModel(t, sample(), 120, 40)
	plain := plainOf(m.Render())
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("escape sequence on a terminal without styles: %q", plain)
	}
	for _, want := range []string{"▶ AGENTS (3)", "> cafe"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain view lacks %q", want)
		}
	}
	key(m, "tab", "enter")
	if v := plainOf(m.Render()); !strings.Contains(v, "[1 RECORD]") || strings.Contains(v, "\x1b[") {
		t.Errorf("plain run screen:\n%s", v)
	}
}
