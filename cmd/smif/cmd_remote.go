package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/daemon"
)

const (
	remoteInstall = `curl -fsSL https://smif.dev/install.sh | sh`
	remoteAwait   = 30 * time.Second // a poll comes every 15 s and may take 12
)

func cmdRemote() *cobra.Command {
	c := &cobra.Command{Use: "remote", Short: "Add another machine to your session list"}
	c.AddCommand(&cobra.Command{
		Use:   "add <ssh-host>",
		Short: "Set up ssh and smif on a machine, then show its sessions here",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return remoteAdd(args[0]) },
	})
	return c
}

// remoteAdd does the whole setup (ADR-0023): the ssh key, smif on the other
// side, the config, and a look at what the daemon itself can reach.
func remoteAdd(host string) error {
	if !config.ValidRemote(host) {
		return fmt.Errorf("%q is not an ssh host name", host)
	}
	if err := remoteLogin(host); err != nil {
		return err
	}
	n, err := remoteSessions(host)
	if err != nil {
		return err
	}
	fmt.Printf("%s answers, with %d sessions.\n", host, n)
	added, err := config.AddRemote("", host)
	if err != nil {
		return err
	}
	if !added {
		fmt.Printf("%s is already in daemon.remotes.\n", host)
	}
	ensureDaemon()
	var r map[string]any
	if err := call("admin.reload", nil, &r, 30*time.Second); err != nil {
		return notRunning(err)
	}
	fmt.Println("Waiting for the daemon to reach it...")
	return awaitRemote(host)
}

// remoteLogin leaves a login that asks for nothing, which is what the daemon
// needs. Only a refused key or an unknown host key can be fixed from here.
func remoteLogin(host string) error {
	out, err := remoteRun(host, "true")
	if err == nil {
		return nil
	}
	if !strings.Contains(out, "Permission denied") && !strings.Contains(out, "Host key verification failed") {
		return fmt.Errorf("cannot reach %s: %s\nOn a Mac, turn on System Settings > General > Sharing > Remote Login. On Linux, start sshd", host, lastLine(out, err))
	}
	fmt.Printf("%s needs your ssh key. Log in once, and it will not ask again.\n", host)
	c := exec.Command(copyIDCommand(), host)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("ssh-copy-id: %v\nWith no ssh key yet, run `ssh-keygen -t ed25519` and try again", err)
	}
	if out, err := remoteRun(host, "true"); err != nil {
		return fmt.Errorf("%s still asks for a login: %s", host, lastLine(out, err))
	}
	return nil
}

// remoteSessions counts the sessions of a host, and installs smif there when
// it is missing and you say yes.
func remoteSessions(host string) (int, error) {
	stdout, stderr, err := remoteOutput(host, daemon.RemoteCommand)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 127 {
		fmt.Printf("%s has no smif. Install the latest release there, in ~/.local/bin? [Y/n] ", host)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); line == "" || (a != "" && a != "y" && a != "yes") {
			return 0, fmt.Errorf("nothing changed. To install it yourself, run this on %s: %s", host, remoteInstall)
		}
		argv := daemon.RemoteArgv(host, remoteInstall)
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return 0, fmt.Errorf("install on %s: %v", host, err)
		}
		stdout, stderr, err = remoteOutput(host, daemon.RemoteCommand)
	}
	if err != nil {
		return 0, fmt.Errorf("smif on %s: %s", host, lastLine(stderr, err))
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(stdout, &rows); err != nil {
		return 0, fmt.Errorf("smif on %s gave no session list: %v", host, err)
	}
	return len(rows), nil
}

// awaitRemote asks the daemon, because it runs as a service and may not have
// what this shell has, like an ssh agent.
func awaitRemote(host string) error {
	last := "no answer yet"
	for deadline := time.Now().Add(remoteAwait); time.Now().Before(deadline); time.Sleep(time.Second) {
		var h api.HealthView
		if err := call("admin.health", nil, &h, 10*time.Second); err != nil {
			return notRunning(err)
		}
		for _, p := range h.Remotes {
			if p.Name != host {
				continue
			}
			if p.State == "ok" {
				fmt.Printf("Done: the sessions of %s are in your list, read only.\n", host)
				return nil
			}
			last = p.Detail
		}
	}
	return fmt.Errorf("%s works from this shell, but the daemon cannot reach it: %s\nA key with a passphrase needs one the daemon can use, or use a key with none. `smif doctor` shows the state", host, last)
}

func remoteRun(host, command string) (string, error) {
	stdout, stderr, err := remoteOutput(host, command)
	return string(stdout) + string(stderr), err
}

func remoteOutput(host, command string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	argv := daemon.RemoteArgv(host, command)
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	return stdout.Bytes(), stderr.String(), err
}

func copyIDCommand() string {
	if p, ok := os.LookupEnv("SMIF_SSH_COPY_ID"); ok {
		return p
	}
	return "ssh-copy-id"
}

func lastLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return err.Error()
	}
	return out[strings.LastIndex(out, "\n")+1:]
}
