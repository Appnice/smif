package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/paths"
)

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Agents, queues, running runs, budgets and DLQ size",
		RunE: func(cmd *cobra.Command, args []string) error {
			var s api.StatusView
			if err := call("agent.status", nil, &s, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(s)
				return nil
			}
			claude := "ok"
			if !s.Daemon.ClaudeOK {
				claude = "UNAVAILABLE (" + strings.Join(s.Daemon.ClaudeProblems, "; ") + ")"
			}
			fmt.Printf("daemon %s (pid %d, up %s), schema %d, claude %s %s\n", s.Daemon.Version, s.Daemon.PID,
				time.Since(s.Daemon.StartedAt).Round(time.Second), s.Daemon.SchemaVersion, s.Daemon.ClaudeVersion, claude)
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "AGENT\tQUEUED\tRUNNING\tNEEDS YOU\tDEAD\tSPENT TODAY\tBUDGET LEFT\tSESSION")
			for _, a := range s.Agents {
				left := "-"
				if a.BudgetLeft != nil {
					left = fmt.Sprintf("$%.2f", *a.BudgetLeft)
				}
				sess := ""
				if a.Interactive {
					sess = "interactive"
				}
				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t$%.2f\t%s\t%s\n", a.ID, a.Queued, a.Running, a.NeedsHuman, a.DeadLetters, a.SpentUSD, left, sess)
			}
			w.Flush()
			for _, r := range s.Running {
				fmt.Printf("running: %s %s %s (attempt %d, %s)\n", r.Agent, short(r.ID), r.Handler, r.Attempt, ago(r.StartedAt))
			}
			m := s.Metrics
			fmt.Printf("24h: %d runs, success %.0f%%, p50 %.1fs, p95 %.1fs, %d coalesced; queue %d; DLQ %d\n",
				m.RunsLast24h, m.SuccessRate*100, m.P50Seconds, m.P95Seconds, m.Coalesced24h, m.QueueDepth, m.DLQ)
			return nil
		},
	}
}

func printMessages(ms []api.MessageView) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN\tFROM\tTO\tKIND\tINTENT\tSTATE\tSUMMARY")
	for _, m := range ms {
		sum := m.Summary
		if sum == "" {
			sum = m.Subject
		}
		if m.State == "rejected" || m.State == "dead_letter" || m.State == "expired" {
			sum = m.Reason
		}
		if m.HoldReason != "" && m.State == "queued" {
			sum = "held: " + m.HoldReason
		}
		if m.Coalesced > 0 {
			sum += fmt.Sprintf(" (+%d coalesced)", m.Coalesced)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", short(m.ID), ago(m.CreatedAt), m.From.Agent, m.To, m.Kind, m.Intent, m.State, oneLine(sum))
	}
	w.Flush()
}

func cmdInbox() *cobra.Command {
	var limit int
	var state string
	c := &cobra.Command{
		Use: "inbox [agent]", Short: "Recent items with state and summary", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := api.ListParams{Limit: limit, State: state}
			if len(args) == 1 {
				p.Agent = args[0]
			}
			var ms []api.MessageView
			if err := call("thread.list", p, &ms, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(ms)
				return nil
			}
			printMessages(ms)
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 30, "how many")
	c.Flags().StringVar(&state, "state", "", "filter by state")
	return c
}

func cmdDLQ() *cobra.Command {
	return &cobra.Command{
		Use: "dlq", Short: "List dead letters",
		RunE: func(cmd *cobra.Command, args []string) error {
			var ms []api.MessageView
			if err := call("thread.list", api.ListParams{State: "dead_letter", Limit: 100}, &ms, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(ms)
				return nil
			}
			if len(ms) == 0 {
				fmt.Println("no dead letters")
				return nil
			}
			printMessages(ms)
			fmt.Println("requeue with `mailroom retry <id>`")
			return nil
		},
	}
}

