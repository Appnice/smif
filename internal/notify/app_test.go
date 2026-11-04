package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMacAppBuild builds Smif.app in a temp dir: smif's icon, the bundle id,
// no Dock icon, a valid ad-hoc signature. SMIF_NOTIFY_REAL=1 also shows one
// notification (a person must look).
func TestMacAppBuild(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	if real := os.Getenv("SMIF_NOTIFY_REAL"); real != "" {
		dir = real
	}
	a := &macApp{dir: dir}
	if err := a.ensure(); err != nil {
		t.Fatal(err)
	}
	icon, err := os.ReadFile(filepath.Join(a.path(), "Contents", "Resources", "applet.icns"))
	if err != nil || string(icon) != string(icns) {
		t.Fatalf("icon not smif's: %v", err)
	}
	plist, _ := exec.Command("plutil", "-p", filepath.Join(a.path(), "Contents", "Info.plist")).Output()
	for _, want := range []string{`"CFBundleIdentifier" => "dev.smif.notify"`, `"CFBundleName" => "Smif"`, `"LSUIElement" => true`} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("Info.plist lacks %s:\n%s", want, plist)
		}
	}
	if out, err := exec.Command("codesign", "--verify", a.path()).CombinedOutput(); err != nil {
		t.Errorf("codesign --verify: %v %s", err, out)
	}
	// A second ensure on a new value keeps the built app (same stamp).
	b := &macApp{dir: dir}
	st, _ := os.Stat(a.path())
	if err := b.ensure(); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(b.path()); !os.SameFile(st, st2) {
		t.Error("app rebuilt although the stamp matches")
	}
	if os.Getenv("SMIF_NOTIFY_REAL") != "" {
		if err := a.notify("bonsai is silent", "nothing written for 26m: thinking"); err != nil {
			t.Fatal(err)
		}
	}
}
