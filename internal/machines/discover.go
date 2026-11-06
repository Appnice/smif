package machines

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/daemon"
)

// Found is a machine you could add.
type Found struct {
	Name   string // what the list shows
	Target string // what ssh connects to
	Source string // where smif saw it
	Open   bool   // port 22 (or its configured port) answers
	addr   string // host:port to probe
}

const (
	browseFor = 1500 * time.Millisecond
	probeFor  = 800 * time.Millisecond
)

// runner runs a helper tool and returns its output. A tool that is missing or
// that a timeout stops still gives what it printed.
var runner = func(ctx context.Context, name string, args ...string) string {
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}

// Discover lists machines from ~/.ssh/config, Tailscale and the local network,
// and checks which ones answer. skip holds names you already added.
func Discover(ctx context.Context, skip map[string]bool) []Found {
	var mu sync.Mutex
	var all [][]Found
	var wg sync.WaitGroup
	for _, f := range []func(context.Context) []Found{fromSSHConfig, fromTailscale, fromNetwork} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows := f(ctx)
			mu.Lock()
			defer mu.Unlock()
			all = append(all, rows)
		}()
	}
	wg.Wait()
	sort.Slice(all, func(i, j int) bool { return rank(all[i]) < rank(all[j]) })
	self, _ := os.Hostname()
	self = shortName(self)
	seen := map[string]bool{self: true}
	var out []Found
	for _, rows := range all {
		for _, r := range rows {
			key := shortName(r.Name)
			if seen[key] || skip[r.Target] || skip[r.Name] {
				continue
			}
			seen[key] = true
			out = append(out, r)
		}
	}
	var pw sync.WaitGroup
	for i := range out {
		pw.Add(1)
		go func() {
			defer pw.Done()
			out[i].Open = probe(out[i].addr)
		}()
	}
	pw.Wait()
	return out
}

func rank(rows []Found) int {
	if len(rows) == 0 {
		return 9
	}
	switch rows[0].Source {
	case "~/.ssh/config":
		return 0
	case "Tailscale":
		return 1
	}
	return 2
}

// shortName makes mini, mini.local and MINI one machine.
func shortName(s string) string {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if i := strings.Index(s, "."); i > 0 {
		s = s[:i]
	}
	return s
}

func probe(addr string) bool {
	if addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", addr, probeFor)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// fromSSHConfig reads the Host lines and asks ssh what each one means. git
// hosts are not machines you run sessions on.
func fromSSHConfig(ctx context.Context) []Found {
	var out []Found
	for _, name := range hostAliases(filepath.Join(home(), ".ssh", "config"), 0) {
		cfg := resolve(ctx, name)
		if cfg["user"] == "git" {
			continue
		}
		host := cfg["hostname"]
		if host == "" {
			host = name
		}
		port := cfg["port"]
		if port == "" {
			port = "22"
		}
		out = append(out, Found{Name: name, Target: name, Source: "~/.ssh/config", addr: net.JoinHostPort(host, port)})
	}
	return out
}

func hostAliases(file string, depth int) []string {
	f, err := os.Open(file)
	if err != nil || depth > 3 {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(strings.ReplaceAll(sc.Text(), "=", " "))
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "include":
			for _, p := range fields[1:] {
				if strings.HasPrefix(p, "~/") {
					p = filepath.Join(home(), p[2:])
				} else if !filepath.IsAbs(p) {
					p = filepath.Join(home(), ".ssh", p)
				}
				matches, _ := filepath.Glob(p)
				for _, m := range matches {
					out = append(out, hostAliases(m, depth+1)...)
				}
			}
		case "host":
			for _, h := range fields[1:] {
				if !strings.ContainsAny(h, "*?!") {
					out = append(out, h)
				}
			}
		}
	}
	return out
}

func resolve(ctx context.Context, name string) map[string]string {
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out := map[string]string{}
	for _, l := range strings.Split(runner(cctx, daemon.SSHCommand(), "-G", "--", name), "\n") {
		if k, v, ok := strings.Cut(l, " "); ok {
			out[k] = v
		}
	}
	return out
}

func fromTailscale(ctx context.Context) []Found {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, bin := range []string{"tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"} {
		if out := runner(cctx, bin, "status", "--json"); out != "" {
			return parseTailscale(out)
		}
	}
	return nil
}

func parseTailscale(out string) []Found {
	var st struct {
		Peer map[string]struct {
			HostName string
			DNSName  string
		}
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return nil
	}
	var rows []Found
	for _, p := range st.Peer {
		host := strings.TrimSuffix(p.DNSName, ".")
		if host == "" {
			host = p.HostName
		}
		rows = append(rows, Found{Name: p.HostName, Target: host, Source: "Tailscale", addr: net.JoinHostPort(host, "22")})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// fromNetwork browses for ssh on the local network. A Linux machine shows up
// only with avahi-daemon, so the screen also takes a typed address.
func fromNetwork(ctx context.Context) []Found {
	cctx, cancel := context.WithTimeout(ctx, browseFor)
	defer cancel()
	if runtime.GOOS == "darwin" {
		return parseBrowse(runner(cctx, "dns-sd", "-B", "_ssh._tcp", "local."))
	}
	return parseAvahi(runner(cctx, "avahi-browse", "-ptr", "_ssh._tcp", "--terminate"))
}

// parseBrowse reads dns-sd -B. The instance name is the computer's name, and a
// Mac's .local name swaps its spaces for dashes.
func parseBrowse(out string) []Found {
	var rows []Found
	for _, l := range strings.Split(out, "\n") {
		i := strings.Index(l, "_ssh._tcp.")
		if i < 0 || !strings.Contains(l[:i], " Add ") {
			continue
		}
		name := strings.TrimSpace(l[i+len("_ssh._tcp."):])
		if name == "" {
			continue
		}
		host := strings.ReplaceAll(name, " ", "-") + ".local"
		rows = append(rows, Found{Name: host, Target: host, Source: "your network", addr: net.JoinHostPort(host, "22")})
	}
	return rows
}

// parseAvahi reads avahi-browse -p: =;if;IPv4;name;type;domain;host;addr;port;txt
func parseAvahi(out string) []Found {
	var rows []Found
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, ";")
		if len(f) < 9 || f[0] != "=" || f[2] != "IPv4" {
			continue
		}
		port := f[8]
		if _, err := strconv.Atoi(port); err != nil {
			port = "22"
		}
		rows = append(rows, Found{Name: f[6], Target: f[6], Source: "your network", addr: net.JoinHostPort(f[7], port)})
	}
	return rows
}
