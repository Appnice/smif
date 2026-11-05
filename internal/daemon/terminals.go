package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appnice/smif/internal/store"
)

// Terminals (ADR-0012). Smif types the human's prompt into the terminal
// a session runs in, and brings that terminal to the front: Terminal.app
// and iTerm2 by AppleScript (macOS), tmux by send-keys, herdr by its CLI
// (any OS; herdr.go). A tab or pane is found by the session's tty (herdr:
// by the claude pid). Text and Enter go separately: text
// with its newline in one piece lands in the Claude Code input as a paste
// and is not sent (docs/compat.md).

// terminal types text into the terminal a session runs in.
type terminal interface {
	// Ready checks that the session's claude process still owns the
	// foreground of its terminal, so the text cannot land in a shell.
	Ready(p store.Presence) error
	// Type types text, then Enter as a separate key press (enterDelay
	// later, so claude has taken the text in).
	Type(ctx context.Context, p store.Presence, text string) error
	// Enter presses Enter once more: for text that sits unsent in the
	// input box because the first Enter came in with it as one paste.
	Enter(ctx context.Context, p store.Presence) error
	// Supports says why this terminal cannot reach the session ("" = it can).
	Supports(p store.Presence) string
	// Focus brings the session's tab or pane to the front.
	Focus(ctx context.Context, p store.Presence) error
	// Open starts argv in dir in a new tab or window of the terminal term
	// ("" = Terminal.app on macOS, else tmux), for a new session (ADR-0015).
	// label names it where the terminal shows names (herdr workspaces).
	Open(ctx context.Context, term, dir, label string, argv []string) error
}

// enterDelay is the pause between the text and Enter. Claude Code reads a
// burst of keys as a paste and keeps an Enter that comes too soon inside
// it as a plain newline, longer text taking longer to settle.
func enterDelay(text string) time.Duration {
	d := 500*time.Millisecond + time.Duration(utf8.RuneCountInString(text))*8*time.Millisecond
	return min(d, 2*time.Second)
}

func newTerminal(spec string) terminal {
	switch {
	case spec == "none":
		return offTerminal{}
	case strings.HasPrefix(spec, "log:"):
		return logTerminal{path: strings.TrimPrefix(spec, "log:")}
	}
	return autoTerminal{}
}

// The $TERM_PROGRAM values (and "tmux", termHerdr) Smif can drive.
const (
	termApple = "Apple_Terminal"
	termITerm = "iTerm.app"
	termTmux  = "tmux"
)

// termSupport says why Smif cannot type into the session's terminal,
// whatever the OS ("" = a terminal it knows).
func termSupport(p store.Presence) string {
	switch p.Term {
	case termApple, termITerm, termTmux, termHerdr:
		return ""
	case "":
		return "Smif does not know which terminal this session runs in"
	}
	return "prompts work in Terminal.app, iTerm2, tmux and herdr; this session runs in " + p.Term
}

type offTerminal struct{}

func (offTerminal) Ready(store.Presence) error {
	return errors.New("prompts are off (daemon.terminal = \"none\")")
}
func (offTerminal) Type(context.Context, store.Presence, string) error {
	return errors.New("prompts are off")
}
func (offTerminal) Enter(context.Context, store.Presence) error {
	return errors.New("prompts are off")
}

func (offTerminal) Focus(context.Context, store.Presence) error {
	return errors.New("prompts are off")
}
func (offTerminal) Open(context.Context, string, string, string, []string) error {
	return errors.New("terminals are off (daemon.terminal = \"none\")")
}
func (offTerminal) Supports(store.Presence) string {
	return "prompts are off (daemon.terminal = \"none\")"
}

// logTerminal appends each prompt and focus to a JSONL file (tests).
type logTerminal struct{ path string }

func (logTerminal) Ready(p store.Presence) error {
	if !processAlive(p.PID) {
		return fmt.Errorf("process %d is gone", p.PID)
	}
	return nil
}

func (l logTerminal) write(v map[string]any) error {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(v)
	_, err = f.Write(append(b, '\n'))
	return err
}

func (l logTerminal) Type(_ context.Context, p store.Presence, text string) error {
	return l.write(map[string]any{"session_id": p.SessionID, "tty": p.TTY, "pid": p.PID, "term": p.Term, "text": text})
}

func (l logTerminal) Enter(_ context.Context, p store.Presence) error {
	return l.write(map[string]any{"session_id": p.SessionID, "tty": p.TTY, "enter": true})
}

func (l logTerminal) Focus(_ context.Context, p store.Presence) error {
	return l.write(map[string]any{"session_id": p.SessionID, "tty": p.TTY, "term": p.Term, "focus": true})
}

func (l logTerminal) Open(_ context.Context, term, dir, label string, argv []string) error {
	return l.write(map[string]any{"open": true, "term": term, "dir": dir, "label": label, "argv": argv})
}

// Supports applies the same terminal rule as the real terminals, on any OS.
func (logTerminal) Supports(p store.Presence) string { return termSupport(p) }

