// Package runner defines the handler interface and the structured result
// every handler returns (RFC 6.3, 7.5, R-RUN-2).
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/appnice/mailroom/internal/config"
	"github.com/appnice/mailroom/internal/envelope"
)

// Followup is a message the daemon sends after the run on the worker's behalf.
type Followup struct {
	To       string          `json:"to"`
	Kind     string          `json:"kind"`
	Intent   string          `json:"intent"`
	Subject  string          `json:"subject"`
	Body     string          `json:"body,omitempty"`
	Priority string          `json:"priority,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Artifact is a file the handler produced, relative to its workspace.
type Artifact struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// Result is the validated handler result.
type Result struct {
	Status        string          `json:"status"`
	Summary       string          `json:"summary"`
	Reply         string          `json:"reply,omitempty"`
	ReplyData     json.RawMessage `json:"reply_data,omitempty"`
	Artifacts     []Artifact      `json:"artifacts,omitempty"`
	HumanQuestion string          `json:"human_question,omitempty"`
	Followups     []Followup      `json:"followups,omitempty"`
}

// ResultSchema is the JSON Schema passed to --json-schema.
const ResultSchema = `{"type":"object","additionalProperties":false,"required":["status","summary"],"properties":{` +
	`"status":{"type":"string","enum":["done","needs_human","failed"]},` +
	`"summary":{"type":"string","maxLength":280},` +
	`"reply":{"type":"string","maxLength":32768},` +
	`"reply_data":{"type":"object"},` +
	`"artifacts":{"type":"array","items":{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"description":{"type":"string"}}}},` +
	`"human_question":{"type":"string"},` +
	`"followups":{"type":"array","items":{"type":"object","required":["to","kind","intent","subject"],"properties":{` +
	`"to":{"type":"string"},"kind":{"type":"string","enum":["tell","ask","event"]},"intent":{"type":"string"},` +
	`"subject":{"type":"string"},"body":{"type":"string"},"priority":{"type":"string","enum":["low","normal","high","urgent"]}}}}}}`

// ParseResult decodes and validates a result object.
func ParseResult(b []byte) (*Result, error) {
	var r Result
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("result is not valid JSON for the schema: %w", err)
	}
	return &r, r.Validate()
}

// Validate enforces the RFC 6.3 schema.
func (r *Result) Validate() error {
	switch r.Status {
	case "done", "needs_human", "failed":
	default:
		return fmt.Errorf("status %q: must be done, needs_human or failed", r.Status)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if utf8.RuneCountInString(r.Summary) > envelope.MaxSummary {
		return fmt.Errorf("summary is longer than 280 characters")
	}
	if len(r.Reply) > envelope.MaxBody {
		return fmt.Errorf("reply is longer than 32 KiB")
	}
	if len(r.ReplyData) > 0 {
		var m map[string]any
		if err := json.Unmarshal(r.ReplyData, &m); err != nil {
			return fmt.Errorf("reply_data must be an object")
		}
		if len(r.ReplyData) > envelope.MaxData {
			return fmt.Errorf("reply_data is larger than 64 KiB")
		}
	}
	if r.Status == "needs_human" && strings.TrimSpace(r.HumanQuestion) == "" {
		return fmt.Errorf("human_question is required when status is needs_human")
	}
	for i, a := range r.Artifacts {
		p := filepath.Clean(a.Path)
		if a.Path == "" || filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("artifacts[%d].path %q must be relative to the workspace", i, a.Path)
		}
	}
	for i, f := range r.Followups {
		if f.To == "" || f.Subject == "" {
			return fmt.Errorf("followups[%d]: to and subject are required", i)
		}
		if f.Kind != "tell" && f.Kind != "ask" && f.Kind != "event" {
			return fmt.Errorf("followups[%d].kind %q: must be tell, ask or event", i, f.Kind)
		}
		if !config.ValidIntent(f.Intent) {
			return fmt.Errorf("followups[%d].intent %q is not a valid intent", i, f.Intent)
		}
		if len(f.Body) > envelope.MaxBody {
			return fmt.Errorf("followups[%d].body is longer than 32 KiB", i)
		}
	}
	if len(r.Followups) > 5 {
		return fmt.Errorf("at most 5 followups per run")
	}
	return nil
}

// Failure classifies why a run did not produce a result.
type Failure string

// Failure kinds.
const (
	FailNone          Failure = ""
	FailTimeout       Failure = "timeout"
	FailOutput        Failure = "output_exceeded"
	FailExit          Failure = "exit"
	FailInvalid       Failure = "invalid_result"
	FailOverload      Failure = "overload"
	FailContext       Failure = "context_length"
	FailUnavailable   Failure = "claude_unavailable"
	FailCancelled     Failure = "cancelled"
	FailHandlerFailed Failure = "handler_failed" // result status failed
	FailMaxTurns      Failure = "max_turns"
	FailBudget        Failure = "run_budget"
	FailSessionLost   Failure = "session_lost" // --resume id no longer exists
)

// Request is what the daemon hands a runner.
type Request struct {
	Agent      *config.Agent
	Message    *envelope.Envelope
	Attempt    int
	RunID      string
	RunToken   string
	Profile    config.Profile
	Workspace  string            // cwd
	BlobDir    string            // per-run read-only attachment copies
	Attachment map[string]string // attachment name -> read-only path
	SessionID  string            // worker session to resume ("" = new)
	// ForkSession resumes SessionID as a new session (--fork-session): a
	// worker that answers with an interactive session's context (ADR-0013).
	ForkSession bool
	Memory      string   // memory.md contents for the policy prompt
	Transcript  string   // where to write the raw stream
	Command     []string // command handler argv
	Timeout     int      // command handler timeout in seconds
	// OnStart reports the process ids; OnSession reports the session id.
	OnStart   func(pid, pgid int)
	OnSession func(id string)
}

// Outcome is what a runner returns.
type Outcome struct {
	Result       *Result
	Failure      Failure
	Error        string
	SessionID    string
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
	UsageJSON    string
	Stderr       string
	NumTurns     int
	Repaired     bool
}

// Runner runs one handler invocation.
type Runner interface {
	Run(ctx context.Context, req *Request) *Outcome
}
