package api

import (
	"encoding/json"
	"time"

	"github.com/appnice/smif/internal/envelope"
)

// SendParams is message.send / message.ask / message.emit.
type SendParams struct {
	envelope.Draft
	// WaitS lets message.ask wait for the reply (max 600).
	WaitS int `json:"wait_s,omitempty"`
}

// SendResult acknowledges a persisted message.
type SendResult struct {
	ID            string     `json:"id"`
	ThreadID      string     `json:"thread_id"`
	State         string     `json:"state"`
	Coalesced     bool       `json:"coalesced,omitempty"`
	CoalescedInto string     `json:"coalesced_into,omitempty"`
	Reply         *ReplyView `json:"reply,omitempty"`
	Ticket        string     `json:"ticket,omitempty"`
}

// AwaitParams is message.await.
type AwaitParams struct {
	Ticket string `json:"ticket"`
	WaitS  int    `json:"wait_s,omitempty"`
}

// AwaitResult carries the reply when it is in.
type AwaitResult struct {
	Ticket string     `json:"ticket"`
	Status string     `json:"status"` // pending | answered
	State  string     `json:"state"`
	Reply  *ReplyView `json:"reply,omitempty"`
}

// ReplyView is a reply as returned to its asker.
type ReplyView struct {
	ID          string                `json:"id"`
	From        string                `json:"from"`
	Status      string                `json:"status"`
	Text        string                `json:"text"`
	Data        json.RawMessage       `json:"data,omitempty"`
	Attachments []envelope.Attachment `json:"attachments,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
}

// HumanReplyParams is message.reply.
type HumanReplyParams struct {
	MessageID string          `json:"message_id"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// IDParams carries one message, run or thread id.
type IDParams struct {
	ID string `json:"id"`
}

// Values of CancelResult.State.
const (
	// CancelRejected: the item was queued, waiting or needs_human and is
	// rejected now (reason "cancelled").
	CancelRejected = "rejected"
	// CancelCancelling: the item is running; its worker is told to stop
	// and the item stays running until the run ends (read thread.get).
	CancelCancelling = "cancelling"
)

// CancelResult is the response of message.cancel: the state the item has
// after the call (ADR-0009).
type CancelResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// MessageView is a message summary (bodies only for the human).
type MessageView struct {
	ID            string                `json:"id"`
	ThreadID      string                `json:"thread_id"`
	ParentID      string                `json:"parent_id,omitempty"`
	From          envelope.From         `json:"from"`
	To            string                `json:"to"`
	Kind          string                `json:"kind"`
	Intent        string                `json:"intent"`
	Priority      string                `json:"priority"`
	Subject       string                `json:"subject"`
	Body          string                `json:"body,omitempty"`
	Data          json.RawMessage       `json:"data,omitempty"`
	Attachments   []envelope.Attachment `json:"attachments,omitempty"`
	State         string                `json:"state"`
	Hop           int                   `json:"hop"`
	Coalesced     int                   `json:"coalesced_count"`
	Recurrence    bool                  `json:"recurrence,omitempty"`
	Attempt       int                   `json:"attempt"`
	Handler       string                `json:"handler,omitempty"`
	RuleID        string                `json:"rule_id,omitempty"`
	Summary       string                `json:"summary,omitempty"`
	Reason        string                `json:"reason,omitempty"`
	HoldReason    string                `json:"hold_reason,omitempty"`
	HumanQuestion string                `json:"human_question,omitempty"`
	ReplyStatus   string                `json:"reply_status,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

// RunView is a run record.
type RunView struct {
	ID              string          `json:"id"`
	MessageID       string          `json:"message_id"`
	Agent           string          `json:"agent"`
	Handler         string          `json:"handler"`
	Profile         string          `json:"profile,omitempty"`
	Attempt         int             `json:"attempt"`
	State           string          `json:"state"`
	PID             int             `json:"pid,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ArchivedSession string          `json:"archived_session,omitempty"`
	Workspace       string          `json:"workspace,omitempty"`
	Branch          string          `json:"branch,omitempty"`
	HeadSHA         string          `json:"head_sha,omitempty"`
	Diffstat        string          `json:"diffstat,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	CostUSD         float64         `json:"cost_usd"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	Error           string          `json:"error,omitempty"`
	Transcript      string          `json:"transcript,omitempty"`
	Stderr          string          `json:"stderr,omitempty"`
	StartedAt       time.Time       `json:"started_at"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
}

// ThreadView is thread.get.
type ThreadView struct {
	ThreadID     string        `json:"thread_id"`
	Frozen       bool          `json:"frozen"`
	FrozenReason string        `json:"frozen_reason,omitempty"`
	Messages     []MessageView `json:"messages"`
	Runs         []RunView     `json:"runs"`
}

// ListParams filters thread.list and run.list.
type ListParams struct {
	Agent     string `json:"agent,omitempty"`
	State     string `json:"state,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// AgentView is agent.list.
type AgentView struct {
	ID          string   `json:"id"`
	Repo        string   `json:"repo"`
	Intents     []string `json:"intents"`
	Queued      int      `json:"queued"`
	Running     int      `json:"running"`
	NeedsHuman  int      `json:"needs_human"`
	DeadLetters int      `json:"dead_letters"`
	BudgetUSD   float64  `json:"budget_usd,omitempty"`
	SpentUSD    float64  `json:"spent_usd_today"`
	TokensToday int64    `json:"tokens_today"`
	BudgetLeft  *float64 `json:"budget_left_usd,omitempty"`
	Interactive bool     `json:"interactive_session"`
}

// StatusView is agent.status (global when no agent is given).
type StatusView struct {
	Daemon  HealthView  `json:"daemon"`
	Agents  []AgentView `json:"agents"`
	Running []RunView   `json:"running"`
	DLQ     int         `json:"dlq"`
	Metrics Metrics     `json:"metrics"`
}

// Metrics are the RFC 8.3 counters.
type Metrics struct {
	QueueDepth   int                `json:"queue_depth"`
	Running      int                `json:"running"`
	SuccessRate  float64            `json:"success_rate_24h"`
	P50Seconds   float64            `json:"run_p50_s"`
	P95Seconds   float64            `json:"run_p95_s"`
	CostToday    map[string]float64 `json:"cost_usd_today"`
	TokensToday  map[string]int64   `json:"tokens_today"`
	DLQ          int                `json:"dlq"`
	BudgetLeft   map[string]float64 `json:"budget_left_usd"`
	RunsLast24h  int                `json:"runs_24h"`
	Coalesced24h int                `json:"coalesced_24h"`
}

// PresenceParams is presence.update.
type PresenceParams struct {
	// Event is start | heartbeat | end, or busy (a prompt was submitted)
	// and idle (the turn ended) from the hooks (ADR-0012).
	Event     string `json:"event"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
	// Agent, when set ($SMIF_AGENT in the session), wins over Cwd.
	Agent string `json:"agent,omitempty"`
	// Where the session runs, from the hook: its terminal device, the
	// claude process and $TERM_PROGRAM ("tmux" inside tmux).
	TTY  string `json:"tty,omitempty"`
	PID  int    `json:"pid,omitempty"`
	Term string `json:"term,omitempty"`
	// Title is the session's name (claude --name, /rename) and LastPrompt
	// the human's last prompt (shortened), from the transcript.
	Title      string `json:"title,omitempty"`
	LastPrompt string `json:"last_prompt,omitempty"`
}

// SessionView is one live interactive session (session.list, ADR-0012).
type SessionView struct {
	Agent      string    `json:"agent"`
	SessionID  string    `json:"session_id"`
	Repo       string    `json:"repo"`
	Turn       string    `json:"turn"` // idle | busy | unknown
	TurnAt     time.Time `json:"turn_at,omitzero"`
	LastSeen   time.Time `json:"last_seen"`
	TTY        string    `json:"tty,omitempty"`
	Term       string    `json:"term,omitempty"`
	Title      string    `json:"title,omitempty"`
	LastPrompt string    `json:"last_prompt,omitempty"`
	// LastAnswer is the final message of the session's newest answer (its
	// text after the last tool call), from the transcript; LastAnswerAt is
	// when it came. Empty while the first answer is still working.
	LastAnswer   string    `json:"last_answer,omitempty"`
	LastAnswerAt time.Time `json:"last_answer_at,omitzero"`
	// CanPrompt is true when session.prompt would type into it now; Why
	// says what is missing otherwise.
	CanPrompt bool   `json:"can_prompt"`
	Why       string `json:"why,omitempty"`
	// Unsent says why the last prompt typed in the background started no
	// turn (since UnsentAt; cleared by the session's next turn).
	Unsent   string    `json:"unsent,omitempty"`
	UnsentAt time.Time `json:"unsent_at,omitzero"`
	// Activity is what the newest exchange does now, from the transcript
	// (nil when there is none).
	Activity *SessionActivity `json:"activity,omitempty"`
}

// SessionActivity is the newest exchange of a session: the human's prompt
// it answers, its tool calls so far (the last few, oldest first) and Now,
// the call without a result yet: the tool that runs, or the permission or
// question a waiting session shows in its tab. WroteAt is when the
// transcript last changed: a busy session that has not written for
// minutes may be stuck.
type SessionActivity struct {
	Prompt   string        `json:"prompt,omitempty"`
	PromptAt time.Time     `json:"prompt_at,omitzero"`
	Tools    int           `json:"tools"`
	Steps    []SessionStep `json:"steps,omitempty"`
	Now      *SessionStep  `json:"now,omitempty"`
	WroteAt  time.Time     `json:"wrote_at,omitzero"`
}

// SessionStep is one tool call: the tool, what it works on (a command, a
// file, a question), when, and whether its result came back.
type SessionStep struct {
	Tool   string    `json:"tool"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
	Done   bool      `json:"done"`
}

// SessionPromptParams is session.prompt: the human's text typed into an
// idle session. SessionID wins; with Agent alone, that agent must have
// exactly one idle session.
type SessionPromptParams struct {
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	// Name picks the session by its name (claude --name, /rename; case
	// does not matter); with no session of that name, it is an agent.
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
	// Async returns once the session is checked: the text is typed in the
	// background and a session that took no turn shows Unsent.
	Async bool `json:"async,omitempty"`
}

// SavedPrompt is one of the human's saved prompts (prompts.list, ADR-0015).
type SavedPrompt struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// SessionNewParams is session.new: open a new terminal tab that runs claude
// in Dir, named Name (ADR-0015). Term picks the terminal ("Apple_Terminal",
// "iTerm.app", "tmux"; "" = the daemon's default).
type SessionNewParams struct {
	Dir  string `json:"dir"`
	Name string `json:"name,omitempty"`
	Term string `json:"term,omitempty"`
}

// SessionFocusParams is session.focus: bring the session's terminal tab to
// the front.
type SessionFocusParams struct {
	SessionID string `json:"session_id"`
}

// SessionTailParams is session.tail: the last turns of one session.
type SessionTailParams struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"` // default 20, at most 100
}

// SessionTurn is one prompt ("you") or answer ("claude") from a session's
// transcript, or a question to a fork of the session ("ask", From the
// asker) and its answer ("worker", with the ask's State) (ADR-0013). An
// answer counts its tool calls.
type SessionTurn struct {
	Role  string    `json:"role"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	Tools int       `json:"tools,omitempty"`
	From  string    `json:"from,omitempty"`
	State string    `json:"state,omitempty"`
}

// SessionSendParams is session.send: the human writes to a session. An
// idle session gets the text typed in (session.prompt); a busy or waiting
// one sends it to a worker that resumes a fork of the session (ADR-0013).
type SessionSendParams struct {
	SessionID string `json:"session_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Text      string `json:"text"`
	Async     bool   `json:"async,omitempty"` // as in SessionPromptParams
}

// SessionSendResult says where the text went: "typed" into the session or
// to a "worker" (MessageID is its ask).
type SessionSendResult struct {
	Mode      string `json:"mode"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id,omitempty"`
}

// SessionTail is the conversation of one interactive session, for the
// human only (it never goes into a session or to an agent).
type SessionTail struct {
	SessionID string        `json:"session_id"`
	Turns     []SessionTurn `json:"turns"`
}

// SessionPromptResult names the session that got the prompt.
type SessionPromptResult struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

// CountsParams is inbox.counts.
type CountsParams struct {
	Cwd   string `json:"cwd,omitempty"`
	Agent string `json:"agent,omitempty"`
	// SessionID is the asking session (the status line): it is left out of
	// SessionsWaiting and SessionsAnswered.
	SessionID string `json:"session_id,omitempty"`
}

// Counts is inbox.counts and smif_status.
type Counts struct {
	Agent            string `json:"agent,omitempty"`
	Running          int    `json:"running"`
	Queued           int    `json:"queued"`
	Handled          int    `json:"handled"`
	NeedsHuman       int    `json:"needs_human"`
	RepliesReady     int    `json:"replies_ready"`
	DirectLock       bool   `json:"direct_lock,omitempty"`
	GlobalNeedsHuman int    `json:"global_needs_human"`
	GlobalDLQ        int    `json:"global_dlq"`
	ClaudeOK         bool   `json:"claude_ok"`
	// The other live sessions that need the human (human callers only,
	// ADR-0014): the names of those that wait in their tab, and of those
	// with an answer the human has not seen (the UI's seen file).
	SessionsWaiting  []string `json:"sessions_waiting,omitempty"`
	SessionsAnswered []string `json:"sessions_answered,omitempty"`
}

// DigestParams is digest.get.
type DigestParams struct {
	Agent   string `json:"agent,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Since   string `json:"since,omitempty"` // "last" (default) or "all"
	Advance bool   `json:"advance"`
	Limit   int    `json:"limit,omitempty"`
	// Until, when above 0, keeps the items and the cursor advance at or
	// below this handled seq (a DigestResult.Cursor the caller already
	// showed), so items handled later are not marked read (ADR-0008).
	Until int64 `json:"until,omitempty"`
}

// DigestItem is one handled item.
type DigestItem struct {
	MessageID     string    `json:"message_id"`
	ThreadID      string    `json:"thread_id"`
	Agent         string    `json:"agent"`
	From          string    `json:"from"`
	Kind          string    `json:"kind"`
	Intent        string    `json:"intent"`
	Subject       string    `json:"subject"`
	State         string    `json:"state"`
	Summary       string    `json:"summary,omitempty"`
	HumanQuestion string    `json:"human_question,omitempty"`
	Coalesced     int       `json:"coalesced_count,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	At            time.Time `json:"at"`
}

// DigestResult is digest.get.
type DigestResult struct {
	Agents []string     `json:"agents"`
	Items  []DigestItem `json:"items"`
	Cursor int64        `json:"cursor"`
}

// AuditParams is audit.since: audit rows after a cursor (human only,
// ADR-0008).
type AuditParams struct {
	// After is the cursor: rows with seq > After.
	After int64 `json:"after"`
	// Limit caps the rows (default 200, max 1000).
	Limit int `json:"limit,omitempty"`
	// FromEnd returns the newest Limit rows instead (Limit 0: none) and a
	// cursor at the head, so a follower starts at "now".
	FromEnd bool `json:"from_end,omitempty"`
}

// AuditEvent is one audit row (the same fields as the JSONL log).
type AuditEvent struct {
	Seq       int64          `json:"seq"`
	TS        time.Time      `json:"ts"`
	Event     string         `json:"event"`
	Agent     string         `json:"agent,omitempty"`
	MessageID string         `json:"message_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	ThreadID  string         `json:"thread_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// AuditPage is the audit.since result.
type AuditPage struct {
	Events []AuditEvent `json:"events"`
	// Cursor is the After of the next call.
	Cursor int64 `json:"cursor"`
	// Head is the newest seq in the table. Head < the caller's cursor
	// means a new database: start again with FromEnd.
	Head int64 `json:"head"`
	// More is true when rows after Cursor exist already.
	More bool `json:"more"`
}

// BlobPutParams uploads an attachment.
type BlobPutParams struct {
	Name string `json:"name"`
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"data"` // base64 in JSON
}

// BlobGetResult returns blob bytes.
type BlobGetResult struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
	Data   []byte `json:"data"`
}

// HealthView is admin.health.
type HealthView struct {
	OK             bool     `json:"ok"`
	Version        string   `json:"version"`
	SchemaVersion  int      `json:"schema_version"`
	DB             string   `json:"db"`
	ClaudeVersion  string   `json:"claude_version"`
	ClaudeOK       bool     `json:"claude_ok"`
	ClaudeProblems []string `json:"claude_problems,omitempty"`
	FlagProbe      []string `json:"flag_probe,omitempty"`
	// Sessions checks the Claude Code files the session view reads (session
	// files, transcripts; not a documented interface, ADR-0012): "ok",
	// "none" (no files: hooks only) or "changed" with the detail.
	Sessions       string    `json:"sessions"`
	SessionsDetail string    `json:"sessions_detail,omitempty"`
	ConfigPath     string    `json:"config_path"`
	StartedAt      time.Time `json:"started_at"`
	PID            int       `json:"pid"`
}

// GCResult reports admin.gc.
type GCResult struct {
	Messages    int `json:"messages_deleted"`
	Runs        int `json:"runs_deleted"`
	Transcripts int `json:"transcripts_deleted"`
	Blobs       int `json:"blobs_deleted"`
	Branches    int `json:"branches_deleted"`
	AuditFiles  int `json:"audit_files_compressed"`
	Logs        int `json:"logs_deleted"`
}

// StatsParams is stats.get (human only, ADR-0011): counts over a window.
type StatsParams struct {
	// Since is the start of the window (default: 14 days ago).
	Since time.Time `json:"since,omitempty"`
}

// StatsRow counts one slice of the window: all, one agent or one day.
// Inbound = asks, tells and events created in the window (replies are
// counted apart). Each inbound message lands in exactly one outcome:
// Auto (done, never needs_human), Answered/Waiting/CancelledNeeded (it
// needed the human), Cancelled (rejected by the human without needing
// them), Dead, Rejected (by a guard or rule), Expired or InProgress.
type StatsRow struct {
	Key             string         `json:"key"`
	Inbound         int            `json:"inbound"`
	Auto            int            `json:"auto"`
	AutoBy          map[string]int `json:"auto_by,omitempty"` // handler → count
	NeededYou       int            `json:"needed_you"`
	Answered        int            `json:"answered"`
	Waiting         int            `json:"waiting"`
	CancelledNeeded int            `json:"cancelled_needed"`
	Cancelled       int            `json:"cancelled"`
	Dead            int            `json:"dead"`
	Rejected        int            `json:"rejected"`
	Expired         int            `json:"expired"`
	InProgress      int            `json:"in_progress"`
	Merged          int            `json:"merged"` // repeats coalesced into an existing message
	Runs            int            `json:"runs"`
	WorkerRuns      int            `json:"worker_runs"`
	CommandRuns     int            `json:"command_runs"`
	FailedRuns      int            `json:"failed_runs"`
	InputTokens     int64          `json:"input_tokens"` // incl. cache reads and writes
	OutputTokens    int64          `json:"output_tokens"`
	CostUSD         float64        `json:"cost_usd"` // as claude reports it
}

// SessionStats counts what the human did with sessions: prompts typed into
// idle ones, asks a fork of a busy one answered (from the human, from
// agents), and tabs brought to the front.
type SessionStats struct {
	Typed      int `json:"typed"`
	WorkerAsks int `json:"worker_asks"`
	AgentAsks  int `json:"agent_asks"`
	Focus      int `json:"focus"`
}

// StatsResult is stats.get.
type StatsResult struct {
	Since   time.Time  `json:"since"`
	Until   time.Time  `json:"until"`
	Total   StatsRow   `json:"total"`
	Agents  []StatsRow `json:"agents"`
	Days    []StatsRow `json:"days"`
	Replies int        `json:"replies"`
	// Sessions counts the session cockpit in the window (ADR-0012, 0013).
	Sessions SessionStats `json:"sessions"`
	// KeptFrom is where the retention window starts: older data is gone.
	KeptFrom time.Time `json:"kept_from"`
}

// ShortSession is the first 8 characters of a Claude Code session id (a
// UUID), as `claude --resume` users know it.
func ShortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
