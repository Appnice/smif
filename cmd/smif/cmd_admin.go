package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/daemon"
	"github.com/appnice/smif/internal/install"
	"github.com/appnice/smif/internal/mcpserver"
	"github.com/appnice/smif/internal/notify"
	"github.com/appnice/smif/internal/paths"
	"github.com/appnice/smif/internal/presence"
	"github.com/appnice/smif/internal/runner/claude"
	"github.com/appnice/smif/internal/statusline"
)

func cmdDaemon() *cobra.Command {
	var foreground bool
	c := &cobra.Command{
		Use: "daemon", Short: "Run the daemon in the foreground (used by the service unit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := daemon.Options{}
			if foreground || os.Getenv("SMIF_LOG_STDERR") != "" {
				opts.LogWriter = os.Stderr
			}
			d, err := daemon.New(opts)
			if err != nil {
				return err
			}
			if err := d.Start(); err != nil {
				return err
			}
			sig := make(chan os.Signal, 2)
			signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			for s := range sig {
				if s == syscall.SIGHUP {
					if err := d.Reload(); err != nil {
						fmt.Fprintln(os.Stderr, "reload failed, old config kept:", err)
					}
					continue
				}
				break
			}
			d.Stop()
			return nil
		},
	}
	c.Flags().BoolVar(&foreground, "log-stderr", false, "also log to stderr")
	return c
}

func cmdConfig() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Configuration"}
	var file string
	v := &cobra.Command{
		Use: "validate", Short: "Validate the config, including the catch-all rule",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(file)
			if err != nil {
				return err
			}
			for _, w := range cfg.Warnings {
				fmt.Println("warning:", w)
			}
			fmt.Printf("%s: ok (%d agents: %s)\n", cfg.Path, len(cfg.Agents), strings.Join(cfg.AgentIDs(), ", "))
			return nil
		},
	}
	v.Flags().StringVar(&file, "config", "", "config file (default "+paths.ConfigFile()+")")
	c.AddCommand(v, &cobra.Command{Use: "path", Short: "Print the config path", Run: func(*cobra.Command, []string) { fmt.Println(paths.ConfigFile()) }})
	return c
}

// cmdNotify sends from this process, so the test also works with no daemon.
func cmdNotify() *cobra.Command {
	c := &cobra.Command{Use: "notify", Short: "Phone ping (daemon.notify_url)"}
	c.AddCommand(&cobra.Command{
		Use: "test", Short: "Send one ping to daemon.notify_url", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Daemon.NotifyURL == "" {
				return errors.New("daemon.notify_url is not set")
			}
			if err := notify.NewPinger(nil).Post(cfg.Daemon.NotifyURL, notify.PingTestText); err != nil {
				return fmt.Errorf("ping failed: %w", err)
			}
			fmt.Println("ping sent")
			return nil
		},
	})
	return c
}

func cmdReload() *cobra.Command {
	return &cobra.Command{Use: "reload", Short: "Hot-reload the config (invalid config keeps the old one)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r map[string]any
			if err := call("admin.reload", nil, &r, 30*time.Second); err != nil {
				return notRunning(err)
			}
			printJSON(r)
			return nil
		}}
}

func cmdGC() *cobra.Command {
	return &cobra.Command{Use: "gc", Short: "Apply retention now",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r api.GCResult
			if err := call("admin.gc", nil, &r, 120*time.Second); err != nil {
				return notRunning(err)
			}
			printJSON(r)
			return nil
		}}
}

func cmdMCP() *cobra.Command {
	return &cobra.Command{Use: "mcp", Short: "Run the MCP server on stdio", Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error { return mcpserver.Run(context.Background()) }}
}

// cmdHook handles Claude Code hooks: presence only, prints nothing, always
// exits 0 within ~200 ms (RFC 7.4).
func cmdHook() *cobra.Command {
	return &cobra.Command{Use: "hook <event>", Short: "Claude Code hook entry point (prints nothing)", Hidden: true,
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			defer func() { recover(); os.Exit(0) }()
			if presence.InWorker() || len(args) == 0 {
				return
			}
			in, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			p := presence.Parse(in)
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := humanClient(120 * time.Millisecond)
				if err != nil {
					hookLog("hook %s: %v", args[0], err)
					return
				}
				defer c.Close()
				pp := api.PresenceParams{Event: presence.EventFor(args[0]), Cwd: p.Cwd, SessionID: p.SessionID, Agent: os.Getenv("SMIF_AGENT")}
				if pp.Event != "end" {
					pp.PID, pp.TTY = presence.Locate()
					pp.Term = presence.Term()
					pp.Title, pp.LastPrompt = presence.Transcript(p.TranscriptPath)
					if p.Prompt != "" {
						pp.LastPrompt = p.Prompt
					}
				}
				if err := c.Call("presence.update", pp, nil, 150*time.Millisecond); err != nil {
					hookLog("hook %s: %v", args[0], err)
				}
			}()
			select {
			case <-done:
			case <-time.After(190 * time.Millisecond):
				hookLog("hook %s: timed out", args[0])
			}
		}}
}

