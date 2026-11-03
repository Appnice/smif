package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/mailroom/internal/api"
)

// ADR-0012: the human sees which sessions wait for input and types a
// prompt into one.

func cmdSessions() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List the live Claude Code sessions: agent, idle or busy, terminal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureDaemon()
			var ss []api.SessionView
			if err := call("session.list", nil, &ss, 10*time.Second); err != nil {
				return err
			}
			if flagJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(ss)
			}
			if len(ss) == 0 {
				fmt.Println("No live sessions.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "agent\tsession\tname\tstate\tsince\tterminal\tprompt")
			for _, s := range ss {
				since := "-"
				if !s.TurnAt.IsZero() {
					since = time.Since(s.TurnAt).Round(time.Second).String()
				}
				pr := "yes"
				if !s.CanPrompt {
					pr = "no: " + s.Why
				}
				agent := s.Agent
				if agent == "" {
					agent = "-"
				}
				name := s.Title
				if name == "" {
					name = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", agent, api.ShortSession(s.SessionID), name, s.Turn, since,
					strings.TrimSpace(s.Term+" "+s.TTY), pr)
			}
			return tw.Flush()
		},
	}
}

func cmdPrompt() *cobra.Command {
	var session string
	c := &cobra.Command{
		Use:   "prompt <session name> <text…>",
		Short: "Write to a session: typed in when idle (Terminal.app, iTerm2, tmux), else a worker with its context answers",
		Long: "The first word names the session (its name from `mailroom sessions`, case does not matter; quote a\n" +
			"name with spaces); --session picks one by id prefix instead. An idle session gets one line typed in and\n" +
			"Enter (Terminal.app, iTerm2 or tmux; claude in the foreground of its tab). A busy session is not touched: a worker resumes\n" +
			"a fork of it (read only) and answers (ADR-0013).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := api.SessionSendParams{Name: args[0], Text: strings.Join(args[1:], " ")}
			if session != "" {
				var ss []api.SessionView
				if err := call("session.list", nil, &ss, 10*time.Second); err != nil {
					return err
				}
				p.Name = ""
				for _, s := range ss {
					if strings.HasPrefix(s.SessionID, session) {
						if p.SessionID != "" {
							return fmt.Errorf("session prefix %q is not unique", session)
						}
						p.SessionID = s.SessionID
					}
				}
				if p.SessionID == "" {
					return fmt.Errorf("no live session %q", session)
				}
			}
			var r api.SessionSendResult
			if err := call("session.send", p, &r, 30*time.Second); err != nil {
				return err
			}
			if r.Mode == "worker" {
				fmt.Printf("%s is busy: a worker with its context answers here; wait for it with `mailroom await %s`\n",
					args[0], r.MessageID)
				return nil
			}
			fmt.Printf("sent to %s (session %s)\n", nonEmptyStr(r.Agent, args[0]), api.ShortSession(r.SessionID))
			return nil
		},
	}
	c.Flags().StringVar(&session, "session", "", "session id prefix, when the agent has several idle sessions")
	return c
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
