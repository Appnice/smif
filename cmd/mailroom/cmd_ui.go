package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/appnice/mailroom/internal/paths"
	"github.com/appnice/mailroom/internal/ui"
)

func cmdUI() *cobra.Command {
	var interval time.Duration
	c := &cobra.Command{
		Use:   "ui",
		Short: "Full-screen live view for the human (also what plain `mailroom` opens in a terminal)",
		Long: "Full-screen terminal UI; plain `mailroom` in a terminal opens it too. The start screen lists what needs you,\n" +
			"running workers (with a live card of their tool calls), the queue, today's handled items and dead letters, with\n" +
			"a preview on the right: a answers inline, : opens a command prompt (ask, tell, answer, …; tab completes),\n" +
			"[ ] pick an agent, / filters, v switches to the six-panel dashboard below.\n\n" +
			"Full-screen terminal UI. It polls the daemon with the human token (default every second) and keeps\n" +
			"running while the daemon is down: the header then says DAEMON DOWN and the screen fills in again\n" +
			"when the daemon answers. Keys: tab/shift+tab panel, arrows move, enter opens the thread of a message, a\n" +
			"run (RUNNING), an agent's inbox (AGENTS) or the thread of an audit event (EVENTS, the live feed),\n" +
			"i inbox (f/s filter by agent/state), d digest (m marks it read), a reply, c cancel, t retry; on a run:\n" +
			"1 record, 2 transcript, 3 diff, a apply the worker branch. Cancel, retry and apply ask y/N first.\n" +
			"esc back, r refresh, ? help (every key), q quit. Without a terminal use `mailroom watch`.\n\n" +
			"Set NO_COLOR=1 for no colours: the cursor, the focused panel (▶) and the open tab ([1 RECORD]) still show.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isTTY() {
				return errors.New("mailroom ui needs a terminal; use `mailroom watch` (or `mailroom status --json`) in scripts and pipes")
			}
			return runUI(interval)
		},
	}
	c.Flags().DurationVar(&interval, "interval", time.Second, "time between polls of the daemon")
	return c
}

func isTTY() bool { return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) }

// runUI runs the full-screen UI until q.
func runUI(interval time.Duration) error {
	// NO_COLOR (https://no-color.org) turns colours off; bold and reverse
	// stay for the selection and the focused frame.
	ui.SetupColor(os.Getenv)
	src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return humanClient(2 * time.Second) }}
	defer src.Close()
	_, err := tea.NewProgram(ui.New(src, ui.Options{Interval: interval, SeenFile: filepath.Join(paths.DataDir(), "ui-seen.json")})).Run()
	return err
}
