package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/envelope"
)

// usage is the token count of one claude -p result event.
type usage struct {
	Input, CacheWrite, CacheRead, Output int64
	CostUSD                              float64 // as claude reports it (API-equivalent; not billed on a subscription)
	Turns                                int
}

// parseResult reads the result event of a stream-JSON transcript or a
// --output-format json reply.
func parseResult(b []byte) (usage, error) {
	var last []byte
	for _, l := range bytes.Split(b, []byte("\n")) {
		if bytes.Contains(l, []byte(`"type":"result"`)) {
			last = l
		}
	}
	if last == nil {
		last = bytes.TrimSpace(b)
	}
	var r struct {
		Usage struct {
			In  int64 `json:"input_tokens"`
			CW  int64 `json:"cache_creation_input_tokens"`
			CR  int64 `json:"cache_read_input_tokens"`
			Out int64 `json:"output_tokens"`
		} `json:"usage"`
		Cost  float64 `json:"total_cost_usd"`
		Turns int     `json:"num_turns"`
	}
	if err := json.Unmarshal(last, &r); err != nil {
		return usage{}, err
	}
	return usage{r.Usage.In, r.Usage.CW, r.Usage.CR, r.Usage.Out, r.Cost, r.Turns}, nil
}

// realTokens (opt-in, -real): the same question answered by a plain
// `claude -p` in the repo and by a Smif worker, on haiku, through the
// existing Claude Code login (subscription). It measures what the bytes/4
// estimates of T1 only guess: the net token cost of a handled message.
func (b *bench) realTokens(trials int) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not on PATH")
	}
	profile := `[profiles.ro]
model = "haiku"
allowed_tools = ["Read", "Grep", "Glob"]
write_scope = "none"
max_turns = 6
timeout_s = 180
max_cost_usd = 0.25
`
	e, err := b.env("", profile, agentSpec{id: "asker", rules: digestRules},
		agentSpec{id: "app", rules: "[[agents.AGENT.rules]]\nmatch = {}\nhandler = \"worker\"\nprofile = \"ro\"\n",
			files: map[string]string{"VERSION": "4.7.1\n"}})
	if err != nil {
		return err
	}
	defer e.close()
	// The real claude, with the real login: HOME and no CLAUDE_CONFIG_DIR.
	cfgPath := filepath.Join(e.root, "cfg", "config.toml")
	cfg, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.Replace(string(cfg), e.fake, claude, 1)), 0o600)
	for i, kv := range e.vars {
		switch {
		case strings.HasPrefix(kv, "HOME="):
			e.vars[i] = "HOME=" + os.Getenv("HOME")
		case strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR="):
			e.vars[i] = "SMIF_UNUSED=1"
		}
	}
	if err := e.start(); err != nil {
		return err
	}
	question := "What version is in the VERSION file at the repository root? Reply with the version only."
	var viaSmif, plain []usage
	for i := 0; i < trials; i++ {
		c, err := e.client("asker", "interactive")
		if err != nil {
			return err
		}
		var res api.SendResult
		err = c.Call("message.ask", api.SendParams{Draft: envelope.Draft{To: "app", Kind: "ask", Intent: "version.get",
			Subject: "version?", Body: question}, WaitS: 170}, &res, 180*time.Second)
		c.Close()
		if err != nil {
			return err
		}
		var tv api.ThreadView
		if err := e.human("thread.get", api.IDParams{ID: res.ID}, &tv); err != nil || len(tv.Runs) == 0 {
			return fmt.Errorf("no run for %s: %v", res.ID, err)
		}
		tr, err := os.ReadFile(tv.Runs[len(tv.Runs)-1].Transcript)
		if err != nil {
			return err
		}
		u, err := parseResult(tr)
		if err != nil {
			return err
		}
		viaSmif = append(viaSmif, u)

		cmd := exec.Command(claude, "-p", "--model", "haiku", "--output-format", "json", question)
		cmd.Dir = e.repos["app"]
		cmd.Env = append(os.Environ(), "SMIF_RUN_ID=bench") // keeps the user's smif hooks quiet
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("plain claude -p: %v", err)
		}
		u, err = parseResult(out)
		if err != nil {
			return err
		}
		plain = append(plain, u)
	}
	row := func(name string, us []usage) string {
		var a usage
		for _, u := range us {
			a.Input += u.Input
			a.CacheWrite += u.CacheWrite
			a.CacheRead += u.CacheRead
			a.Output += u.Output
			a.CostUSD += u.CostUSD
		}
		n := int64(len(us))
		return fmt.Sprintf("| %s | %d | %d | %d | %d | **%d** | $%.4f |", name, a.Input/n, a.CacheWrite/n, a.CacheRead/n,
			a.Output/n, (a.Input+a.CacheWrite+a.CacheRead)/n, a.CostUSD/float64(n))
	}
	b.add("TR", fmt.Sprintf("### T-real · actual tokens of one handled question (haiku, subscription)\n\nThe same question, %d "+
		"trials each: a Smif worker (profile `ro`: Read/Grep/Glob, no MCP, no hooks) versus a plain `claude -p` in the "+
		"same repo with the user's normal setup. Means per run; the cost column is what claude reports (API-equivalent; "+
		"on a subscription it is not billed).\n\n| | input | cache write | cache read | output | **context tokens** | reported cost |\n"+
		"|---|---|---|---|---|---|---|\n%s\n%s\n", trials, row("Smif worker", viaSmif), row("plain claude -p", plain)),
		map[string]any{"smif": viaSmif, "plain": plain})
	return nil
}
