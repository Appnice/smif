package machines

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/smif/internal/daemon"
	"github.com/appnice/smif/internal/paths"
)

const keyName = daemon.RemoteKey

func home() string { return paths.Home() }

// ensureKey makes the daemon's key once and returns its public half. It has no
// passphrase, because a service has nobody to type one, so it must be able to
// do almost nothing: see keyLine.
func ensureKey(ctx context.Context) (string, error) {
	file := filepath.Join(home(), ".ssh", keyName)
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		c := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "smif-daemon", "-f", file)
		if out, err := c.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	b, err := os.ReadFile(file + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// keyLine is the authorized_keys line. Whatever a holder of the key asks for,
// that machine runs smif remote-read: the session list, or one conversation.
// There is no shell, port forward or terminal, and no way to type.
//
// write adds a line to a session you may type into. It sits in this line, on
// the other machine, so nothing the holder of the key sends can turn it on.
func keyLine(smif, pub string, write bool) string {
	flag := ""
	if write {
		flag = " --write"
	}
	return fmt.Sprintf(`restrict,command="PATH=%s:$PATH %s remote-read%s" %s`, filepath.Dir(smif), smif, flag, pub)
}

// servePassword hands the password to the askpass helper over a socket only
// you can open, so it is never in a file, an argument or the environment.
func servePassword(pass string) (env []string, stop func(), err error) {
	dir, err := os.MkdirTemp("", "smif-ask-")
	if err != nil {
		return nil, nil, err
	}
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(pass))
			c.Close()
		}
	}()
	self, err := os.Executable()
	if err != nil {
		l.Close()
		os.RemoveAll(dir)
		return nil, nil, err
	}
	env = []string{"SSH_ASKPASS=" + self, "SSH_ASKPASS_REQUIRE=force", AskpassEnv + "=" + sock}
	return env, func() {
		l.Close()
		os.RemoveAll(dir)
	}, nil
}

// AskpassEnv marks a run of the smif binary as ssh's askpass helper.
const AskpassEnv = "SMIF_ASKPASS"

// Askpass is the helper: it prints the password for ssh and nothing else.
func Askpass(sock string) int {
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return 1
	}
	defer c.Close()
	b := make([]byte, 4096)
	var out []byte
	for {
		n, err := c.Read(b)
		out = append(out, b[:n]...)
		if err != nil {
			break
		}
	}
	fmt.Println(string(out))
	return 0
}