func hookLog(format string, a ...any) {
	p := filepath.Join(paths.DataDir(), "logs", "hooks.log")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func cmdStatusline() *cobra.Command {
	var wrap string
	c := &cobra.Command{Use: "statusline", Short: "Print the status-line segment (counts only)", Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			in, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			wrapped := make(chan string, 1)
			go func() { wrapped <- statusline.RunWrapped(wrap, in, 2*time.Second) }()
			p := presence.Parse(in)
			var counts api.Counts
			cl, err := humanClient(150 * time.Millisecond)
			if err == nil {
				err = cl.Call("inbox.counts", api.CountsParams{Cwd: p.Cwd, Agent: os.Getenv("SMIF_AGENT"), SessionID: p.SessionID}, &counts, 250*time.Millisecond)
				if !presence.InWorker() {
					_ = cl.Call("presence.update", api.PresenceParams{Event: "heartbeat", Cwd: p.Cwd, SessionID: p.SessionID, Agent: os.Getenv("SMIF_AGENT")}, nil, 150*time.Millisecond)
				}
				cl.Close()
			}
			fmt.Println(statusline.Combine(<-wrapped, statusline.Segment(&counts, err)))
		}}
	c.Flags().StringVar(&wrap, "wrap", "", "existing status-line command to run first")
	return c
}

func cmdInstall() *cobra.Command {
	var noService, noMCP bool
	c := &cobra.Command{
		Use: "install", Short: "Install hooks, status line, slash command, MCP server and the service (backs up every file)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w (run `smif init` first)", err)
			}
			reg := auth.NewRegistry()
			if err := reg.Load(cfg.AgentIDs()); err != nil {
				return err
			}
			_, err = install.Install(install.Options{Binary: absBinary(), ClaudeCommand: cfg.Defaults.ClaudeCommand,
				NoService: noService, NoMCP: noMCP, Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }})
			if err != nil {
				return err
			}
			fmt.Println("installed. Undo everything with `smif uninstall`.")
			return nil
		},
	}
	c.Flags().BoolVar(&noService, "no-service", false, "do not install the launchd/systemd unit")
	c.Flags().BoolVar(&noMCP, "no-mcp", false, "do not register the MCP server")
	return c
}

func cmdUninstall() *cobra.Command {
	return &cobra.Command{
		Use: "uninstall", Short: "Reverse `smif install`, restoring every edited file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cc := "claude"
			if cfg, err := loadConfig(); err == nil {
				cc = cfg.Defaults.ClaudeCommand
			}
			if err := install.Uninstall(install.Options{ClaudeCommand: cc, Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}); err != nil {
				return err
			}
			fmt.Println("uninstalled. State in", paths.DataDir(), "and config in", paths.ConfigDir(), "are kept.")
			return nil
		},
	}
}

