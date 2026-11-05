package ui

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // decode a dropped GIF for its card
	_ "image/jpeg" // decode a dropped JPEG for its card
	_ "image/png"  // decode a clipboard image for its card
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// The parts of a prompt (ADR-0020): pasted text, an image from the clipboard
// or a dropped file. Each one is a file, and the prompt names it with @path,
// which Claude Code reads with no tool call and no permission prompt.

const (
	longPasteRunes = 2000           // a one-line paste this long is a part too
	endLines       = 3              // lines shown at each end of a part
	wideLine       = 5000           // a line this wide earns a warning
	bigPart        = 200 << 10      // a file this big may not be read whole
	maxShownFile   = 8 << 20        // a bigger file gets no bookends
	maxDecoded     = 32 << 20       // a bigger image gets no thumbnail
	partsKept      = 24 * time.Hour // part files older than this are removed
	thumbSide      = 140            // the longest side of a kept thumbnail
	attachedWord   = "(attached)"   // follows a part at the very end
	clipboardFrom  = "clipboard"    // where an image came from
)

type partKind int

const (
	partText partKind = iota
	partImage
	partFile
)

// partWarn is one thing to look at before sending. Line is 0 for the whole
// part.
type partWarn struct {
	line int
	what string
}

// promptPart is one attachment of the prompt box.
type promptPart struct {
	n      int // the number in the [n] token
	kind   partKind
	path   string
	name   string
	from   string
	size   int64
	lines  int
	head   []string // the first lines, for a text
	tail   []string // the last lines, when more than head and tail exist
	w, h   int      // an image's size
	thumb  image.Image
	same   int // 1 same as the clipboard, -1 differs, 0 not compared
	warns  []partWarn
	binary bool
}

// label names the part in the box.
func (p *promptPart) label() string {
	switch p.kind {
	case partImage:
		return "image · " + p.name
	case partFile:
		return "file · " + p.name
	}
	return "pasted text"
}

// facts is the part's size line.
func (p *promptPart) facts() string {
	var fs []string
	if p.kind == partImage {
		if p.w > 0 {
			fs = append(fs, fmt.Sprintf("%d×%d", p.w, p.h))
		}
	} else if !p.binary {
		fs = append(fs, countOf(p.lines, "line", "lines"))
	}
	fs = append(fs, sizeLabel(p.size))
	switch p.same {
	case 1:
		fs = append(fs, "same as clipboard")
	case -1:
		fs = append(fs, "differs from clipboard")
	}
	return strings.Join(fs, " · ")
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func sizeLabel(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// partLine makes one line of a file safe to draw: a pasted log can hold
// escape codes, and those must never reach the terminal.
func partLine(s string) string {
	s = ansi.Strip(strings.TrimRight(s, "\r"))
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
			return -1
		}
		return r
	}, s)
}

// fillText sets the bookends, the line count and the warnings from a text.
func (p *promptPart) fillText(text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	p.lines = len(lines)
	if len(lines) <= 2*endLines+1 {
		p.head = lines
	} else {
		p.head, p.tail = lines[:endLines], lines[len(lines)-endLines:]
	}
	p.warns = append(scanWarns(lines), p.sizeWarn()...)
}

func (p *promptPart) sizeWarn() []partWarn {
	if p.size < bigPart {
		return nil
	}
	return []partWarn{{what: sizeLabel(p.size) + " may be too large for Claude to read at once"}}
}

// secretRules find what looks like a key. They look at one line at a time.
var secretRules = []*regexp.Regexp{
	regexp.MustCompile(`\bsk[-_](live|test|ant|proj)?[-_]?[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\b(ghp|gho|ghs|ghu|github_pat)_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b[a-z0-9_-]*(api[_-]?key|secret|token|password|passwd)[a-z0-9_-]*["']?\s*[:=]\s*["']?[^\s"']{8,}`),
}

// scanWarns lists what to look at in a text. It says where, and it never
// repeats the secret.
func scanWarns(lines []string) []partWarn {
	var out []partWarn
	for i, l := range lines {
		for _, re := range secretRules {
			if re.MatchString(l) {
				out = append(out, partWarn{i + 1, "looks like a secret: " + maskSecret(l, re)})
				break
			}
		}
		if n := utf8.RuneCountInString(l); n >= wideLine {
			out = append(out, partWarn{i + 1, fmt.Sprintf("is %d characters long", n)})
		}
	}
	return out
}

// maskSecret keeps a few characters of the match, enough to find it.
func maskSecret(line string, re *regexp.Regexp) string {
	r := []rune(partLine(re.FindString(line)))
	return string(r[:min(len(r), 10)]) + "…"
}

// allWarns are the warnings of every part, with the part's number.
func allWarns(parts []*promptPart) []string {
	var out []string
	for _, p := range parts {
		for _, w := range p.warns {
			where := fmt.Sprintf("[%d]", p.n)
			if w.line > 0 {
				where = fmt.Sprintf("Line %d of [%d]", w.line, p.n)
			}
			out = append(out, where+" "+w.what)
		}
		if p.same == -1 {
			out = append(out, fmt.Sprintf("[%d] differs from the clipboard: it changed, or the paste did not arrive whole", p.n))
		}
	}
	return out
}

