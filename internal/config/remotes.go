package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/appnice/smif/internal/paths"
)

// ValidRemote says whether host can go in daemon.remotes.
func ValidRemote(host string) bool { return remoteHostRe.MatchString(host) }

var (
	daemonHeader = regexp.MustCompile(`(?m)^[ \t]*\[daemon\][ \t]*(#.*)?$`)
	nextTable    = regexp.MustCompile(`(?m)^[ \t]*\[`)
	remotesKey   = regexp.MustCompile(`(?ms)^[ \t]*remotes[ \t]*=[ \t]*\[.*?\]`)
)

// AddRemote puts host in daemon.remotes in the config file and keeps the rest
// of the file as it is, comments included. It reports false when the host is
// already there. The edit must parse back to the expected list, or the file
// stays untouched.
func AddRemote(file, host string) (bool, error) {
	if file == "" {
		file = paths.ConfigFile()
	}
	if !ValidRemote(host) {
		return false, fmt.Errorf("%q is not an ssh host name", host)
	}
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	c, err := Parse(b)
	if err != nil {
		return false, fmt.Errorf("%s: %w", file, err)
	}
	if slices.Contains(c.Daemon.Remotes, host) {
		return false, nil
	}
	want := append(slices.Clone(c.Daemon.Remotes), host)
	text := withRemotes(string(b), want)
	after, err := Parse([]byte(text))
	if err != nil || !slices.Equal(after.Daemon.Remotes, want) {
		return false, fmt.Errorf("cannot edit %s: add %q to daemon.remotes by hand", file, host)
	}
	return true, replaceFile(file, text)
}

// RemoveRemote takes host out of daemon.remotes. It reports false when the
// host was not there.
func RemoveRemote(file, host string) (bool, error) {
	if file == "" {
		file = paths.ConfigFile()
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := Parse(b)
	if err != nil {
		return false, fmt.Errorf("%s: %w", file, err)
	}
	i := slices.Index(c.Daemon.Remotes, host)
	if i < 0 {
		return false, nil
	}
	want := slices.Delete(slices.Clone(c.Daemon.Remotes), i, i+1)
	text := withRemotes(string(b), want)
	after, err := Parse([]byte(text))
	if err != nil || !slices.Equal(after.Daemon.Remotes, want) {
		return false, fmt.Errorf("cannot edit %s: remove %q from daemon.remotes by hand", file, host)
	}
	return true, replaceFile(file, text)
}

// withRemotes writes the list into the [daemon] table of a config text.
func withRemotes(text string, hosts []string) string {
	quoted := make([]string, len(hosts))
	for i, h := range hosts {
		quoted[i] = strconv.Quote(h)
	}
	line := "remotes = [" + strings.Join(quoted, ", ") + "]"
	head := daemonHeader.FindStringIndex(text)
	if head == nil {
		if text != "" {
			text = strings.TrimRight(text, "\n") + "\n\n"
		}
		return text + "[daemon]\n" + line + "\n"
	}
	start, end := head[1], len(text)
	if next := nextTable.FindStringIndex(text[start:]); next != nil {
		end = start + next[0]
	}
	body := text[start:end]
	if key := remotesKey.FindStringIndex(body); key != nil {
		body = body[:key[0]] + line + body[key[1]:]
	} else {
		body = "\n" + line + body
	}
	return text[:start] + body + text[end:]
}

// replaceFile swaps the file in one step and keeps its mode. A config that is
// a symlink stays one.
func replaceFile(file, text string) error {
	mode := fs.FileMode(0o600)
	if real, err := filepath.EvalSymlinks(file); err == nil {
		file = real
	}
	if st, err := os.Stat(file); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
