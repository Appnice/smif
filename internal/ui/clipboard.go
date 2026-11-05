package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/appnice/smif/internal/api"
)

// clipboardTools are tried in order. The text goes through stdin, never argv.
var clipboardTools = [][]string{
	{"pbcopy"},
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// clipboardReaders print the clipboard's text.
var clipboardReaders = [][]string{
	{"pbpaste"},
	{"wl-paste", "-n"},
	{"xclip", "-selection", "clipboard", "-o"},
	{"xsel", "--clipboard", "--output"},
}

// clipboardImageScript saves the clipboard's image as a PNG at the path it
// gets as an argument, so that no path is written into the script.
var clipboardImageScript = []string{
	"-e", "on run argv",
	"-e", "set f to open for access (POSIX file (item 1 of argv)) with write permission",
	"-e", "try",
	"-e", "write (the clipboard as «class PNGf») to f",
	"-e", "close access f",
	"-e", "on error e",
	"-e", "close access f",
	"-e", "error e",
	"-e", "end try",
	"-e", "end run",
}

var errNoClipboard = errors.New("no clipboard tool found (pbcopy, wl-copy, xclip or xsel)")

// writeClipboard puts text on the system clipboard. Terminal.app has no
// escape sequence for this, so it runs the tool of the system.
func writeClipboard(text string) error {
	for _, tool := range clipboardTools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errNoClipboard
}

// readClipboard gives the clipboard's text.
func readClipboard() (string, error) {
	for _, tool := range clipboardReaders {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, tool[1:]...).Output()
		return string(out), err
	}
	return "", errNoClipboard
}

// saveClipboardImage saves the clipboard's image as a PNG, or fails when it
// holds none.
func saveClipboardImage(dest string) error {
	var cmd []string
	switch {
	case runtime.GOOS == "darwin":
		cmd = append(append([]string{"osascript"}, clipboardImageScript...), dest)
	default:
		if _, err := exec.LookPath("wl-paste"); err == nil {
			cmd = []string{"wl-paste", "-t", "image/png"}
		} else {
			cmd = []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}
		}
	}
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, path, cmd[1:]...)
	if runtime.GOOS != "darwin" {
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		c.Stdout = f
	}
	if err := c.Run(); err != nil {
		return err
	}
	if info, err := os.Stat(dest); err != nil || info.Size() == 0 {
		return errors.New("no image on the clipboard")
	}
	return nil
}

// mdCodeBlocks returns the fenced code blocks of an answer, exactly as
// written. A long turn is kept from its end and starts with "…": when that
// cut went through a block, the first fence closes it and the block is left
// out, because its start is gone.
func mdCodeBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	isFence := func(l string) bool { return strings.HasPrefix(strings.TrimLeft(l, " "), "```") }
	fences := 0
	for _, l := range lines {
		if isFence(l) {
			fences++
		}
	}
	if strings.HasPrefix(text, "…") && fences%2 == 1 {
		for i, l := range lines {
			if isFence(l) {
				lines = lines[i+1:]
				break
			}
		}
	}
	var out, cur []string
	code := false
	add := func() {
		if b := strings.Join(cur, "\n"); strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
		cur = nil
	}
	for _, l := range lines {
		if isFence(l) {
			if code {
				add()
			}
			code = !code
			continue
		}
		if code {
			cur = append(cur, l)
		}
	}
	if code {
		add()
	}
	return out
}

// sessionCode lists the code blocks of a session's answers, newest first.
func (m *Model) sessionCode(sv *api.SessionView) []string {
	turns, _ := m.sessionTurns(sv)
	var answers []string
	for _, t := range turns {
		if isAnswer(t) {
			answers = append(answers, t.Text)
		}
	}
	if len(answers) == 0 {
		answers = []string{sv.LastAnswer}
	}
	var out []string
	for i := len(answers) - 1; i >= 0; i-- {
		blocks := mdCodeBlocks(answers[i])
		for j := len(blocks) - 1; j >= 0; j-- {
			out = append(out, blocks[j])
		}
	}
	return out
}

// copiedMsg reports a finished copy.
type copiedMsg struct {
	status string
	err    error
}

// copyCode copies the newest code block of the session. Pressing the key
// again takes the block before it, and after the oldest the newest again.
func (m *Model) copyCode(sv *api.SessionView) tea.Cmd {
	blocks := m.sessionCode(sv)
	if len(blocks) == 0 {
		m.setStatus("No code block in this conversation.", false)
		return nil
	}
	// A new answer or another session starts from the newest block again.
	if from := sv.SessionID + "\x00" + blocks[0]; from != m.home.copyFrom {
		m.home.copyFrom, m.home.copyNext = from, 0
	}
	i := m.home.copyNext % len(blocks)
	m.home.copyNext = i + 1
	block, status, clip := blocks[i], copyStatus(blocks[i], i, len(blocks)), m.clip
	return func() tea.Msg {
		return copiedMsg{status: status, err: clip(block)}
	}
}

// copyKeyHint is the footer's copy key, or "" with nothing to copy.
func (m *Model) copyKeyHint(sv *api.SessionView, k string) string {
	if len(m.sessionCode(sv)) == 0 {
		return ""
	}
	return k + " copy code · "
}

func copyStatus(block string, i, n int) string {
	size := fmt.Sprintf("%d lines", strings.Count(block, "\n")+1)
	if !strings.Contains(block, "\n") {
		size = "1 line"
	}
	head := "Copied"
	if n > 1 {
		head = fmt.Sprintf("Copied block %d of %d (newest first)", i+1, n)
	}
	start := ansi.Truncate(oneLine(block), 32, "…")
	return fmt.Sprintf("%s: %s, %d characters: %s", head, size, len([]rune(block)), start)
}

func (m *Model) onCopied(msg copiedMsg) {
	if msg.err != nil {
		m.setStatus("Not copied: "+msg.err.Error(), true)
		return
	}
	m.setStatus(msg.status, false)
}
