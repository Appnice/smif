package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/machines"
)

func cmdRemote() *cobra.Command {
	c := &cobra.Command{Use: "remote", Short: "Add another machine to your session list"}
	c.AddCommand(&cobra.Command{
		Use:   "add [ssh-host]",
		Short: "Add a machine. With no host, the Machines screen opens",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !isTTY() {
					return errors.New("name a host: smif remote add me@host")
				}
				return runMachines()
			}
			return remoteAdd(args[0])
		},
	}, &cobra.Command{
		Use:   "rm <ssh-host>",
		Short: "Take a machine out of your list",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return remoteRemove(args[0]) },
	})
	return c
}

func remoteAdd(host string) error {
	if !config.ValidRemote(host) {
		return fmt.Errorf("%q is not an ssh host name", host)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ensureDaemon()
	s := &machines.Setup{Host: host, Ask: newTerminalAsk(), Daemon: machineDaemon{}, Report: printStep}
	if err := s.Run(ctx); err != nil {
		if errors.Is(err, machines.ErrStopped) {
			return errors.New("stopped, nothing was added")
		}
		return err
	}
	fmt.Printf("Done: the sessions of %s are in your list, read only.\n", s.Host)
	return nil
}

func remoteRemove(host string) error {
	removed, err := config.RemoveRemote("", host)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s is not in daemon.remotes", host)
	}
	var r map[string]any
	if err := call("admin.reload", nil, &r, 30*time.Second); err != nil {
		return notRunning(err)
	}
	fmt.Printf("%s left your list. Its key stays there and can only list sessions: remove the smif-daemon line from ~/.ssh/authorized_keys.\n", host)
	return nil
}

func printStep(e machines.Event) {
	label := ""
	for _, s := range machines.Steps {
		if s.ID == e.Step {
			label = s.Label
		}
	}
	switch e.State {
	case machines.Done:
		fmt.Printf("✓ %s: %s\n", label, e.Detail)
	case machines.Skipped:
		fmt.Printf("! %s: %s\n", label, e.Detail)
	case machines.Failed:
		fmt.Printf("✗ %s\n", label)
	}
}

// terminalAsk asks on this terminal.
type terminalAsk struct{ in *bufio.Reader }

func newTerminalAsk() *terminalAsk { return &terminalAsk{bufio.NewReader(os.Stdin)} }

func (a *terminalAsk) Closed(name string) {
	fmt.Printf("ssh does not answer on %s, or the machine is off. Run this once on it, and smif goes on by itself:\n", name)
	fmt.Printf("  Linux: %s\n  macOS: %s\n", machines.CommandLinux, machines.CommandMac)
}

func (a *terminalAsk) secret(prompt string) (string, bool) {
	fmt.Print(prompt)
	if term.IsTerminal(os.Stdin.Fd()) {
		b, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Println()
		return string(b), err == nil && len(b) > 0
	}
	line, err := a.in.ReadString('\n')
	line = strings.TrimSpace(line)
	return line, err == nil && line != ""
}

func (a *terminalAsk) Login(user, problem string) (string, string, bool) {
	if problem != "" {
		fmt.Println(problem)
	}
	pass, ok := a.secret("Password, used once and not stored: ")
	return user, pass, ok
}

func (a *terminalAsk) Sudo(problem string) (string, bool) {
	if problem != "" {
		fmt.Println(problem)
	}
	return a.secret("Linux stops smif when nobody is logged in. sudo password to keep it running (enter skips): ")
}

func (a *terminalAsk) Install() bool {
	fmt.Print("That machine has no smif. Install the latest release there, in ~/.local/bin? [Y/n] ")
	line, err := a.in.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "" || ans == "y" || ans == "yes"
}

// machineDaemon is the running daemon.
type machineDaemon struct{}

func (machineDaemon) Reload() error {
	var r map[string]any
	return notRunning(call("admin.reload", nil, &r, 30*time.Second))
}

// Remotes is the daemon's last look at each host.
func (machineDaemon) Remotes() ([]api.ProviderCheck, error) {
	var h api.HealthView
	if err := call("admin.health", nil, &h, 10*time.Second); err != nil {
		return nil, notRunning(err)
	}
	return h.Remotes, nil
}

func (d machineDaemon) Reached(host string) (bool, string, error) {
	rows, err := d.Remotes()
	if err != nil {
		return false, "", err
	}
	for _, p := range rows {
		if p.Name == host {
			return p.State == "ok", p.Detail, nil
		}
	}
	return false, "", nil
}