func cmdInit() *cobra.Command {
	var agents, scan []string
	var yes, scaffold bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Set up: discover repos, write config, generate tokens",
		Long: "Interactive by default: lists the git repos under ~/dev, ~/code, ~/src, ~/projects and ~/repos (or the\n" +
			"--scan dirs) and asks for an agent id for each. Non-interactive:\n" +
			"  smif init --agent api=~/dev/api --agent web=~/dev/web --yes\n" +
			"Each agent gets the safe default rules: a status.get ask is answered from git by a command handler\n" +
			"(" + "<config dir>/handlers/status.sh, no LLM), crash.* events go to a read-only triage worker, notice.* events\n" +
			"go to the digest, and everything else escalates to you. Nothing is written into the repos unless --scaffold.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cf := paths.ConfigFile()
			if _, err := os.Stat(cf); err == nil && !yes {
				return fmt.Errorf("%s already exists; edit it or pass --yes to overwrite", cf)
			}
			pairs := map[string]string{}
			for _, a := range agents {
				id, repo, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("--agent wants id=path, got %q", a)
				}
				pairs[id] = repo
			}
			if len(pairs) == 0 {
				if yes {
					return fmt.Errorf("--yes needs at least one --agent id=path")
				}
				var err error
				pairs, err = interactiveAgents(scan)
				if err != nil {
					return err
				}
			}
			claudePath, _ := exec.LookPath("claude")
			if claudePath == "" {
				claudePath = "claude"
			}
			handler := filepath.Join(paths.ConfigDir(), "handlers", "status.sh")
			if scaffold {
				handler = "./scripts/smif/status.sh"
			}
			body := renderConfig(pairs, claudePath, handler)
			if _, err := config.Parse([]byte(body)); err != nil {
				return fmt.Errorf("generated config is invalid (bug): %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(cf), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(cf, []byte(body), 0o600); err != nil {
				return err
			}
			fmt.Println("wrote", cf)
			reg := auth.NewRegistry()
			ids := make([]string, 0, len(pairs))
			for id := range pairs {
				ids = append(ids, id)
			}
			if err := reg.Load(ids); err != nil {
				return err
			}
			fmt.Println("tokens in", auth.TokenPath(""))
			// The status handler: one shared copy in the config dir, or with
			// --scaffold a copy in each repo that the repo can then own.
			targets := []string{filepath.Join(paths.ConfigDir(), "handlers", "status.sh")}
			if scaffold {
				targets = nil
				for _, repo := range pairs {
					targets = append(targets, filepath.Join(paths.Expand(repo), "scripts", "smif", "status.sh"))
				}
			}
			for _, p := range targets {
				if _, err := os.Stat(p); err == nil {
					continue // never overwrite a handler someone edited
				}
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(StatusHandler), 0o755); err == nil {
					fmt.Println("handler", p)
				}
			}
			fmt.Println("next: smif install")
			return nil
		},
	}
	c.Flags().StringArrayVar(&agents, "agent", nil, "agent as id=repo-path (repeatable)")
	c.Flags().BoolVar(&yes, "yes", false, "non-interactive; overwrite an existing config")
	c.Flags().StringArrayVar(&scan, "scan", nil, "directory whose git repos are offered as agents (repeatable; replaces the defaults)")
	c.Flags().BoolVar(&scaffold, "scaffold", false, "write the status handler into each repo's scripts/smif/ instead of the config dir")
	return c
}

// DefaultScanDirs are searched for git repos when init runs without --scan.
var DefaultScanDirs = []string{"~/dev", "~/code", "~/src", "~/projects", "~/repos"}

func interactiveAgents(scan []string) (map[string]string, error) {
	r := bufio.NewReader(os.Stdin)
	var found []string
	if len(scan) == 0 {
		scan = DefaultScanDirs
	}
	for _, root := range scan {
		dir := paths.Expand(root)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
				found = append(found, p)
			}
		}
	}
	sort.Strings(found)
	pairs := map[string]string{}
	if len(found) == 0 {
		return nil, fmt.Errorf("no git repositories under %s; pass --scan <dir> or --agent id=path", strings.Join(scan, ", "))
	}
	fmt.Println("Found git repositories. For each, type an agent id to register it, or press Enter to skip.")
	for _, p := range found {
		fmt.Printf("%s [agent id]: ", p)
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		if id := strings.TrimSpace(line); id != "" {
			pairs[id] = p
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no agents chosen")
	}
	return pairs, nil
}