// partFileName is the name of a new part file.
func partFileName(now time.Time, n int, ext string) string {
	return fmt.Sprintf("%s-%d.%s", now.Format("20060102-150405"), n, ext)
}

// writePart keeps data as a part file only this user can read: a paste can
// hold a secret.
func writePart(dir, name string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, data, 0o600)
}

// cleanParts removes part files old enough that nothing still needs them.
func cleanParts(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > partsKept {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// newTextPart keeps a paste as a file, with the line ends made plain.
func newTextPart(dir string, n int, text string, now time.Time) (*promptPart, error) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	path, err := writePart(dir, partFileName(now, n, "txt"), []byte(text))
	if err != nil {
		return nil, err
	}
	p := &promptPart{n: n, kind: partText, path: path, name: filepath.Base(path), from: "pasted " + now.Format("15:04"), size: int64(len(text))}
	p.fillText(text)
	return p, nil
}

// newImagePart reads an image file for its size and thumbnail. A format Go
// cannot decode still goes: only its card has less to show.
func newImagePart(n int, path, from string) (*promptPart, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	p := &promptPart{n: n, kind: partImage, path: path, name: filepath.Base(path), from: from, size: info.Size()}
	if info.Size() > maxDecoded {
		return p, nil
	}
	if b, err := os.ReadFile(path); err == nil {
		if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
			p.w, p.h = img.Bounds().Dx(), img.Bounds().Dy()
			p.thumb = shrink(img, thumbSide)
		}
	}
	return p, nil
}

// shrink keeps a small copy for drawing, sampled from the middle of each
// block.
func shrink(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return img
	}
	scale := max(float64(w), float64(h)) / float64(side)
	if scale < 1 {
		scale = 1
	}
	dw, dh := max(1, int(float64(w)/scale)), max(1, int(float64(h)/scale))
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			out.Set(x, y, img.At(b.Min.X+int((float64(x)+.5)*scale), b.Min.Y+int((float64(y)+.5)*scale)))
		}
	}
	return out
}

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// newFilePart attaches a file from the disk where it is.
func newFilePart(n int, path string) (*promptPart, error) {
	if strings.ContainsAny(path, `"`+"\n") {
		return nil, fmt.Errorf("a path with a quote cannot go in a prompt")
	}
	if imageExts[strings.ToLower(filepath.Ext(path))] {
		return newImagePart(n, path, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	p := &promptPart{n: n, kind: partFile, path: path, name: filepath.Base(path), from: path, size: info.Size()}
	if info.Size() > maxShownFile {
		p.binary, p.warns = true, p.sizeWarn()
		return p, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		p.binary, p.warns = true, p.sizeWarn()
		return p, nil
	}
	p.fillText(string(b))
	return p, nil
}

// pastedPath reads a paste as one file: that is what a dropped file gives,
// with a space written `\ ` or the whole path in quotes.
func pastedPath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "\n") {
		return "", false
	}
	if len(s) > 1 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		s = s[1 : len(s)-1]
	} else {
		s = strings.ReplaceAll(s, `\ `, " ")
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		s = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(s) {
		return "", false
	}
	info, err := os.Stat(s)
	return s, err == nil && info.Mode().IsRegular()
}

// isLongPaste says whether a paste becomes a part: it has lines to keep, or
// it is too long for the box.
func isLongPaste(s string) bool {
	return strings.Contains(strings.TrimSpace(s), "\n") || utf8.RuneCountInString(s) >= longPasteRunes
}

// mention is how a prompt names a file for Claude Code. Quotes hold a path
// with a space together.
func mention(path string) string {
	if strings.Contains(path, " ") {
		return `@"` + path + `"`
	}
	return "@" + path
}

func token(n int) string { return fmt.Sprintf("[%d]", n) }

// partsPrompt is the line that goes to the session: the text on one line,
// each [n] replaced by its file, and a part with no [n] left in the text
// added at the end. A file name at the very end would open Claude Code's
// file list, and enter would then pick a row instead of sending, so a word
// always follows it.
func partsPrompt(text string, parts []*promptPart) string {
	text = strings.Join(strings.Fields(text), " ")
	last := ""
	for _, p := range parts {
		m := mention(p.path)
		if strings.Contains(text, token(p.n)) {
			text = strings.Replace(text, token(p.n), m, 1)
		} else {
			text = strings.TrimSpace(text + " " + m)
		}
		last = m
	}
	if last != "" && strings.HasSuffix(text, last) {
		text += " " + attachedWord
	}
	return text
}

// partsGone names a part whose file is no longer there.
func partsGone(parts []*promptPart) string {
	for _, p := range parts {
		if _, err := os.Stat(p.path); err != nil {
			return token(p.n) + " " + p.name
		}
	}
	return ""
}

// sameText compares a part with the clipboard, line ends aside.
func sameText(a, b string) bool {
	norm := func(s string) string {
		return strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), "\n")
	}
	return norm(a) == norm(b)
}
