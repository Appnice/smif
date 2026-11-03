// Package command runs deterministic command handlers (RFC 7.5): the
// envelope on stdin, one JSON result object on stdout, no LLM.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/mailroom/internal/paths"
	"github.com/appnice/mailroom/internal/runner"
	"github.com/appnice/mailroom/internal/runner/claude"
	"github.com/appnice/mailroom/internal/supervisor"
)

// Runner executes command handlers.
type Runner struct {
	BaseEnv []string
}

// Run implements runner.Runner.
func (r *Runner) Run(ctx context.Context, req *runner.Request) *runner.Outcome {
	if len(req.Command) == 0 {
		return &runner.Outcome{Failure: runner.FailExit, Error: "no command configured"}
	}
	argv := append([]string{}, req.Command...)
	argv[0] = resolveProgram(argv[0], req.Workspace)
	env, _ := json.Marshal(req.Message)
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	sup := supervisor.Run(ctx, supervisor.Spec{
		Argv: argv, Dir: req.Workspace, Env: claude.WorkerEnv(r.BaseEnv, req), Stdin: env,
		Timeout: timeout, MaxOutput: 1 << 20, OnStart: req.OnStart,
	})
	o := &runner.Outcome{Stderr: sup.Stderr}
	switch {
	case sup.Cancelled:
		o.Failure, o.Error = runner.FailCancelled, "cancelled"
	case sup.TimedOut:
		o.Failure, o.Error = runner.FailTimeout, fmt.Sprintf("command timed out after %s", timeout)
	case sup.OutputExceeded:
		o.Failure, o.Error = runner.FailOutput, "command printed more than 1 MiB"
	case sup.Err != nil:
		o.Failure, o.Error = runner.FailExit, "start: "+sup.Err.Error()
	case sup.ExitCode != 0:
		o.Failure, o.Error = runner.FailExit, fmt.Sprintf("command exited %d: %s", sup.ExitCode, strings.TrimSpace(lastLine(sup.Stderr)))
	default:
		res, err := runner.ParseResult(trimJSON(sup.Stdout))
		if err != nil {
			o.Failure, o.Error = runner.FailInvalid, "command output: "+err.Error()
		} else {
			o.Result = res
		}
	}
	return o
}

func trimJSON(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	// Allow log lines before the JSON object; take the last line that parses.
	if !strings.HasPrefix(s, "{") {
		lines := strings.Split(s, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
				return []byte(strings.Join(lines[i:], "\n"))
			}
		}
	}
	return []byte(s)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// resolveProgram expands a leading ~/ to the home directory and resolves a
// relative path with a slash (./scripts/x.sh) against the agent's repo. A
// bare name is left for PATH.
func resolveProgram(prog, workspace string) string {
	switch {
	case strings.HasPrefix(prog, "~/"):
		return paths.Expand(prog)
	case strings.Contains(prog, "/") && !filepath.IsAbs(prog):
		return filepath.Join(workspace, prog)
	}
	return prog
}
