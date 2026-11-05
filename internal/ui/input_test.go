package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The box breaks long text at spaces over several lines, keeps the cursor
// line in view, and starts a line at a new line in the text.
func TestInputWrapView(t *testing.T) {
	in := lineInput{}
	in.insert([]rune("rt - sa jakies issues w sentry + dzisiaj jak gralem raid i pozniej chcialem zagrac normalna rozgrywke"))
	ls := in.wrapView(30, 0)
	if len(ls) < 4 {
		t.Fatalf("not wrapped: %q", ls)
	}
	for _, l := range ls {
		if w := ansi.StringWidth(ansi.Strip(l)); w > 30 {
			t.Fatalf("line %q is %d wide", ansi.Strip(l), w)
		}
	}
	joined := strings.Join(func() []string {
		var out []string
		for _, l := range ls {
			out = append(out, strings.TrimRight(ansi.Strip(l), " "))
		}
		return out
	}(), " ")
	if joined != string(in.r) {
		t.Fatalf("words broken or lost:\n%q\n%q", joined, string(in.r))
	}
	// Only maxLines show, the cursor's (the last) among them.
	if got := in.wrapView(30, 2); len(got) != 2 || !strings.Contains(ansi.Strip(got[1]), "rozgrywke") {
		t.Fatalf("window: %q", got)
	}
	in.pos = 0
	if got := in.wrapView(30, 2); !strings.HasPrefix(ansi.Strip(got[0]), "rt - sa") {
		t.Fatalf("window at the start: %q", got)
	}
	nl := lineInput{}
	nl.insert([]rune("one\ntwo"))
	if got := nl.wrapView(30, 0); len(got) != 2 || ansi.Strip(got[1]) != "two " {
		t.Fatalf("new line: %q", got)
	}
	if got := in.boxLines("› ", 32, 0); !strings.HasPrefix(got[0], "› ") || !strings.HasPrefix(got[1], "  ") {
		t.Fatalf("box lines: %q", got[:2])
	}
}

// A Markdown table draws as fitted columns: no pipes or dashes from the
// source, the header with a rule, long cells wrapped inside their column.
func TestMarkdownTable(t *testing.T) {
	text := "Intro line.\n\n| Session | When | What |\n|---|---|---|\n| `3ecfe77e` (134) | 12.10 → 03.11 | **OTA app-config + Digibot** in the panel. Design from the artifact, BE, tablets, SSE, tests. |\n| `d129ca3f` (45) | 29.09 | Waste AI. |\n\nAfter."
	ls := mdLines(text, 60)
	plain := make([]string, len(ls))
	for i, l := range ls {
		plain[i] = ansi.Strip(l)
		if w := ansi.StringWidth(plain[i]); w > 60 {
			t.Fatalf("line %q is %d wide", plain[i], w)
		}
		if strings.Contains(plain[i], "|") || strings.Contains(plain[i], "---") || strings.Contains(plain[i], "**") {
			t.Fatalf("raw Markdown in %q", plain[i])
		}
	}
	all := strings.Join(plain, "\n")
	for _, want := range []string{"Session", "│ When", "─┼─", "3ecfe77e (134)", "OTA app-config + Digibot", "Waste AI.", "After."} {
		if !strings.Contains(all, want) {
			t.Fatalf("table lacks %q:\n%s", want, all)
		}
	}
	if !strings.Contains(all, "Intro line.") || strings.Count(all, "\n") < 8 {
		t.Fatalf("long cell not wrapped:\n%s", all)
	}
	t.Log("\n" + all)
}

// A long line in a code block is usually a command to copy: nothing may be cut.
func TestMdCodeLineIsNotCut(t *testing.T) {
	line := `! gh repo create appnice/appnice --public --description "profile" --disable-wiki && git -C ~/repos/appnice remote add origin https://github.com/appnice/appnice.git && git push -u origin main`
	ls := mdLines("Run this:\n\n```\n"+line+"\n```\n", 60)
	var got strings.Builder
	for _, l := range ls[2:] {
		if w := ansi.StringWidth(l); w > 60 {
			t.Fatalf("line is %d wide, the pane is 60: %q", w, l)
		}
		got.WriteString(ansi.Strip(l))
	}
	if got.String() != line {
		t.Fatalf("the command changed:\n got %q\nwant %q", got.String(), line)
	}
}