// autoTerminal drives the session's own terminal: Terminal.app or iTerm2
// (macOS), tmux or herdr.
type autoTerminal struct{}

func (autoTerminal) Supports(p store.Presence) string {
	if why := termSupport(p); why != "" {
		return why
	}
	switch p.Term {
	case termApple, termITerm:
		if runtime.GOOS != "darwin" {
			return "Terminal.app and iTerm2 prompts work on macOS only"
		}
	case termTmux:
		if tmuxPath() == "" {
			return "this session runs in tmux, but the daemon cannot find the tmux command"
		}
	case termHerdr:
		if herdrPath() == "" {
			return "this session runs in herdr, but the daemon cannot find the herdr command"
		}
	}
	return ""
}

// Ready runs ps on the claude process: it must be alive, be claude, sit on
// the recorded tty and be in that tty's foreground process group (+).
func (autoTerminal) Ready(p store.Presence) error {
	out, err := exec.Command("ps", "-o", "stat=,tty=,comm=", "-p", strconv.Itoa(p.PID)).Output()
	if err != nil {
		return fmt.Errorf("the claude process %d is gone", p.PID)
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		return fmt.Errorf("cannot read process %d", p.PID)
	}
	stat, tty, comm := f[0], f[1], strings.Join(f[2:], " ")
	switch {
	case !strings.Contains(filepath.Base(comm), "claude"):
		return fmt.Errorf("process %d is %s, not claude", p.PID, filepath.Base(comm))
	case "/dev/"+tty != p.TTY:
		return fmt.Errorf("process %d is on %s, not %s", p.PID, tty, p.TTY)
	case !strings.Contains(stat, "+"):
		return errors.New("claude is not in the foreground of its terminal")
	}
	return nil
}

func (autoTerminal) Type(ctx context.Context, p store.Presence, text string) error {
	switch p.Term {
	case termITerm:
		return runAppleScript(ctx, "iTerm2", iTermTypeScript, p.TTY, text, delayMS(text))
	case termTmux:
		return tmuxType(ctx, p.TTY, text)
	case termHerdr:
		return herdrType(ctx, p, text)
	}
	return runAppleScript(ctx, "Terminal", appleTypeScript, p.TTY, text, delayMS(text))
}

// delayMS is enterDelay in whole milliseconds, for AppleScript (an integer:
// a decimal point would depend on the locale).
func delayMS(text string) string { return strconv.FormatInt(enterDelay(text).Milliseconds(), 10) }

func (autoTerminal) Enter(ctx context.Context, p store.Presence) error {
	switch p.Term {
	case termITerm:
		return runAppleScript(ctx, "iTerm2", iTermEnterScript, p.TTY)
	case termTmux:
		return tmuxEnter(ctx, p.TTY)
	case termHerdr:
		return herdrEnter(ctx, p)
	}
	return runAppleScript(ctx, "Terminal", appleEnterScript, p.TTY)
}

func (autoTerminal) Focus(ctx context.Context, p store.Presence) error {
	switch p.Term {
	case termITerm:
		return runAppleScript(ctx, "iTerm2", iTermFocusScript, p.TTY)
	case termTmux:
		return tmuxFocus(ctx, p.TTY)
	case termHerdr:
		return herdrFocus(ctx, p)
	}
	return runAppleScript(ctx, "Terminal", appleFocusScript, p.TTY)
}

