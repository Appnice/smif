package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/ui"
)

func cmdWatch() *cobra.Command {
	var (
		interval time.Duration
		events   bool
		last     int
		once     bool
	)
	c := &cobra.Command{
		Use:   "watch",
		Short: "Plain-text live view for a pipe, a tmux pane or ssh: a summary every few seconds, or --events",
		Long: "Plain-text view of the daemon for places without a full-screen terminal. It reads the socket with the\n" +
			"human token, like `smif ui`.\n\n" +
			"Without --events it prints a compact summary every --interval (default 5s): agents with queued,\n" +
			"running, needs-you and dead counts, spend and budget left, the running runs, the needs-you items,\n" +
			"the dead letters and the last --last audit events.\n\n" +
			"With --events it prints the last --last audit events, then one line per new event as it happens\n" +
			"(polled every --interval, default 1s; the cursor survives a daemon restart, so no event is skipped).\n\n" +
			"While the daemon is down it says so and retries. Ctrl+C (SIGINT) or SIGTERM ends it with exit 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			src := &ui.DaemonSource{Dial: func() (ui.Caller, error) { return humanClient(2 * time.Second) }, RecentLimit: 1}
			defer src.Close()
			out := cmd.OutOrStdout()
			if events {
				if interval <= 0 {
					interval = time.Second
				}
				return watchEvents(ctx, out, src, interval, last, once)
			}
			if interval <= 0 {
				interval = 5 * time.Second
			}
			return watchSummary(ctx, out, src, interval, last, once)
		},
	}
	c.Flags().DurationVar(&interval, "interval", 0, "time between polls (default 5s, 1s with --events)")
	c.Flags().BoolVar(&events, "events", false, "stream one line per audit event instead of the summary")
	c.Flags().IntVar(&last, "last", 5, "audit events to show first (--events) or in each summary")
	c.Flags().BoolVar(&once, "once", false, "print one summary (or the last events) and exit; exit 1 when the daemon is down")
	return c
}

// sleepCtx waits d or until ctx ends; it reports whether ctx is still live.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func watchSummary(ctx context.Context, out io.Writer, src *ui.DaemonSource, interval time.Duration, last int, once bool) error {
	for {
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		s := src.Fetch(fctx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprint(out, ui.WatchSummary(s, time.Now(), interval, last))
		if once {
			if s.Err != nil {
				return notRunning(s.Err)
			}
			return nil
		}
		fmt.Fprintln(out)
		if !sleepCtx(ctx, interval) {
			return nil
		}
	}
}

func watchEvents(ctx context.Context, out io.Writer, src *ui.DaemonSource, interval time.Duration, last int, once bool) error {
	down := false
	for {
		evs, reset, err := src.PollEvents(ctx, last)
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		now := time.Now().Local().Format("15:04:05")
		if reset {
			fmt.Fprintf(out, "%s the daemon's audit log starts again at #%d (a new database)\n", now, src.EventCursor())
		}
		if err == nil && down {
			fmt.Fprintf(out, "%s daemon is up again\n", now)
			down = false
		}
		for _, ev := range evs {
			fmt.Fprintln(out, ui.FormatEvent(ev))
		}
		if err != nil {
			if once {
				return notRunning(err)
			}
			var rpcErr *api.Error
			switch {
			case errors.As(err, &rpcErr):
				// The daemon answered but refused (an older daemon
				// without audit.since): say it on every poll.
				fmt.Fprintf(out, "%s the daemon refused audit.since: %s; retrying every %s\n", now, rpcErr.Message, interval)
			case !down:
				fmt.Fprintf(out, "%s daemon down: %s; retrying every %s\n", now, oneLine(err.Error()), interval)
				down = true
			}
		}
		if once {
			return nil
		}
		if !sleepCtx(ctx, interval) {
			return nil
		}
	}
}
