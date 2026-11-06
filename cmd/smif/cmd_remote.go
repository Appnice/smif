package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/harness"
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
	}, &cobra.Command{
		Use:   "name <ssh-host> [name]",
		Short: "Name a machine in your list, or take its name away",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return remoteName(args[0], strings.Join(args[1:], ""))
		},
	}, &cobra.Command{
		Use:   "write <ssh-host> <on|off>",
		Short: "Allow or stop typing from here into that machine's sessions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "on" && args[1] != "off" {
				return errors.New("say on or off")
			}
			return remoteSetup(args[0], args[1] == "on")
		},
	})
	return c
}

// cmdRemoteRead is what the daemon's key may run on a machine (ADR-0024). ssh
// puts what the other side asked for in SSH_ORIGINAL_COMMAND, and this reads
// only a few requests from it, or else gives the session list. --write comes
// from the key's line in authorized_keys, never from the request.
func cmdRemoteRead() *cobra.Command {
	var write bool
	c := &cobra.Command{
		Use: "remote-read", Hidden: true, Short: "Answer another machine's smif",
		RunE: func(cmd *cobra.Command, args []string) error {
			if orig := os.Getenv("SSH_ORIGINAL_COMMAND"); orig != "" {
				_, after, ok := strings.Cut(orig, "remote-read ")
				args = nil
				if ok {
					args = strings.Fields(after)
				}
			}
			ensureDaemon()
			op := ""
			if len(args) > 0 {
				op = args[0]
			}
			switch op {
			case "check":
				return nil
			case "tail":
				if len(args) != 3 || !harness.ValidID(args[1]) {
					return errors.New("bad request")
				}
				limit, err := strconv.Atoi(args[2])
				if err != nil {
					return err
				}
				var t api.SessionTail
				if err := call("session.tail", api.SessionTailParams{SessionID: args[1], Limit: limit}, &t, 10*time.Second); err != nil {
					return err
				}
				printJSON(t)
				return nil
			case "send", "queue", "cancel", "new":
				if !write {
					return errors.New("writing is off for this machine")
				}
				return remoteWrite(op, args[1:])
			}
			var rows []api.SessionView
			if err := call("session.list", nil, &rows, 10*time.Second); err != nil {
				return err
			}
			printJSON(rows)
			return nil
		},
	}
	c.Flags().BoolVar(&write, "write", false, "")
	return c
}

// remoteWrite takes one line from stdin to this machine's daemon.
func remoteWrite(op string, args []string) error {
	if op == "new" {
		var p api.SessionNewParams
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<16)).Decode(&p); err != nil {
			return err
		}
		// Only the folder and the name come from the other side.
		var r map[string]any
		return call("session.new", api.SessionNewParams{Dir: p.Dir, Name: p.Name, Term: "tmux"}, &r, 20*time.Second)
	}
	if len(args) != 1 {
		return errors.New("bad request")
	}
	if op == "cancel" {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return err
		}
		return call("session.queue.cancel", api.SessionQueueCancelParams{ID: id}, nil, 10*time.Second)
	}
	if !harness.ValidID(args[0]) {
		return errors.New("bad request")
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return err
	}
	if op == "queue" {
		var r api.SessionQueueResult
		if err := call("session.queue", api.SessionQueueParams{SessionID: args[0], Text: string(b)}, &r, 10*time.Second); err != nil {
			return err
		}
		printJSON(r)
		return nil
	}
	var r api.SessionSendResult
	if err := call("session.send", api.SessionSendParams{SessionID: args[0], Text: string(b), Async: true}, &r, 15*time.Second); err != nil {
		return err
	}
	printJSON(r)
	return nil
}

func remoteAdd(host string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	return remoteSetup(host, slices.Contains(c.Daemon.RemoteWrite, host))
}

func remoteSetup(host string, write bool) error {
	if !config.ValidRemote(host) {
		return fmt.Errorf("%q is not an ssh host name", host)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ensureDaemon()
	s := &machines.Setup{Host: host, Write: write, Ask: newTerminalAsk(), Daemon: machineDaemon{}, Report: printStep}
	if err := s.Run(ctx); err != nil {
		if errors.Is(err, machines.ErrStopped) {
			return errors.New("stopped, nothing was added")
		}
		return err
	}
	how := "read only"
	if write {
		how = "and you can write to them"
	}
	fmt.Printf("Done: the sessions of %s are in your list, %s.\n", s.Host, how)
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
	fmt.Printf("%s left your list. Its key stays there and can only read sessions: remove the smif-daemon line from ~/.ssh/authorized_keys.\n", host)
	return nil
}

func remoteName(host, name string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if !slices.Contains(c.Daemon.Remotes, host) {
		return fmt.Errorf("%s is not in daemon.remotes", host)
	}
	if err := config.SetRemoteName("", host, name); err != nil {
		return err
	}
	return machineDaemon{}.Reload()
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
