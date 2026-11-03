package main

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/appnice/mailroom/internal/api"
)

// openLoop sends at a fixed rate for d, whatever the replies do (no
// coordinated omission): call i is due at start + i/rate, and its latency
// counts from that due time, so a stall shows as the wait of every call
// scheduled behind it. Calls run on a pool of connections (one call at a
// time per connection, like the real clients).
func openLoop(dial func() (*api.Client, error), pool int, rate float64, d time.Duration,
	call func(c *api.Client, i int) error) (lat []time.Duration, errs int, err error) {
	conns := make(chan *api.Client, pool)
	for i := 0; i < pool; i++ {
		c, err := dial()
		if err != nil {
			return nil, 0, err
		}
		defer c.Close()
		conns <- c
	}
	n := int(rate * d.Seconds())
	lat = make([]time.Duration, n)
	failed := make([]bool, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		due := start.Add(time.Duration(float64(i) / rate * float64(time.Second)))
		if w := time.Until(due); w > 0 {
			time.Sleep(w)
		}
		wg.Add(1)
		go func(i int, due time.Time) {
			defer wg.Done()
			c := <-conns
			defer func() { conns <- c }()
			if call(c, i) != nil {
				failed[i] = true
			}
			lat[i] = time.Since(due)
		}(i, due)
	}
	wg.Wait()
	for _, f := range failed {
		if f {
			errs++
		}
	}
	return lat, errs, nil
}

// stats summarises latencies.
type stats struct {
	N                        int
	P50, P90, P99, P999, Max time.Duration
	Mean                     time.Duration
}

func summarize(xs []time.Duration) stats {
	if len(xs) == 0 {
		return stats{}
	}
	s := append([]time.Duration(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	q := func(p float64) time.Duration {
		i := int(math.Ceil(p*float64(len(s)))) - 1
		return s[max(0, min(i, len(s)-1))]
	}
	var sum time.Duration
	for _, x := range s {
		sum += x
	}
	return stats{N: len(s), P50: q(0.5), P90: q(0.9), P99: q(0.99), P999: q(0.999), Max: s[len(s)-1], Mean: sum / time.Duration(len(s))}
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.2f ms", float64(d)/float64(time.Millisecond))
}

func (s stats) row(name string) string {
	return fmt.Sprintf("| %s | %d | %s | %s | %s | %s | %s |", name, s.N, ms(s.P50), ms(s.P90), ms(s.P99), ms(s.P999), ms(s.Max))
}

const statsHead = "| | n | p50 | p90 | p99 | p99.9 | max |\n|---|---|---|---|---|---|---|"
