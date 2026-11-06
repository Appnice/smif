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

const remoteNameMax = 32

var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidRemoteName says whether a name can stand for a machine.
func ValidRemoteName(name string) bool {
	return len(name) <= remoteNameMax && remoteNameRe.MatchString(name)
}

// namesHeader is the table of names. A name is the last key in a line of its own.
var namesHeader = regexp.MustCompile(`(?m)^[ \t]*\[daemon\.remote_names\][ \t]*(#.*)?$`)

// SetRemoteName names a machine, and an empty name takes the name away. The
// rest of the file stays as it is.
func SetRemoteName(file, host, name string) error {
	if file == "" {
		file = paths.ConfigFile()
	}
	if name != "" && !ValidRemoteName(name) {
		return fmt.Errorf("%q: a name is letters, digits, dots or dashes, at most %d", name, remoteNameMax)
	}
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := withName(string(b), host, name)
	after, err := Parse([]byte(text))
	if err != nil || after.Daemon.RemoteNames[host] != name {
		return fmt.Errorf("cannot edit %s: set daemon.remote_names by hand", file)
	}
	return replaceFile(file, text)
}

func withName(text, host, name string) string {
	key := strconv.Quote(host)
	line := ""
	if name != "" {
		line = key + " = " + strconv.Quote(name) + "\n"
	}
	head := namesHeader.FindStringIndex(text)
	if head == nil {
		if name == "" {
			return text
		}
		if text != "" {
			text = strings.TrimRight(text, "\n") + "\n\n"
		}
		return text + "[daemon.remote_names]\n" + line
	}
	start, end := head[1], len(text)
	if next := nextTable.FindStringIndex(text[start:]); next != nil {
		end = start + next[0]
	}
	body := text[start:end]
	old := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=.*\n?`)
	if old.MatchString(body) {
		body = old.ReplaceAllLiteralString(body, line)
	} else if strings.HasPrefix(body, "\n") {
		body = "\n" + line + body[1:]
	} else {
		body = "\n" + line + body
	}
	return text[:start] + body + text[end:]
}

var (
	daemonHeader = regexp.MustCompile(`(?m)^[ \t]*\[daemon\][ \t]*(#.*)?$`)
	nextTable    = regexp.MustCompile(`(?m)^[ \t]*\[`)
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
	text := withList(string(b), "remotes", want)
	after, err := Parse([]byte(text))
	if err != nil || !slices.Equal(after.Daemon.Remotes, want) {
		return false, fmt.Errorf("cannot edit %s: add %q to daemon.remotes by hand", file, host)
	}
	return true, replaceFile(file, text)
}

// SetRemoteWrite says whether you can type into that machine's sessions.
func SetRemoteWrite(file, host string, on bool) error {
	if file == "" {
		file = paths.ConfigFile()
	}
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	c, err := Parse(b)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	want := slices.DeleteFunc(slices.Clone(c.Daemon.RemoteWrite), func(h string) bool { return h == host })
	if on {
		want = append(want, host)
	}
	if slices.Equal(want, c.Daemon.RemoteWrite) {
		return nil
	}
	text := withList(string(b), "remote_write", want)
	after, err := Parse([]byte(text))
	if err != nil || !slices.Equal(after.Daemon.RemoteWrite, want) {
		return fmt.Errorf("cannot edit %s: set daemon.remote_write by hand", file)
	}
	return replaceFile(file, text)
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
	writers := slices.DeleteFunc(slices.Clone(c.Daemon.RemoteWrite), func(h string) bool { return h == host })
	text := withName(withList(string(b), "remotes", want), host, "")
	if len(writers) != len(c.Daemon.RemoteWrite) {
		text = withList(text, "remote_write", writers)
	}
	after, err := Parse([]byte(text))
	if err != nil || !slices.Equal(after.Daemon.Remotes, want) {
		return false, fmt.Errorf("cannot edit %s: remove %q from daemon.remotes by hand", file, host)
	}
	return true, replaceFile(file, text)
}

// withList writes a list of hosts into the [daemon] table of a config text.
func withList(text, key string, hosts []string) string {
	quoted := make([]string, len(hosts))
	for i, h := range hosts {
		quoted[i] = strconv.Quote(h)
	}
	line := key + " = [" + strings.Join(quoted, ", ") + "]"
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
	found := regexp.MustCompile(`(?ms)^[ \t]*` + key + `[ \t]*=[ \t]*\[.*?\]`)
	if at := found.FindStringIndex(body); at != nil {
		body = body[:at[0]] + line + body[at[1]:]
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