func (autoTerminal) Open(ctx context.Context, term, dir, label string, argv []string) error {
	if term == "" {
		term = termTmux
		if runtime.GOOS == "darwin" {
			term = termApple
		}
	}
	line := "cd " + shellQuote(dir) + " && " + shellJoin(argv)
	switch term {
	case termApple, termITerm:
		if runtime.GOOS != "darwin" {
			return errors.New("new sessions in Terminal.app and iTerm2 open on macOS only")
		}
		if term == termITerm {
			return runAppleScript(ctx, "iTerm2", iTermOpenScript, line)
		}
		return runAppleScript(ctx, "Terminal", appleOpenScript, line)
	case termTmux:
		if tmuxPath() == "" {
			return errors.New("the daemon cannot find the tmux command")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		args := append([]string{"new-window", "-c", dir, "--"}, argv...)
		if out, err := exec.CommandContext(ctx, tmuxPath(), args...).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux new-window: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case termHerdr:
		if herdrPath() == "" {
			return errors.New("the daemon cannot find the herdr command")
		}
		return herdrOpen(ctx, dir, label, line)
	}
	return fmt.Errorf("new sessions open in Terminal.app, iTerm2, tmux or herdr, not %s", term)
}

// shellQuote quotes s for sh: 'it'\”s'.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shellJoin quotes each word for sh.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// ---- Terminal.app ----

// appleOpenScript runs a command line in a new Terminal window.
const appleOpenScript = `on run argv
	tell application "Terminal"
		do script (item 1 of argv)
		activate
	end tell
	return "ok"
end run`

const appleTypeScript = `on run argv
	set want to item 1 of argv
	tell application "Terminal"
		repeat with w in windows
			repeat with t in tabs of w
				if tty of t is want then
					do script (item 2 of argv) in t
					delay ((item 3 of argv as integer) / 1000)
					do script "" in t
					return "ok"
				end if
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

const appleEnterScript = `on run argv
	set want to item 1 of argv
	tell application "Terminal"
		repeat with w in windows
			repeat with t in tabs of w
				if tty of t is want then
					do script "" in t
					return "ok"
				end if
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

const appleFocusScript = `on run argv
	set want to item 1 of argv
	tell application "Terminal"
		repeat with w in windows
			repeat with t in tabs of w
				if tty of t is want then
					set selected of t to true
					set index of w to 1
					activate
					return "ok"
				end if
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

// ---- iTerm2 ----

// iTermOpenScript runs a command line in a new tab (a new window when
// there is none).
const iTermOpenScript = `on run argv
	tell application "iTerm2"
		if (count of windows) is 0 then
			create window with default profile
		else
			tell current window to create tab with default profile
		end if
		tell current session of current window to write text (item 1 of argv)
		activate
	end tell
	return "ok"
end run`

// iTerm2 keeps sessions (panes) inside tabs; `write text … newline no`
// types without Enter, and `write text ""` is Enter.
const iTermTypeScript = `on run argv
	set want to item 1 of argv
	tell application "iTerm2"
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s is want then
						tell s to write text (item 2 of argv) newline no
						delay ((item 3 of argv as integer) / 1000)
						tell s to write text ""
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

const iTermEnterScript = `on run argv
	set want to item 1 of argv
	tell application "iTerm2"
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s is want then
						tell s to write text ""
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

const iTermFocusScript = `on run argv
	set want to item 1 of argv
	tell application "iTerm2"
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s is want then
						tell s to select
						tell t to select
						select w
						activate
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "no tab"
end run`

// runAppleScript runs one AppleScript against app with the tab's tty (and
// text) as arguments, never inside the script.
func runAppleScript(ctx context.Context, app, script string, argv ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{}
	for _, l := range strings.Split(script, "\n") {
		args = append(args, "-e", l)
	}
	out, err := exec.CommandContext(ctx, "osascript", append(args, argv...)...).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	switch {
	case err != nil && ctx.Err() != nil:
		return fmt.Errorf("no answer from %s in 15 s. If macOS asks whether smif may control %s, click OK, then try again", app, app)
	case err != nil && strings.Contains(msg, "-1743"):
		return fmt.Errorf("macOS does not let Smif control %s: allow it in System Settings → Privacy & Security → Automation", app)
	case err != nil:
		return fmt.Errorf("osascript: %v: %s", err, msg)
	case msg == "no tab":
		return fmt.Errorf("no %s tab on %s", app, argv[0])
	case msg != "ok":
		return fmt.Errorf("%s: %s", app, msg)
	}
	return nil
}

// ---- tmux ----

// tmuxPath finds the tmux command: on PATH, else the usual install places
// (the daemon runs with launchd's short PATH).
func tmuxPath() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// tmuxPane is the pane whose tty is tty, on the default tmux server.
func tmuxPane(ctx context.Context, tty string) (string, error) {
	out, err := exec.CommandContext(ctx, tmuxPath(), "list-panes", "-a", "-F", "#{pane_tty} #{pane_id}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux list-panes: %v", err)
	}
	if id := paneForTTY(string(out), tty); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("no tmux pane on %s", tty)
}

// paneForTTY reads `list-panes -F '#{pane_tty} #{pane_id}'` output.
func paneForTTY(listing, tty string) string {
	for _, l := range strings.Split(listing, "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == tty {
			return f[1]
		}
	}
	return ""
}

// tmuxTypeArgs are the two send-keys calls: the text as literal keys, then
// Enter.
func tmuxTypeArgs(pane, text string) [][]string {
	return [][]string{
		{"send-keys", "-t", pane, "-l", "--", text},
		{"send-keys", "-t", pane, "Enter"},
	}
}

func tmuxType(ctx context.Context, tty, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pane, err := tmuxPane(ctx, tty)
	if err != nil {
		return err
	}
	for i, args := range tmuxTypeArgs(pane, text) {
		if i > 0 {
			time.Sleep(enterDelay(text))
		}
		if out, err := exec.CommandContext(ctx, tmuxPath(), args...).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func tmuxEnter(ctx context.Context, tty string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pane, err := tmuxPane(ctx, tty)
	if err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, tmuxPath(), "send-keys", "-t", pane, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tmuxFocus selects the pane and its window (and, when a client is
// attached, switches it there).
func tmuxFocus(ctx context.Context, tty string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pane, err := tmuxPane(ctx, tty)
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"select-window", "-t", pane}, {"select-pane", "-t", pane}} {
		if out, err := exec.CommandContext(ctx, tmuxPath(), args...).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	_ = exec.CommandContext(ctx, tmuxPath(), "switch-client", "-t", pane).Run()
	return nil
}
