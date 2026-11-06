// Package codex runs a read-only mail worker on `codex exec` (ADR-0022). The
// message goes in on stdin, never in argv (R-ISO-3), and the sandbox holds the
// worker to reading. A run keeps no session: each message starts fresh.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/appnice/smif/internal/runner"
	"github.com/appnice/smif/internal/runner/claude"
	"github.com/appnice/smif/internal/supervisor"
)

// schema is the result for --output-schema. Codex takes a strict schema, so
// every field is required and the optional parts of a result are left out:
// a Codex worker sends no followups.
const schema = `{"type":"object","additionalProperties":false,"required":["status","summary","reply","human_question"],"properties":{` +
	`"status":{"type":"string","enum":["done","needs_human","failed"]},` +
	`"summary":{"type":"string"},"reply":{"type":"string"},"human_question":{"type":"string"}}}`

const resultNote = `Your result has four fields: status, summary, reply and human_question. Use "" for a field with no value. You cannot send followups.`

// Runner runs `codex exec`. Command gives the program at each run, so a
// config reload needs no new Runner.
type Runner struct {
	Command   func() string
	MaxOutput int64
}

// Argv builds the command from the profile and the schema path, never from
// the message.
func (r *Runner) Argv(req *runner.Request, schemaPath string) []string {
	argv := []string{r.Command(), "exec", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral",
		"--ignore-user-config", "--ignore-rules", "--json", "--output-schema", schemaPath}
	if m := req.Profile.Model; m != "" {
		argv = append(argv, "--model", m)
	}
	return append(argv, "-") // - reads the prompt from stdin
}

// Run implements the worker handler for a Codex profile.
func (r *Runner) Run(ctx context.Context, req *runner.Request) *runner.Outcome {
	f, err := os.CreateTemp("", "smif-codex-schema-*.json")
	if err != nil {
		return &runner.Outcome{Failure: runner.FailExit, Error: "schema file: " + err.Error()}
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(schema)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return &runner.Outcome{Failure: runner.FailExit, Error: "schema file: " + err.Error()}
	}
	policy, err := claude.Policy(req.Agent.ID, req.Profile, "")
	if err != nil {
		return &runner.Outcome{Failure: runner.FailExit, Error: "policy: " + err.Error()}
	}
	prompt := policy + "\n" + claude.Prompt(req.Agent.ID, req.Message, req.Attempt, req.Attachment, true) + resultNote + "\n"
	timeout := time.Duration(req.Profile.TimeoutS) * time.Second
	sup := supervisor.Run(ctx, supervisor.Spec{
		Argv: r.Argv(req, f.Name()), Dir: req.Workspace, Env: claude.WorkerEnv(nil, req), Stdin: []byte(prompt),
		Timeout: timeout, MaxOutput: r.MaxOutput, OnStart: req.OnStart,
	})
	if req.Transcript != "" {
		_ = os.WriteFile(req.Transcript, sup.Stdout, 0o600)
	}
	o := &runner.Outcome{Stderr: sup.Stderr}
	s := readStream(sup.Stdout)
	o.InputTokens, o.OutputTokens = s.input, s.output
	switch {
	case sup.Cancelled:
		o.Failure, o.Error = runner.FailCancelled, "cancelled"
	case sup.TimedOut:
		o.Failure, o.Error = runner.FailTimeout, fmt.Sprintf("codex timed out after %s", timeout)
	case sup.OutputExceeded:
		o.Failure, o.Error = runner.FailOutput, "codex printed too much"
	case sup.Err != nil:
		o.Failure, o.Error = runner.FailExit, "start: "+sup.Err.Error()
	case s.failed != "":
		o.Failure, o.Error = runner.FailExit, "codex: "+s.failed
	case sup.ExitCode != 0:
		o.Failure, o.Error = runner.FailExit, fmt.Sprintf("codex exited %d: %s", sup.ExitCode, lastLine(sup.Stderr))
	default:
		res, err := runner.ParseResult([]byte(s.answer))
		if err != nil {
			o.Failure, o.Error = runner.FailInvalid, "codex result: "+err.Error()
		} else {
			o.Result = res
		}
	}
	return o
}

// stream is what the --json events of one run report.
type stream struct {
	answer        string
	failed        string
	input, output int64
}

// readStream reads the events. The last agent message is the result, because
// a run can say something before its first tool call.
func readStream(out []byte) stream {
	var s stream
	for _, l := range bytes.Split(out, []byte("\n")) {
		var e struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		switch {
		case e.Type == "item.completed" && e.Item.Type == "agent_message":
			s.answer = e.Item.Text
		case e.Type == "turn.completed":
			s.input += e.Usage.Input
			s.output += e.Usage.Output
		case e.Type == "turn.failed":
			s.failed = e.Error.Message
		case e.Type == "error" && s.failed == "":
			s.failed = e.Message
		}
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
