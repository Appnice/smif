package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
)

func cmdStats() *cobra.Command {
	var days int
	var since string
	c := &cobra.Command{
		Use:   "stats",
		Short: "How much went through Smif: handled without you, needed you, runs, tokens, per agent and day",
		Long: "Counts over a window (default the last 14 days), straight from the daemon's store.\n" +
			"inbound: asks, tells and events created in the window (replies counted apart).\n" +
			"without you: done and never in needs_human. needed you: in needs_human at least once (from the audit trail).\n" +
			"The work share is without you / (without you + needed you + dead); cancels and guard rejections are not work.\n" +
			"merged: repeats coalesced into an open message (dedupe). Tokens include cache reads and writes; the cost is\n" +
			"what claude reports (API-equivalent; not billed on a subscription).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := api.StatsParams{Since: time.Now().AddDate(0, 0, -days)}
			if since != "" {
				t, err := time.ParseInLocation("2006-01-02", since, time.Local)
				if err != nil {
					return fmt.Errorf("--since wants YYYY-MM-DD: %v", err)
				}
				p.Since = t
			}
			var r api.StatsResult
			if err := call("stats.get", p, &r, 10*time.Second); err != nil {
				return err
			}
			if flagJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			printStats(r)
			return nil
		},
	}
	c.Flags().IntVar(&days, "days", 14, "window: the last N days")
	c.Flags().StringVar(&since, "since", "", "window start date, YYYY-MM-DD (overrides --days)")
	return c
}

func pct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (n*100+of/2)/of)
}

func kTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func printStats(r api.StatsResult) {
	t := r.Total
	fmt.Printf("Smif stats since %s (%.1f days)\n\n", r.Since.Local().Format("2006-01-02 15:04"), r.Until.Sub(r.Since).Hours()/24)
	merged := ""
	if t.Merged > 0 {
		merged = fmt.Sprintf(" (+%d repeats merged)", t.Merged)
	}
	fmt.Printf("  inbound       %d messages%s, %d replies\n", t.Inbound, merged, r.Replies)
	var by []string
	for h, n := range t.AutoBy {
		by = append(by, fmt.Sprintf("%s %d", h, n))
	}
	sort.Strings(by)
	fmt.Printf("  without you   %d (%s of the work)  %s\n", t.Auto, pct(t.Auto, t.Auto+t.NeededYou+t.Dead), strings.Join(by, " · "))
	fmt.Printf("  needed you    %d  answered %d · waiting %d · cancelled %d\n", t.NeededYou, t.Answered, t.Waiting, t.CancelledNeeded)
	fmt.Printf("  other         dead %d · cancelled by you %d · stopped by a guard %d · expired %d · in progress %d\n",
		t.Dead, t.Cancelled, t.Rejected, t.Expired, t.InProgress)
	fmt.Printf("  runs          %d worker, %d command (%d failed)\n", t.WorkerRuns, t.CommandRuns, t.FailedRuns)
	fmt.Printf("  tokens        %s in (incl. cache) · %s out · $%.4f reported\n", kTokens(t.InputTokens), kTokens(t.OutputTokens), t.CostUSD)
	ss := r.Sessions
	fmt.Printf("  sessions      %d typed into idle ones · %d asked a worker (busy) · %d agent asks · %d tabs shown\n\n",
		ss.Typed, ss.WorkerAsks, ss.AgentAsks, ss.Focus)
	table := func(title string, rows []api.StatsRow) {
		if len(rows) == 0 {
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\tinbound\twithout you\tneeded you\tdead\tmerged\truns\ttokens in\t$\n", strings.ToUpper(title))
		for _, x := range rows {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%.4f\n", x.Key, x.Inbound, x.Auto, x.NeededYou, x.Dead, x.Merged,
				x.Runs, kTokens(x.InputTokens), x.CostUSD)
		}
		w.Flush()
		fmt.Println()
	}
	table("agent", r.Agents)
	table("day", r.Days)
	if r.Since.Before(r.KeptFrom) {
		fmt.Printf("Note: data before %s is past message_retention and no longer counted.\n", r.KeptFrom.Local().Format("2006-01-02"))
	}
}
