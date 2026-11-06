package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/appnice/smif/internal/api"
	"github.com/charmbracelet/x/ansi"
)

// partsModel is a chat with the idle session, with no real clipboard.
func partsModel(t *testing.T) (*Model, *fakePrompter, string) {
	t.Helper()
	m, f := newSessionModel(t)
	dir := filepath.Join(t.TempDir(), "parts")
	m.partsDir = dir
	m.clipRead = func() (string, error) { return "", nil }
	m.clipImage = func(string) error { return os.ErrNotExist }
	key(m, "j", "enter")
	return m, f, dir
}

func paste(m *Model, s string) {
	_, cmd := m.Update(tea.PasteMsg{Content: s})
	run(m, cmd)
}

func logLines(n int) string {
	ls := make([]string, n)
	for i := range ls {
		ls[i] = fmt.Sprintf("log line %d", i+1)
	}
	ls[0], ls[n-1] = "=== RUN TestStart", "FAIL at the very end"
	return strings.Join(ls, "\n") + "\n"
}

func writePNG(t *testing.T, path string, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// A long paste keeps its lines in a file. The box shows its start, its end
// and its size, and the prompt names the file. Claude Code took 501 and 3000
// lines this way, whole, with no tool call and no permission prompt.
func TestLongPasteBecomesAPart(t *testing.T) {
	m, f, dir := partsModel(t)
	text := logLines(412)
	m.clipRead = func() (string, error) { return text, nil }
	key(m, "why does this fail? ")
	paste(m, text)
	key(m, "look at the end")

	if got := m.home.draft.Value(); got != "why does this fail? [1] look at the end" {
		t.Fatalf("box %q", got)
	}
	out := view(m)
	for _, want := range []string{"[1] pasted text", "412 lines", "same as clipboard", "=== RUN TestStart", "FAIL at the very end", "406 lines not shown"} {
		if !strings.Contains(out, want) {
			t.Fatalf("box lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "log line 200") {
		t.Fatalf("the middle shows:\n%s", out)
	}
	p := m.home.part.parts[0]
	b, err := os.ReadFile(p.path)
	if err != nil || string(b) != text {
		t.Fatalf("file %q %v", b, err)
	}
	if info, _ := os.Stat(p.path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v: a paste can hold a secret", info.Mode())
	}
	if filepath.Dir(p.path) != dir || strings.Contains(p.path, " ") {
		t.Fatalf("path %q", p.path)
	}
	key(m, "enter")
	want := "idle-session-0001 why does this fail? @" + p.path + " look at the end"
	if len(f.prompts) != 1 || f.prompts[0] != want {
		t.Fatalf("sent %q, want %q", f.prompts, want)
	}
	if len(m.home.part.parts) != 0 || m.home.draft.Value() != "" {
		t.Fatalf("the box stays full: %+v %q", m.home.part.parts, m.home.draft.Value())
	}
}

func TestShortPasteStaysText(t *testing.T) {
	m, _, _ := partsModel(t)
	paste(m, "one short line")
	if m.home.draft.Value() != "one short line" || len(m.home.part.parts) != 0 {
		t.Fatalf("box %q, parts %d", m.home.draft.Value(), len(m.home.part.parts))
	}
}

// Live test (Claude Code 2.1.318): with a file name at the very end, enter
// only picks a row in the file list, and it drops the quotes.
func TestPartsPrompt(t *testing.T) {
	a := &promptPart{n: 1, path: "/c/a.txt"}
	b := &promptPart{n: 2, path: "/c/my  shot.png"}
	for _, c := range []struct {
		text  string
		parts []*promptPart
		want  string
	}{
		{"see [1] and [2] please", []*promptPart{a, b}, `see @/c/a.txt and @"/c/my  shot.png" please`},
		{"see [1]", []*promptPart{a}, "see @/c/a.txt " + attachedWord},
		{"", []*promptPart{a}, "@/c/a.txt " + attachedWord},
		{"look", []*promptPart{a, b}, `look @/c/a.txt @"/c/my  shot.png" ` + attachedWord},
		{"two\n  lines [1] end", []*promptPart{a}, "two lines @/c/a.txt end"},
		{"[9] is not a part", []*promptPart{a}, "[9] is not a part @/c/a.txt " + attachedWord},
	} {
		if got := partsPrompt(c.text, c.parts); got != c.want {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
	}
}

// A dropped file arrives as a paste of its path.
func TestDroppedFile(t *testing.T) {
	m, f, _ := partsModel(t)
	dir := filepath.Join(t.TempDir(), "my folder")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rfc notes.md")
	if err := os.WriteFile(path, []byte("# Title\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paste(m, strings.ReplaceAll(path, " ", `\ `))
	if !strings.Contains(view(m), "file · rfc notes.md") {
		t.Fatalf("no part:\n%s", view(m))
	}
	key(m, "enter")
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0], `@"`+path+`" `+attachedWord) {
		t.Fatalf("sent %q", f.prompts)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a dropped file must stay: %v", err)
	}
}

func TestPastedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a b.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for in, ok := range map[string]bool{
		strings.ReplaceAll(path, " ", `\ `): true, "'" + path + "'": true, `"` + path + `"`: true, path + "\n": true,
		dir: false, path + "x": false, "a b.txt": false, path + "\nmore": false, "": false,
	} {
		if got, is := pastedPath(in); is != ok || (ok && got != path) {
			t.Errorf("%q: %q %v", in, got, is)
		}
	}
}

// ctrl+v takes an image from the clipboard. The card shows which one, and x
// removes it with its file and its [n].
func TestClipboardImage(t *testing.T) {
	m, f, _ := partsModel(t)
	m.clipImage = func(dest string) error { writePNG(t, dest, color.RGBA{255, 0, 255, 255}); return nil }
	key(m, "ctrl+v")
	p := m.home.part.parts[0]
	if p.kind != partImage || p.w != 40 || p.h != 20 || m.home.draft.Value() != "[1] " {
		t.Fatalf("part %+v, box %q", p, m.home.draft.Value())
	}
	out := view(m)
	for _, want := range []string{"[1] image", "40×20", "from clipboard"} {
		if !strings.Contains(out, want) {
			t.Fatalf("box lacks %q:\n%s", want, out)
		}
	}
	key(m, "ctrl+p", "enter")
	out = view(m)
	for _, want := range []string{"part 1 of 1", "will be sent", "r replace from clipboard"} {
		if !strings.Contains(out, want) {
			t.Fatalf("card lacks %q:\n%s", want, out)
		}
	}
	old := p.path
	m.clipImage = func(dest string) error { writePNG(t, dest, color.RGBA{0, 255, 0, 255}); return nil }
	m.now = func() time.Time { return t0.Add(time.Minute) }
	key(m, "r")
	p = m.home.part.parts[0]
	if _, err := os.Stat(old); p.path == old || err == nil {
		t.Fatalf("not replaced: %q %v", p.path, err)
	}
	key(m, "x")
	if _, err := os.Stat(p.path); len(m.home.part.parts) != 0 || m.home.draft.Value() != "" || err == nil {
		t.Fatalf("not removed: %+v %q %v", m.home.part.parts, m.home.draft.Value(), err)
	}
	if m.home.part.open || m.home.part.pick || len(f.prompts) != 0 {
		t.Fatalf("state %+v, sent %q", m.home.part, f.prompts)
	}
}

// ctrl+v with text on the clipboard acts as a paste, and with nothing says so.
func TestCtrlVWithoutAnImage(t *testing.T) {
	m, _, _ := partsModel(t)
	key(m, "ctrl+v")
	if m.Status() != "The clipboard has no image and no text." {
		t.Fatalf("status %q", m.Status())
	}
	m.clipRead = func() (string, error) { return "a\nb\n", nil }
	key(m, "ctrl+v")
	if len(m.home.part.parts) != 1 {
		t.Fatalf("parts %d", len(m.home.part.parts))
	}
}

// A secret and a very long line stop the send once, on the check screen,
// which says where and does not repeat the secret.
func TestWarningsOpenTheCheck(t *testing.T) {
	m, f, _ := partsModel(t)
	key := func(ks ...string) { key(m, ks...) }
	secret := "STRIPE_KEY=" + fakeKey("sk_", "live_", strings.Repeat("x", 26))
	long := strings.Repeat("y", wideLine)
	paste(m, "first\n"+secret+"\n"+long+"\nlast\n")
	key("enter")
	if len(f.prompts) != 0 || !m.home.part.check {
		t.Fatalf("sent %q, check %v", f.prompts, m.home.part.check)
	}
	out := view(m)
	for _, want := range []string{"check before sending", "! 2 things to look at", "Line 2 of [1] looks like a secret", "Line 3 of [1] is 5000 characters", "starts first", "ends   last"} {
		if !strings.Contains(out, want) {
			t.Fatalf("check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "xxxxxxxxxxxx") {
		t.Fatalf("the check shows the secret:\n%s", out)
	}
	key("g")
	if !m.home.part.open || !strings.Contains(view(m), "    2 ") {
		t.Fatalf("g did not open line 2:\n%s", view(m))
	}
	key("esc", "esc") // the part, then the parts' cursor is off
	key("enter", "e")
	if m.home.part.check || len(f.prompts) != 0 {
		t.Fatalf("e sent: %q", f.prompts)
	}
	key("enter", "enter")
	if len(f.prompts) != 1 {
		t.Fatalf("enter on the check sent %q", f.prompts)
	}
}

func TestDifferentClipboardWarns(t *testing.T) {
	m, f, _ := partsModel(t)
	m.clipRead = func() (string, error) { return "something else", nil }
	paste(m, "a\nb\n")
	if !strings.Contains(view(m), "differs from clipboard") {
		t.Fatalf("no mark:\n%s", view(m))
	}
	key(m, "enter")
	if !m.home.part.check || len(f.prompts) != 0 {
		t.Fatalf("check %v, sent %q", m.home.part.check, f.prompts)
	}
}

// A busy session keeps a part in its queue, flat in the line (ADR-0021). A
// fork reads no attachments, so ctrl+f refuses it.
func TestPartsQueueForABusySession(t *testing.T) {
	m, f := newSessionModel(t)
	m.partsDir = t.TempDir()
	m.clipRead = func() (string, error) { return "", nil }
	key(m, "j", "j", "enter") // the busy session
	paste(m, "a\nb\n")
	key(m, "ctrl+f")
	if len(f.prompts) != 0 || len(m.home.part.parts) != 1 || !strings.Contains(m.Status(), "A fork reads no attachments") {
		t.Fatalf("sent %q, status %q", f.prompts, m.Status())
	}
	path := m.home.part.parts[0].path
	key(m, "enter")
	if want := "queue busy-session-0001 @" + path + " (attached)"; len(f.prompts) != 1 || f.prompts[0] != want {
		t.Fatalf("sent %q, want %q (%s)", f.prompts, want, m.Status())
	}
}

func TestPartFileGoneStopsTheSend(t *testing.T) {
	m, f, _ := partsModel(t)
	paste(m, "a\nb\n")
	if err := os.Remove(m.home.part.parts[0].path); err != nil {
		t.Fatal(err)
	}
	key(m, "enter")
	if len(f.prompts) != 0 || !strings.Contains(m.Status(), "gone from the disk") {
		t.Fatalf("sent %q, status %q", f.prompts, m.Status())
	}
}

// A pasted log can hold escape codes: they must not reach the terminal.
func TestPartDrawsNoControlCodes(t *testing.T) {
	m, _, _ := partsModel(t)
	paste(m, "\x1b[2Jcleared\x07\nsecond\x1b]0;title\x07\n")
	if got := m.home.part.parts[0].head[0]; partLine(got) != "cleared" {
		t.Fatalf("line %q", partLine(got))
	}
	for _, l := range partLines(m.home.part.parts[0], 60, false, false) {
		if strings.ContainsAny(ansi.Strip(l), "\x1b\x07") {
			t.Fatalf("control code in %q", l)
		}
	}
	key(m, "ctrl+p", "enter")
	if out := ansi.Strip(view(m)); !strings.Contains(out, "cleared") || strings.ContainsAny(out, "\x07") {
		t.Fatalf("view:\n%q", out)
	}
}

func TestPartPicking(t *testing.T) {
	m, _, _ := partsModel(t)
	paste(m, "one\ntwo\n")
	paste(m, "three\nfour\n")
	key(m, "ctrl+p")
	if m.home.part.sel != 1 || !strings.Contains(view(m), "▸ [2]") {
		t.Fatalf("pick:\n%s", view(m))
	}
	key(m, "up", "enter")
	if !strings.Contains(view(m), "    1 one") {
		t.Fatalf("open:\n%s", view(m))
	}
	key(m, "x")
	if m.home.draft.Value() != "[2] " || len(m.home.part.parts) != 1 || !m.home.part.open {
		t.Fatalf("box %q, parts %d", m.home.draft.Value(), len(m.home.part.parts))
	}
	key(m, "esc", "esc")
	key(m, "hello")
	if m.home.draft.Value() != "[2] hello" {
		t.Fatalf("keys did not return to the text: %q", m.home.draft.Value())
	}
}

func TestCleanParts(t *testing.T) {
	dir := t.TempDir()
	old, fresh := filepath.Join(dir, "old.txt"), filepath.Join(dir, "fresh.txt")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, t0.Add(-25*time.Hour), t0.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, t0.Add(-time.Hour), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleanParts(dir, t0, nil)
	if _, err := os.Stat(old); err == nil {
		t.Fatal("old file stays")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal(err)
	}
}

// A part of a queued prompt stays past the 24 h cleanup, and goes once no
// item names it (ADR-0021).
func TestCleanPartsKeepsQueuedFiles(t *testing.T) {
	m, _ := newSessionModel(t)
	dir := t.TempDir()
	m.partsDir = dir
	queued, loose := filepath.Join(dir, "queued.txt"), filepath.Join(dir, "loose.txt")
	for _, p := range []string{queued, loose} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, t0.Add(-25*time.Hour), t0.Add(-25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	m.snap.Sessions[0].Queue = []api.QueuedPrompt{{ID: 1, Text: "see @" + queued + " (attached)", CreatedAt: t0.Add(-time.Hour)}}
	m.partsDirReady()
	if _, err := os.Stat(queued); err != nil {
		t.Fatalf("the cleanup removed a queued part: %v", err)
	}
	if _, err := os.Stat(loose); err == nil {
		t.Fatal("a part no item names stays")
	}
	cleanParts(dir, t0, nil)
	if _, err := os.Stat(queued); err == nil {
		t.Fatal("a part stays after its item left the queue")
	}
}

// fakeKey joins a made-up key at run time, so that no text in this file has
// the shape of a real one: GitHub's push protection reads it as a secret.
func fakeKey(parts ...string) string { return strings.Join(parts, "") }

func TestSecretRules(t *testing.T) {
	for in, want := range map[string]bool{
		fakeKey("sk-", "ant-api03-", "abcdefghijklmnopqrstuv"):  true,
		fakeKey("ghp", "_", "abcdefghijklmnopqrstuvwxyz123456"): true,
		fakeKey("AKIA", "", "IOSFODNN7EXAMPLE"):                 true,
		fakeKey("-----BEGIN RSA ", "PRIVATE KEY", "-----"):      true,
		`"api_key": "abcd1234efgh"`:                             true,
		"PASSWORD=hunter2hunter2":                               true,
		"the token is short":                                    false,
		"session_test.go:78: want 3, got 2":                     false,
		"password: ok":                                          false,
		"=== RUN TestSessionPrompt":                             false,
	} {
		if got := len(scanWarns([]string{in})) > 0; got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestThumbKeepsShape(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 100))
	if c, r := thumbSize(img, 40, 20); c != 40 || r != 5 {
		t.Fatalf("wide: %d×%d", c, r)
	}
	tall := image.NewRGBA(image.Rect(0, 0, 100, 400))
	if c, r := thumbSize(tall, 40, 20); r != 20 || c != 10 {
		t.Fatalf("tall: %d×%d", c, r)
	}
	if ls := thumbLines(img, 40, 20); len(ls) != 5 || ansi.StringWidth(ls[0]) != 40 {
		t.Fatalf("lines %d", len(ls))
	}
}
