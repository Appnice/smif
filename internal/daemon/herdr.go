package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

// ---- herdr (ADR-0018) ----
//
// herdr (herdr.dev) is a terminal multiplexer for coding agents. A server
// owns the panes; its CLI talks to the server's socket and answers JSON.
// Smif finds the pane whose foreground process is the session's claude,
// on any running herdr session, and drives it with `pane run` (text and
// Enter as one submission, in bracketed paste when the pane asks for it).

const termHerdr = "herdr"

// herdrPath finds the herdr command: on PATH, else the usual install places
// (the daemon runs with launchd's short PATH; install.sh uses ~/.local/bin).
// $SMIF_HERDR overrides both: tests set a fake, or "" for no herdr.
func herdrPath() string {
	if p, ok := os.LookupEnv("SMIF_HERDR"); ok {
		return p
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/opt/homebrew/bin/herdr", "/usr/local/bin/herdr", filepath.Join(home, ".local/bin/herdr"), "/usr/bin/herdr"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// herdrPane is a pane on one herdr session.
type herdrPane struct{ session, pane, tab, workspace string }

// args prefixes a herdr command with the session it runs on.
func (h herdrPane) args(a ...string) []string {
	return append([]string{"--session", h.session}, a...)
}

// herdrRun runs one herdr CLI command and returns its JSON result. Errors
// come as JSON on stderr: {"error":{"code","message"}}.
func herdrRun(ctx context.Context, args ...string) (json.RawMessage, error) {
	bin := herdrPath()
	if bin == "" {
		return nil, errors.New("the daemon cannot find the herdr command")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal([]byte(stderr.String()), &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("herdr %s: %s", herdrVerb(args), e.Error.Message)
		}
		return nil, fmt.Errorf("herdr %s: %v: %s", herdrVerb(args), err, strings.TrimSpace(stderr.String()))
	}
	var r struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil || r.Result == nil {
		return out, nil
	}
	return r.Result, nil
}

// herdrVerb is the command's name for errors: "pane run", "agent focus".
func herdrVerb(args []string) string {
	if len(args) >= 2 && args[0] == "--session" {
		args = args[2:]
	}
	return strings.Join(args[:min(2, len(args))], " ")
}

// herdrSessions are the names of the running herdr sessions, default first.
func herdrSessions(ctx context.Context) ([]string, error) {
	out, err := herdrRun(ctx, "session", "list", "--json")
	if err != nil {
		return nil, err
	}
	return parseHerdrSessions(out)
}

func parseHerdrSessions(b []byte) ([]string, error) {
	var l struct {
		Sessions []struct {
			Name    string `json:"name"`
			Default bool   `json:"default"`
			Running bool   `json:"running"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("herdr session list: %v", err)
	}
	var out []string
	for _, s := range l.Sessions {
		switch {
		case !s.Running:
		case s.Default:
			out = append([]string{s.Name}, out...)
		default:
			out = append(out, s.Name)
		}
	}
	return out, nil
}

// parseHerdrPanes reads `pane list`: the pane, tab and workspace ids.
func parseHerdrPanes(b []byte) ([]herdrPane, error) {
	var l struct {
		Panes []struct {
			Pane      string `json:"pane_id"`
			Tab       string `json:"tab_id"`
			Workspace string `json:"workspace_id"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("herdr pane list: %v", err)
	}
	out := make([]herdrPane, 0, len(l.Panes))
	for _, p := range l.Panes {
		out = append(out, herdrPane{pane: p.Pane, tab: p.Tab, workspace: p.Workspace})
	}
	return out, nil
}

// herdrForeground reads `pane process-info`: the pids that own the pane's
// foreground (its foreground process group and each process in it).
func herdrForeground(b []byte) []int {
	var r struct {
		Info struct {
			Group int `json:"foreground_process_group_id"`
			Procs []struct {
				PID int `json:"pid"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []int
	if r.Info.Group > 0 {
		out = append(out, r.Info.Group)
	}
	for _, p := range r.Info.Procs {
		out = append(out, p.PID)
	}
	return out
}

// findHerdrPane is the pane whose foreground is the claude process pid, on
// any running herdr session. Looked up on each use: a moved pane gets a new id.
func findHerdrPane(ctx context.Context, pid int) (herdrPane, error) {
	sessions, err := herdrSessions(ctx)
	if err != nil {
		return herdrPane{}, err
	}
	for _, s := range sessions {
		out, err := herdrRun(ctx, "--session", s, "pane", "list")
		if err != nil {
			continue
		}
		panes, err := parseHerdrPanes(out)
		if err != nil {
			continue
		}
		for _, p := range panes {
			p.session = s
			info, err := herdrRun(ctx, p.args("pane", "process-info", "--pane", p.pane)...)
			if err != nil {
				continue
			}
			if slices.Contains(herdrForeground(info), pid) {
				return p, nil
			}
		}
	}
	return herdrPane{}, fmt.Errorf("no herdr pane has process %d in its foreground", pid)
}

// herdrAgent is one agent of `agent list` or `agent get`.
type herdrAgent struct {
	Agent         string `json:"agent"` // the agent herdr recognised: codex
	Name          string `json:"name"`  // set by `agent rename`
	Status        string `json:"agent_status"`
	Cwd           string `json:"cwd"`
	ForegroundCwd string `json:"foreground_cwd"`
	Pane          string `json:"pane_id"`
	Terminal      string `json:"terminal_id"` // stays when the pane moves
}

// parseHerdrAgents reads `agent list`.
func parseHerdrAgents(b []byte) ([]herdrAgent, error) {
	var l struct {
		Agents []herdrAgent `json:"agents"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("herdr agent list: %v", err)
	}
	return l.Agents, nil
}

// herdrTurn maps herdr's agent_status to smif's turn. Any word it does not
// know is unknown, and unknown never types. herdr's idle and done count only
// for an agent herdr can tell idle for (ADR-0019): for the others they mean
// that no screen rule matched, which an approval dialog also does. An agent
// whose only idle rule is herdr's integration needs that integration current.
func herdrTurn(ctx context.Context, agent, status string) string {
	switch status {
	case "working":
		return "busy"
	case "blocked":
		return "waiting"
	case "idle", "done":
		if harness.HerdrTellsIdle(agent) && (!harness.HerdrNeedsIntegration(agent) || herdrIntegrations.current(ctx, agent)) {
			return "idle"
		}
	}
	return "unknown"
}

// herdrChecked reports whether a real session checked this agent at the
// herdr level. It covers agents with no provider, and until it says yes smif
// does not type into them: a wrong idle could answer an approval prompt. Add
// an agent only with its docs/compat.md row. A test replaces this to reach
// the gates behind it.
var herdrChecked = func(agent string) bool { return false }

// herdrUnknownWhy says why a herdr-level row with state unknown takes no
// typing. It reads the integrations' last answer, so it never runs herdr.
func herdrUnknownWhy(agent string) string {
	name := nonEmpty(agent, "this agent")
	switch {
	case !harness.HerdrTellsIdle(agent):
		return "herdr cannot tell idle from a dialog for " + name + ", so Smif does not type"
	case harness.HerdrNeedsIntegration(agent) && !herdrIntegrations.last(agent):
		return "no current herdr integration for " + name + ", so herdr cannot tell idle and Smif does not type (herdr integration install " + name + ")"
	}
	return "herdr cannot tell this agent's state, so Smif does not type"
}

// herdrIntegrations caches `herdr integration status`. The status belongs to
// the user, not to a herdr session, so one answer serves every session.
var herdrIntegrations integrationCache

const (
	integrationEvery   = 5 * time.Second // an answer is fresh this long
	integrationTimeout = 2 * time.Second // a hung herdr ends here
)

type integrationCache struct {
	mu     sync.Mutex
	at     time.Time
	agents map[string]bool
}

// current reports whether the agent's integration is current. A failed read
// gives no agents, so an answer smif cannot read counts as not installed. Two
// callers can run the command at once: it only reads, and the last one wins.
func (c *integrationCache) current(ctx context.Context, agent string) bool {
	c.mu.Lock()
	agents, fresh := c.agents, time.Since(c.at) < integrationEvery
	c.mu.Unlock()
	if !fresh {
		agents = readHerdrIntegrations(ctx)
		c.mu.Lock()
		c.agents, c.at = agents, time.Now()
		c.mu.Unlock()
	}
	return agents[agent]
}

// forget drops the answer, so the next read runs the command.
func (c *integrationCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents, c.at = nil, time.Time{}
}

// last is the last answer, however old. It runs no command.
func (c *integrationCache) last(agent string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agents[agent]
}

func readHerdrIntegrations(ctx context.Context) map[string]bool {
	bin := herdrPath()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, integrationTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "integration", "status").Output()
	if err != nil {
		return nil
	}
	return harness.HerdrIntegrationsCurrent(string(out))
}

// herdrAgentReady finds the pane of a herdr-level session and asks herdr
// for the state now: the list may be a few seconds old, and a state that
// is not idle must stop the prompt (ADR-0019). The agent's name comes from
// the same answer, so a pane that now runs another agent cannot pass.
func herdrAgentReady(ctx context.Context, p store.Presence) (herdrPane, error) {
	h, err := findHerdrPane(ctx, p.PID)
	if err != nil {
		return h, err
	}
	out, err := herdrRun(ctx, h.args("agent", "get", h.pane)...)
	if err != nil {
		return h, err
	}
	var r struct {
		Agent herdrAgent `json:"agent"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return h, fmt.Errorf("herdr agent get: %v", err)
	}
	switch t := herdrTurn(ctx, r.Agent.Agent, r.Agent.Status); {
	case t == "idle":
		return h, nil
	case r.Agent.Status == "idle" || r.Agent.Status == "done":
		return h, errors.New(herdrUnknownWhy(r.Agent.Agent))
	}
	return h, fmt.Errorf("herdr says %s is %s", p.Harness, nonEmpty(r.Agent.Status, "unknown"))
}

// herdrAgentPrompt sends one prompt through `agent prompt`, which sends
// text and Enter as one submission and refuses a blocked agent. It takes
// the text as it is: a leading - stays text, and -- would be typed.
func herdrAgentPrompt(ctx context.Context, p store.Presence, text string) error {
	h, err := herdrAgentReady(ctx, p)
	if err != nil {
		return err
	}
	_, err = herdrRun(ctx, h.args("agent", "prompt", h.pane, text)...)
	return err
}

func herdrType(ctx context.Context, p store.Presence, text string) error {
	h, err := findHerdrPane(ctx, p.PID)
	if err != nil {
		return err
	}
	_, err = herdrRun(ctx, h.args("pane", "run", h.pane, text)...)
	return err
}

func herdrEnter(ctx context.Context, p store.Presence) error {
	h, err := findHerdrPane(ctx, p.PID)
	if err != nil {
		return err
	}
	_, err = herdrRun(ctx, h.args("pane", "send-keys", h.pane, "enter")...)
	return err
}

// herdrFocus selects the pane in herdr (as an agent, else its tab), then
// brings the window of a herdr client attached to that session to the front
// when the client runs in Terminal.app or iTerm2.
func herdrFocus(ctx context.Context, p store.Presence) error {
	h, err := findHerdrPane(ctx, p.PID)
	if err != nil {
		return err
	}
	if _, err := herdrRun(ctx, h.args("agent", "focus", h.pane)...); err != nil {
		if _, err := herdrRun(ctx, h.args("workspace", "focus", h.workspace)...); err != nil {
			return err
		}
		if _, err := herdrRun(ctx, h.args("tab", "focus", h.tab)...); err != nil {
			return err
		}
	}
	for _, c := range herdrClients(h.session) {
		pi := readProcInfo(c)
		if pi.tty == "" {
			continue
		}
		switch pi.term {
		case termApple:
			_ = runAppleScript(ctx, "Terminal", appleFocusScript, pi.tty)
		case termITerm:
			_ = runAppleScript(ctx, "iTerm2", iTermFocusScript, pi.tty)
		}
	}
	return nil
}

// herdrClients are the pids of herdr clients attached to session: herdr
// processes on a tty that are not a server.
func herdrClients(session string) []int {
	out, err := exec.Command("ps", "-ax", "-o", "pid=,tty=,args=").Output()
	if err != nil {
		return nil
	}
	return parseHerdrClients(string(out), session)
}

func parseHerdrClients(ps, session string) []int {
	var out []int
	for _, l := range strings.Split(ps, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || f[1] == "??" || f[1] == "?" || filepath.Base(f[2]) != "herdr" {
			continue
		}
		args, name := f[3:], "default"
		if slices.Contains(args, "server") {
			continue
		}
		if i := slices.Index(args, "--session"); i >= 0 && i+1 < len(args) {
			name = args[i+1]
		}
		if name != session {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// herdrOpen starts line in a new workspace in dir on the default herdr
// session (or the first running one) and focuses it.
func herdrOpen(ctx context.Context, dir, label, line string) error {
	sessions, err := herdrSessions(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return errors.New("no herdr session runs: start herdr first")
	}
	args := []string{"--session", sessions[0], "workspace", "create", "--cwd", dir, "--focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	out, err := herdrRun(ctx, args...)
	if err != nil {
		return err
	}
	var r struct {
		Root struct {
			Pane string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if json.Unmarshal(out, &r) != nil || r.Root.Pane == "" {
		return errors.New("herdr workspace create gave no pane")
	}
	_, err = herdrRun(ctx, "--session", sessions[0], "pane", "run", r.Root.Pane, line)
	return err
}
