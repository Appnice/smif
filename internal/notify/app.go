package notify

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// On macOS a notification shows the icon of the app that raised it: with
// osascript that is Script Editor. So Smif builds a small app of its own,
// <data dir>/Smif.app (an AppleScript applet from osacompile, smif's icon,
// signed ad hoc on this Mac), and hands it each notification through a
// queue folder: an applet gets no arguments. The first one makes macOS ask
// the human to allow notifications from Smif.

//go:embed smif.icns
var icns []byte

const appBundleID = "dev.smif.notify"

// appScript shows every queued notification, then quits. A file is claimed
// with mv first, so two copies of the app never show it twice.
const appScript = `on run
	set q to %s
	repeat 5 times
		set names to paragraphs of (do shell script "ls " & quoted form of q & " 2>/dev/null | grep '[.]msg$' || true")
		if (count of names) is 0 or names is {""} then exit repeat
		repeat with n in names
			set f to q & "/" & (n as text)
			try
				set body to do shell script "mv " & quoted form of f & " " & quoted form of (f & ".x") & " && cat " & quoted form of (f & ".x") & " && rm -f " & quoted form of (f & ".x")
				set ls to paragraphs of body
				set t to item 1 of ls
				set m to ""
				if (count of ls) > 1 then set m to item 2 of ls
				display notification m with title t
			end try
		end repeat
	end repeat
end run
`

type macApp struct {
	dir  string // the data dir
	once sync.Once
	err  error
}

func (a *macApp) path() string  { return filepath.Join(a.dir, "Smif.app") }
func (a *macApp) queue() string { return filepath.Join(a.dir, "notify-queue") }

// stamp names this build of the app: a new script or icon rebuilds it.
func (a *macApp) stamp() string {
	h := sha256.New()
	fmt.Fprintf(h, appScript, a.queue())
	h.Write(icns)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ensure builds the app when it is missing or older than this binary's.
func (a *macApp) ensure() error {
	a.once.Do(func() {
		stampFile := filepath.Join(a.path(), "Contents", "Resources", "smif-stamp")
		if b, err := os.ReadFile(stampFile); err == nil && strings.TrimSpace(string(b)) == a.stamp() {
			return
		}
		a.err = a.build(stampFile)
	})
	return a.err
}

func (a *macApp) build(stampFile string) error {
	tmp := filepath.Join(a.dir, "Smif-build.app") // osacompile makes an app only for a .app name
	_ = os.RemoveAll(tmp)
	src := filepath.Join(a.dir, "smif-notify.applescript")
	if err := os.WriteFile(src, []byte(fmt.Sprintf(appScript, appleQuote(a.queue()))), 0o600); err != nil {
		return err
	}
	defer os.Remove(src)
	run := func(name string, args ...string) error {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("osacompile", "-o", tmp, src); err != nil {
		return err
	}
	res := filepath.Join(tmp, "Contents", "Resources")
	if err := os.WriteFile(filepath.Join(res, "applet.icns"), icns, 0o644); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(res, "Assets.car")) // holds the default applet icon
	plist := filepath.Join(tmp, "Contents", "Info.plist")
	_ = run("plutil", "-remove", "CFBundleIconName", plist)
	for _, kv := range [][]string{
		{"CFBundleIdentifier", "-string", appBundleID},
		{"CFBundleName", "-string", "Smif"},
		{"CFBundleIconFile", "-string", "applet"},
		{"LSUIElement", "-bool", "YES"},
	} {
		if err := run("plutil", "-replace", kv[0], kv[1], kv[2], plist); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(res, "smif-stamp"), []byte(a.stamp()+"\n"), 0o644); err != nil {
		return err
	}
	if err := run("codesign", "--force", "--sign", "-", tmp); err != nil {
		return err
	}
	_ = os.RemoveAll(a.path())
	if err := os.Rename(tmp, a.path()); err != nil {
		return err
	}
	// Tell Launch Services about the new icon.
	_ = run("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", "-f", a.path())
	return nil
}

// notify queues one notification and starts the app in the background.
func (a *macApp) notify(title, body string) error {
	if err := a.ensure(); err != nil {
		return err
	}
	if err := os.MkdirAll(a.queue(), 0o700); err != nil {
		return err
	}
	name := strconv.FormatInt(time.Now().UnixNano(), 10)
	tmp := filepath.Join(a.queue(), name+".tmp")
	line := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ") }
	if err := os.WriteFile(tmp, []byte(line(title)+"\n"+line(body)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(a.queue(), name+".msg")); err != nil {
		return err
	}
	return exec.Command("open", "-n", "-g", "-j", a.path()).Run()
}