func cmdThread() *cobra.Command {
	return &cobra.Command{
		Use: "thread <id>", Short: "A thread with its messages, runs and results", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var t api.ThreadView
			if err := call("thread.get", api.IDParams{ID: args[0]}, &t, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(t)
				return nil
			}
			fz := ""
			if t.Frozen {
				fz = " FROZEN: " + t.FrozenReason
			}
			fmt.Printf("thread %s%s\n", t.ThreadID, fz)
			runs := map[string][]api.RunView{}
			for _, r := range t.Runs {
				runs[r.MessageID] = append(runs[r.MessageID], r)
			}
			for _, m := range t.Messages {
				fmt.Printf("\n%s  %s -> %s  %s %s  [%s] hop %d  %s\n", m.ID, m.From.Agent, m.To, m.Kind, m.Intent, m.State, m.Hop, m.CreatedAt.Local().Format("15:04:05"))
				fmt.Printf("  subject: %s\n", m.Subject)
				if m.Body != "" {
					fmt.Printf("  body: %s\n", indent(m.Body))
				}
				if m.Summary != "" {
					fmt.Printf("  summary: %s\n", m.Summary)
				}
				if m.HumanQuestion != "" {
					fmt.Printf("  needs you: %s  (answer: mailroom reply %s \"...\")\n", m.HumanQuestion, short(m.ID))
				}
				if m.Reason != "" {
					fmt.Printf("  reason: %s\n", m.Reason)
				}
				if m.Coalesced > 0 {
					fmt.Printf("  coalesced: %d repeats\n", m.Coalesced)
				}
				for _, a := range m.Attachments {
					fmt.Printf("  attachment: %s (%d bytes) %s\n", a.Name, a.Size, a.SHA256)
				}
				for _, r := range runs[m.ID] {
					fmt.Printf("  run %s %s attempt %d %s", short(r.ID), r.Handler, r.Attempt, r.State)
					if r.Branch != "" {
						fmt.Printf(" branch %s", r.Branch)
					}
					if r.Error != "" {
						fmt.Printf(" error: %s", oneLine(r.Error))
					}
					fmt.Println()
				}
			}
			return nil
		},
	}
}

func indent(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n        ")
}

func cmdRun() *cobra.Command {
	var transcript bool
	c := &cobra.Command{
		Use: "run <id>", Short: "A run record, or its transcript", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r api.RunView
			if err := call("run.get", api.IDParams{ID: args[0]}, &r, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if transcript {
				if r.Transcript == "" {
					return fmt.Errorf("run %s has no transcript (command handler); stderr:\n%s", r.ID, r.Stderr)
				}
				f, err := os.Open(r.Transcript)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(os.Stdout, f)
				return err
			}
			if flagJSON {
				printJSON(r)
				return nil
			}
			fmt.Printf("run %s  agent %s  %s/%s  attempt %d  %s\n", r.ID, r.Agent, r.Handler, r.Profile, r.Attempt, r.State)
			fmt.Printf("message %s  started %s", r.MessageID, r.StartedAt.Local().Format(time.RFC3339))
			if r.EndedAt != nil {
				fmt.Printf("  took %s", r.EndedAt.Sub(r.StartedAt).Round(100*time.Millisecond))
			}
			fmt.Printf("\ncost $%.4f  tokens in %d out %d\n", r.CostUSD, r.InputTokens, r.OutputTokens)
			if r.SessionID != "" {
				fmt.Printf("session %s (open with: claude --resume %s, from %s)\n", r.SessionID, r.SessionID, r.Workspace)
			}
			if r.ArchivedSession != "" {
				fmt.Printf("rotated out session %s\n", r.ArchivedSession)
			}
			if r.Branch != "" {
				fmt.Printf("branch %s head %s\n%s\n", r.Branch, r.HeadSHA, r.Diffstat)
			}
			if len(r.Result) > 0 {
				fmt.Printf("result %s\n", string(r.Result))
			}
			if r.Error != "" {
				fmt.Printf("error %s\n", r.Error)
			}
			if r.Transcript != "" {
				fmt.Printf("transcript %s\n", r.Transcript)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&transcript, "transcript", false, "print the raw stream transcript")
	return c
}

func cmdDigest() *cobra.Command {
	var agent, cwd, since string
	var peek bool
	c := &cobra.Command{
		Use: "digest", Short: "Summaries of handled items; advances the read cursor",
		RunE: func(cmd *cobra.Command, args []string) error {
			var d api.DigestResult
			if agent == "" {
				agent = os.Getenv("MAILROOM_AGENT") // wins over --cwd, like in the hooks
			}
			p := api.DigestParams{Agent: agent, Cwd: cwd, Since: since, Advance: !peek}
			if err := call("digest.get", p, &d, 10*time.Second); err != nil {
				fmt.Println("Mailroom is not reachable:", err)
				return nil
			}
			if flagJSON {
				printJSON(d)
				return nil
			}
			fmt.Print(renderDigest(&d))
			return nil
		},
	}
	c.Flags().StringVar(&agent, "agent", "", "one agent")
	c.Flags().StringVar(&cwd, "cwd", "", "the agent whose repo contains this directory")
	c.Flags().StringVar(&since, "since", "last", "last (since the cursor) or all")
	c.Flags().BoolVar(&peek, "peek", false, "do not advance the cursor")
	return c
}

func renderDigest(d *api.DigestResult) string {
	var b strings.Builder
	if len(d.Items) == 0 {
		fmt.Fprintf(&b, "Mailroom: nothing new for %s.\n", strings.Join(d.Agents, ", "))
		return b.String()
	}
	byAgent := map[string][]api.DigestItem{}
	var order []string
	for _, it := range d.Items {
		if _, ok := byAgent[it.Agent]; !ok {
			order = append(order, it.Agent)
		}
		byAgent[it.Agent] = append(byAgent[it.Agent], it)
	}
	sort.Strings(order)
	for _, ag := range order {
		fmt.Fprintf(&b, "### %s (%d)\n", ag, len(byAgent[ag]))
		for _, it := range byAgent[ag] {
			fmt.Fprintf(&b, "- [%s] %s %s from %s: %s", it.State, it.Kind, it.Intent, it.From, it.Subject)
			if it.Coalesced > 0 {
				fmt.Fprintf(&b, " (x%d)", it.Coalesced+1)
			}
			b.WriteString("\n")
			if it.Summary != "" && it.Summary != it.Subject {
				fmt.Fprintf(&b, "  %s\n", it.Summary)
			}
			if it.HumanQuestion != "" {
				fmt.Fprintf(&b, "  NEEDS YOU: %s -> mailroom reply %s \"<answer>\"\n", it.HumanQuestion, short(it.MessageID))
			}
			if it.Branch != "" {
				fmt.Fprintf(&b, "  branch %s -> mailroom diff %s / mailroom apply %s\n", it.Branch, short(it.RunID), short(it.RunID))
			}
		}
	}
	return b.String()
}

func cmdDiff() *cobra.Command {
	return &cobra.Command{
		Use: "diff <run>", Short: "Show a worker branch against the main checkout", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]string
			if err := call("run.diff", api.IDParams{ID: args[0]}, &r, 30*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(r)
				return nil
			}
			fmt.Printf("# %s in %s\n%s\n", r["branch"], r["repo"], r["diff"])
			return nil
		},
	}
}