func renderConfig(pairs map[string]string, claudePath, statusHandler string) string {
	ids := make([]string, 0, len(pairs))
	for id := range pairs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, `# Smif config (RFC 7.6). Validate with: smif config validate
# Rules are tried top to bottom; the first match handles the message, the last rule must match all.
[daemon]
max_workers = 3
command_pool = 4
default_ttl = "24h"
presence_ttl = "30m"
notify = "os"

[defaults]
claude_command = %q
max_hops = 4
max_attempts = 3
`, claudePath)
	for _, id := range ids {
		fmt.Fprintf(&b, `
[agents.%[1]s]
repo = %[2]q
base_ref = "main"

# "What is the state of %[1]s?": answered from git, no LLM.
[[agents.%[1]s.rules]]
match = { kind = "ask", intent = "status.get" }
handler = "command"
command = [%[3]q]

# Crash signals: a read-only worker looks at them in the repo. Repeats with the same dedupe key are
# merged; after the worker is done they stay merged for the cooldown (use a cooldown longer than the
# interval of whatever sends them), and the 10th repeat opens a new run.
[[agents.%[1]s.rules]]
match = { kind = "event", intent = "crash.*" }
handler = "worker"
profile = "triage"
cooldown = "24h"
reopen_after = 10

# Information only (a build finished, a report is ready): straight to the digest.
[[agents.%[1]s.rules]]
match = { kind = "event", intent = "notice.*" }
handler = "digest"

# A reply to a worker's own follow-up ask continues that worker (RFC 5.3).
[[agents.%[1]s.rules]]
match = { kind = "reply" }
handler = "worker"
profile = "triage"

[[agents.%[1]s.rules]]
match = {}
handler = "escalate"
`, id, pairs[id], statusHandler)
	}
	return b.String()
}

// StatusHandler is the status.get command handler that init installs: it
// answers from git alone. cwd is the agent's repo; stdin is the envelope.
const StatusHandler = `#!/bin/sh
# Smif command handler for status.get asks: answers from git, no LLM.
# cwd: the agent's repo. stdin: the message envelope (JSON). stdout: one JSON result.
# Extend it freely (a release file, a store API, a build status); keep the output shape.
cat >/dev/null
esc() { sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | tr -d '\n\r\t'; }
repo=$(basename "$(pwd)")
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null | esc)
tag=$(git describe --tags --abbrev=0 2>/dev/null | esc)
head=$(git log -1 --format='%h %cs %s' 2>/dev/null | esc)
dirty=$(git status --porcelain --untracked-files=no 2>/dev/null | wc -l | tr -d ' ')
reply="$repo: branch ${branch:-?}, latest tag ${tag:-none}, HEAD $head, $dirty uncommitted files"
printf '{"status":"done","summary":"%s","reply":"%s","reply_data":{"branch":"%s","latest_tag":"%s","head":"%s","uncommitted":%s}}\n' \
  "$repo ${tag:-untagged}" "$reply" "$branch" "$tag" "$head" "${dirty:-0}"
`

func cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check Claude Code flags, socket, permissions, hooks, git and the notifier",
		RunE: func(cmd *cobra.Command, args []string) error {
			bad := 0
			check := func(ok bool, what, detail string) {
				mark := "ok  "
				if !ok {
					mark = "FAIL"
					bad++
				}
				fmt.Printf("[%s] %s", mark, what)
				if detail != "" {
					fmt.Printf(": %s", detail)
				}
				fmt.Println()
			}
			warn := func(what string) { fmt.Printf("[warn] %s\n", what) }
			cfg, err := loadConfig()
			if _, serr := os.Stat(paths.ConfigFile()); err == nil && os.IsNotExist(serr) {
				warn("no config at " + paths.ConfigFile() + ": the session view works; `smif init` adds agents for messages and workers")
			} else {
				check(err == nil, "config "+paths.ConfigFile(), errString(err))
			}
			cc := "claude"
			if cfg != nil {
				cc = cfg.Defaults.ClaudeCommand
				for _, w := range cfg.Warnings {
					warn(w)
				}
				if cfg.Daemon.NotifyURL != "" {
					fmt.Println("[info] phone ping is on: session names leave this machine (`smif notify test`)")
				}
				for _, id := range cfg.AgentIDs() {
					a := cfg.Agent(id)
					if a.AllowBypass {
						warn("agent " + id + " has allow_bypass = true (bypassPermissions workers)")
					}
					_, gerr := exec.Command("git", "-C", a.RepoPath(), "rev-parse", "--git-dir").Output()
					check(gerr == nil, "agent "+id+" repo "+a.RepoPath(), errString(gerr))
				}
			}
			if p, err := exec.LookPath(cc); err == nil {
				if !filepath.IsAbs(cc) {
					warn("claude_command is " + cc + "; the service PATH must contain " + filepath.Dir(p) + " (or set the absolute path)")
				}
			}
			caps := claude.Probe(context.Background(), cc)
			check(caps.OK(), "claude CLI "+caps.Version, strings.Join(caps.Problems, "; "))
			for _, l := range caps.Lines() {
				fmt.Println("       ", l)
			}
			// The probe above checks every flag Smif needs, so a newer
			// version only warns when it is a new minor line; an older one
			// than the verified version always warns.
			if v := caps.Version; v != "" {
				switch c := claude.CompareVersions(v, claude.VerifiedVersion); {
				case c < 0:
					warn("claude " + v + " is older than the verified " + claude.VerifiedVersion + "; update Claude Code")
				case c > 0 && !claude.SameMinor(v, claude.VerifiedVersion):
					warn("claude " + v + " is a newer line than the verified " + claude.VerifiedVersion + "; the flags passed the probe, re-check docs/compat.md")
				}
			}
			_, gerr := exec.LookPath("git")
			check(gerr == nil, "git", errString(gerr))
			permCheck := func(p string, want os.FileMode) {
				fi, err := os.Stat(p)
				if os.IsNotExist(err) {
					warn(p + " does not exist yet: the daemon makes it on its first start (`smif` starts it)")
					return
				}
				if err != nil {
					check(false, p, err.Error())
					return
				}
				check(fi.Mode().Perm()&^want == 0, fmt.Sprintf("%s mode %o", p, fi.Mode().Perm()), "")
			}
			permCheck(paths.ConfigDir(), 0o700)
			permCheck(paths.DataDir(), 0o700)
			permCheck(paths.TokenDir(), 0o700)
			c, derr := humanClient(time.Second)
			if derr == nil {
				var h api.HealthView
				derr = c.Call("admin.health", nil, &h, 5*time.Second)
				c.Close()
				if derr == nil {
					check(h.OK, fmt.Sprintf("daemon %s pid %d, schema %d, db %s", h.Version, h.PID, h.SchemaVersion, h.DB), "")
					check(h.ClaudeOK, "daemon sees claude", strings.Join(h.ClaudeProblems, "; "))
					for _, p := range h.Providers {
						name := p.Name
						if name == "claude" {
							name = "Claude Code"
						}
						switch p.State {
						case "changed":
							// Only Claude Code's files can fail the doctor: mail runs on them.
							text := name + " session files: " + p.Detail + " (a new " + name + " version? the session view needs an update)"
							if p.Name == "claude" {
								check(false, name+" session files", p.Detail+" (a new "+name+" version? the session view needs an update)")
							} else {
								warn(text)
							}
						case "none":
							if p.Name == "claude" {
								warn(p.Detail)
							}
						case "ok":
							check(true, name+" session files", p.Detail)
						case "unchecked":
							fmt.Printf("[info] %s format unchecked, read from its source and no real session checked it: %s\n", name, nonEmptyStr(p.Detail, "nothing to read"))
						}
					}
					for _, p := range h.Remotes {
						name := nonEmptyStr(p.Title, p.Name)
						if p.State == "ok" {
							check(true, "remote "+name, p.Detail)
						} else {
							warn("remote " + name + ": " + p.Detail)
						}
					}
					// A research preview: its absence or a change is no failure.
					for _, p := range h.Background {
						if p.State == "ok" {
							check(true, "background sessions", p.Detail)
						} else {
							fmt.Printf("[info] no background sessions: %s\n", p.Detail)
						}
					}
				}
			}
			check(derr == nil, "socket "+paths.Socket(), errString(derr))
			if derr == nil {
				permCheck(paths.Socket(), 0o600)
			}
			m, _ := install.LoadManifest()
			check(m != nil, "installed (hooks, status line, slash command)", map[bool]string{true: "", false: "run `smif install`"}[m != nil])
			if m != nil {
				b, _ := os.ReadFile(install.SettingsPath())
				check(strings.Contains(string(b), " hook SessionStart"), "hooks in "+install.SettingsPath(), "")
				check(strings.Contains(string(b), " statusline"), "status line wrapper", "")
				ok, detail := install.ServiceLoaded()
				check(ok || m.ServicePath == "", "service", detail)
			}
			switch runtime.GOOS {
			case "darwin":
				_, e1 := exec.LookPath("terminal-notifier")
				_, e2 := exec.LookPath("osascript")
				_, e3 := exec.LookPath("osacompile")
				check(e1 == nil || e2 == nil, "notifier (Smif.app via osacompile, terminal-notifier or osascript)", errString(e3))
			default:
				_, e := exec.LookPath("notify-send")
				check(e == nil, "notifier (notify-send)", errString(e))
			}
			if bad > 0 {
				return fmt.Errorf("%d check(s) failed", bad)
			}
			return nil
		},
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
