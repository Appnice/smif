package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
)

// ADR-0012: the human sees which sessions wait for input and types a
// prompt into one.

func cmdSessions() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List the live sessions: agent, harness, level, idle or busy, terminal",
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
			fmt.Fprintln(tw, "agent\tharness\tlevel\tsession\tname\tstate\tsince\tterminal\tprompt")
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
				state := s.Turn
				if n := len(s.Queue); n > 0 {
					state += fmt.Sprintf(" (%d queued)", n)
				}
				level := s.Level
				if s.Unverified {
					level += " unverified"
				}
				if s.Background {
					level += " background"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", agent, s.Harness, level, api.ShortSession(s.SessionID), name, state, since,
					strings.TrimSpace(s.Term+" "+s.TTY), pr)
			}
			return tw.Flush()
		},
	}
}

func cmdPrompt() *cobra.Command {
	var session string
	var whenIdle bool
	c := &cobra.Command{
		Use:   "prompt (<session name> | --session <prefix>) <text…>",
		Short: "Write to a session: typed in when idle (Terminal.app, iTerm2, tmux, herdr), else a worker with its context answers",
		Long: "The first word names the session (its name from `smif sessions`, case does not matter; quote a\n" +
			"name with spaces). With --session an id prefix picks the session and every word is prompt text:\n" +
			"no session name goes before the text. An idle session gets one line typed in and\n" +
			"Enter (Terminal.app, iTerm2, tmux or herdr; claude in the foreground of its tab). A busy session is not touched: a worker resumes\n" +
			"a fork of it (read only) and answers (ADR-0013). With --when-idle the text waits in a queue and\n" +
			"is typed when the session is idle, one item per idle, for at most 24 h (ADR-0021).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var p api.SessionSendParams
			target := args[0]
			if session == "" {
				p = api.SessionSendParams{Name: args[0], Text: strings.Join(args[1:], " ")}
			} else {
				p.Text = strings.Join(args, " ")
				var ss []api.SessionView
				if err := call("session.list", nil, &ss, 10*time.Second); err != nil {
					return err
				}
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
				target = api.ShortSession(p.SessionID)
			}
			if whenIdle {
				var q api.SessionQueueResult
				err := call("session.queue", api.SessionQueueParams{SessionID: p.SessionID, Name: p.Name, Text: p.Text}, &q, 10*time.Second)
				if err != nil {
					return err
				}
				fmt.Printf("queued for %s (session %s, item %d): it goes in when the session is idle\n",
					nonEmptyStr(q.Agent, target), api.ShortSession(q.SessionID), q.ID)
				return nil
			}
			var r api.SessionSendResult
			if err := call("session.send", p, &r, 30*time.Second); err != nil {
				return err
			}
			if r.Mode == "worker" {
				fmt.Printf("%s is busy: a worker with its context answers here; wait for it with `smif await %s`\n",
					target, r.MessageID)
				return nil
			}
			fmt.Printf("sent to %s (session %s)\n", nonEmptyStr(r.Agent, target), api.ShortSession(r.SessionID))
			return nil
		},
	}
	c.Flags().StringVar(&session, "session", "", "session id prefix, instead of a session name; every word is then prompt text")
	c.Flags().BoolVar(&whenIdle, "when-idle", false, "wait in the queue and type it when the session is idle, instead of asking a fork")
	return c
}

func nonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
