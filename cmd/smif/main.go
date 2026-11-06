// Command smif is the Smif CLI, daemon, MCP server, hooks and status
// line in one static binary (RFC 4.1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/daemon"
	"github.com/appnice/smif/internal/machines"
	"github.com/appnice/smif/internal/paths"
	"github.com/appnice/smif/internal/ui"
)

var (
	flagJSON bool
	flagAs   string
)

func main() {
	if sock := os.Getenv(machines.AskpassEnv); sock != "" {
		os.Exit(machines.Askpass(sock)) // ssh asks for a password (ADR-0024)
	}
	root := &cobra.Command{
		Use:           "smif",
		Short:         "Event-driven, hard-isolated messaging between Claude Code sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       daemon.Version,
		Args:          cobra.NoArgs,
		// Plain `smif` opens the UI in a terminal, like lazygit or k9s.
		// In a pipe or script it prints one summary instead.
		RunE: func(cmd *cobra.Command, args []string) error {
			if isTTY() {
				return runUI(time.Second)
			}
			src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return humanClient(2 * time.Second) }, RecentLimit: 1}
			defer src.Close()
			err := watchSummary(context.Background(), cmd.OutOrStdout(), src, time.Second, 5, true)
			fmt.Fprintln(cmd.ErrOrStderr(), "(no terminal: one summary; `smif watch` follows, `smif --help` lists commands)")
			return err
		},
	}
	root.PersistentFlags().BoolVar(&flagJSON, "json", false, "print JSON")
	root.AddCommand(
		cmdDaemon(), cmdInit(), cmdInstall(), cmdUninstall(), cmdStatus(), cmdInbox(), cmdThread(), cmdRun(),
		cmdDigest(), cmdDiff(), cmdApply(), cmdReply(), cmdSend("send", "tell"), cmdSend("ask", "ask"), cmdSend("emit", "event"),
		cmdAwait(), cmdRetry(), cmdCancel(), cmdDLQ(), cmdTail(), cmdDoctor(), cmdConfig(), cmdMCP(), cmdHook(), cmdStatusline(),
		cmdBlob(), cmdAgents(), cmdGC(), cmdReload(), cmdUI(), cmdWatch(), cmdStats(), cmdSessions(), cmdPrompt(), cmdNotify(), cmdRemote(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "smif:", err)
		os.Exit(1)
	}
}

// humanClient dials with the human token (CLI, hooks, status line).
func humanClient(timeout time.Duration) (*api.Client, error) {
	tok, err := auth.ReadToken(auth.HumanPrincipal)
	if err != nil {
		return nil, err
	}
	return api.Dial(paths.Socket(), api.Auth{Agent: auth.HumanPrincipal, Token: tok}, timeout)
}

// senderClient dials as the sender: a worker run token, --as <agent>, or the
// human.
func senderClient() (*api.Client, error) {
	if tok := os.Getenv("SMIF_RUN_TOKEN"); tok != "" {
		return api.Dial(paths.Socket(), api.Auth{Agent: os.Getenv("SMIF_AGENT"), Token: tok}, 3*time.Second)
	}
	as := flagAs
	if as == "" {
		as = os.Getenv("SMIF_AGENT")
	}
	if as == "" || as == auth.HumanPrincipal {
		return humanClient(3 * time.Second)
	}
	tok, err := auth.ReadToken(as)
	if err != nil {
		return nil, err
	}
	return api.Dial(paths.Socket(), api.Auth{Agent: as, Token: tok, Origin: "cli"}, 3*time.Second)
}

func call(method string, params, out any, timeout time.Duration) error {
	c, err := humanClient(3 * time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(method, params, out, timeout)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02")
	}
}

func short(id string) string {
	if len(id) > 10 {
		return id[len(id)-10:]
	}
	return id
}

func loadConfig() (*config.Config, error) { return config.Load("") }

func absBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "smif"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 100 {
		s = s[:99] + "…"
	}
	return s
}

func notRunning(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not reachable") {
		return errors.New(err.Error() + "\nstart it with `smif daemon` or `smif install`")
	}
	return err
}