func cmdApply() *cobra.Command {
	return &cobra.Command{
		Use: "apply <run>", Short: "Merge a worker branch into the main checkout's current branch (refused on a dirty tree)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]string
			if err := call("run.apply", api.IDParams{ID: args[0]}, &r, 60*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(r)
				return nil
			}
			fmt.Printf("merged %s into %s (HEAD %s)\n", r["branch"], r["repo"], r["head"])
			return nil
		},
	}
}

func cmdAgents() *cobra.Command {
	return &cobra.Command{
		Use: "agents", Short: "Agents and the intents they handle",
		RunE: func(cmd *cobra.Command, args []string) error {
			var as []api.AgentView
			if err := call("agent.list", nil, &as, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(as)
				return nil
			}
			for _, a := range as {
				fmt.Printf("%s  %s\n  %s\n", a.ID, a.Repo, strings.Join(a.Intents, "\n  "))
			}
			return nil
		},
	}
}

func cmdTail() *cobra.Command {
	var follow bool
	c := &cobra.Command{
		Use: "tail", Short: "Follow the audit log",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := filepath.Join(paths.DataDir(), "audit")
			var cur string
			var f *os.File
			var r *bufio.Reader
			for {
				ents, _ := os.ReadDir(dir)
				latest := ""
				for _, e := range ents {
					if strings.HasSuffix(e.Name(), ".jsonl") && e.Name() > latest {
						latest = e.Name()
					}
				}
				if latest != "" && latest != cur {
					if f != nil {
						f.Close()
					}
					var err error
					f, err = os.Open(filepath.Join(dir, latest))
					if err != nil {
						return err
					}
					r = bufio.NewReader(f)
					cur = latest
				}
				if r != nil {
					for {
						line, err := r.ReadString('\n')
						if line != "" {
							fmt.Print(line)
						}
						if err != nil {
							break
						}
					}
				}
				if !follow {
					return nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", true, "keep following")
	return c
}
