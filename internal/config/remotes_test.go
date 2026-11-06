package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddRemote(t *testing.T) {
	cases := []struct{ name, before, host, after string }{
		{"no file text", "", "mini", "[daemon]\nremotes = [\"mini\"]\n"},
		{"no daemon table", "# mine\n[defaults]\nmax_hops = 4\n", "mini",
			"# mine\n[defaults]\nmax_hops = 4\n\n[daemon]\nremotes = [\"mini\"]\n"},
		{"new key keeps the rest", "# top\n[daemon] # why\nmax_workers = 2\n\n[defaults]\nmax_hops = 4\n", "mini",
			"# top\n[daemon] # why\nremotes = [\"mini\"]\nmax_workers = 2\n\n[defaults]\nmax_hops = 4\n"},
		{"one more host", "[daemon]\nremotes = [\"a\"] # both Macs\nnotify = \"os\"\n", "me@b.local",
			"[daemon]\nremotes = [\"a\", \"me@b.local\"] # both Macs\nnotify = \"os\"\n"},
		{"array on many lines", "[daemon]\nremotes = [\n  \"a\",\n]\n", "b", "[daemon]\nremotes = [\"a\", \"b\"]\n"},
		{"a comment is not the key", "[daemon]\n# remotes = [\"old\"]\n", "b", "[daemon]\nremotes = [\"b\"]\n# remotes = [\"old\"]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.toml")
			if c.before != "" {
				if err := os.WriteFile(file, []byte(c.before), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			added, err := AddRemote(file, c.host)
			if err != nil || !added {
				t.Fatalf("added %v, err %v", added, err)
			}
			got, _ := os.ReadFile(file)
			if string(got) != c.after {
				t.Fatalf("got:\n%s\nwant:\n%s", got, c.after)
			}
			added, err = AddRemote(file, c.host)
			if err != nil || added {
				t.Fatalf("second add: added %v, err %v", added, err)
			}
			again, _ := os.ReadFile(file)
			if string(again) != c.after {
				t.Fatalf("second add changed the file:\n%s", again)
			}
		})
	}
}

func TestAddRemoteRefuses(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	if _, err := AddRemote(file, "-oProxyCommand=x"); err == nil {
		t.Fatal("took an ssh option as a host")
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("wrote a file for a bad host")
	}
	broken := "[daemon\nmax_workers = 2\n"
	if err := os.WriteFile(file, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AddRemote(file, "mini"); err == nil {
		t.Fatal("edited a broken config")
	}
	if b, _ := os.ReadFile(file); string(b) != broken {
		t.Fatalf("broken config changed:\n%s", b)
	}
}

func TestAddRemoteKeepsModeAndLink(t *testing.T) {
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real.toml"), filepath.Join(dir, "config.toml")
	if err := os.WriteFile(real, []byte("[daemon]\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AddRemote(link, "mini"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link is gone: %v %v", st, err)
	}
	st, err := os.Stat(real)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, err %v", st.Mode(), err)
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), `remotes = ["mini"]`) {
		t.Fatalf("real file:\n%s", b)
	}
}
