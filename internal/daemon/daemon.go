// Package daemon wires Smif together: the socket API, the store, the
// router, the scheduler, the supervisor and the runners. It is the only
// long-lived process (RFC 4.1).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/audit"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/blob"
	"github.com/appnice/smif/internal/config"
	"github.com/appnice/smif/internal/notify"
	"github.com/appnice/smif/internal/paths"
	"github.com/appnice/smif/internal/router"
	"github.com/appnice/smif/internal/runner/claude"
	"github.com/appnice/smif/internal/runner/command"
	"github.com/appnice/smif/internal/scheduler"
	"github.com/appnice/smif/internal/store"
	"github.com/appnice/smif/internal/supervisor"
	"github.com/appnice/smif/internal/workspace"
)

// Version is set at build time.
var Version = "dev"

// Options configure a daemon.
type Options struct {
	ConfigPath string
	DataDir    string
	Socket     string
	// Notifier overrides the configured notifier (tests).
	Notifier notify.Notifier
	// SkipProbe uses the verified capability set instead of probing claude.
	SkipProbe bool
	// LogWriter receives daemon logs in addition to the log file.
	LogWriter io.Writer
}

// Daemon is the running service.
type Daemon struct {
	opts      Options
	cfg       atomic.Pointer[config.Config]
	store     *store.Store
	reg       *auth.Registry
	srv       *api.Server
	blobs     *blob.Store
	audit     *audit.Log
	notifier  notify.Notifier
	lock      *os.File    // daemon.lock, held while the daemon lives
	terminal  terminal    // types the human's prompts into sessions (ADR-0012)
	promptMu  sync.Mutex  // one prompt typed at a time
	procs     procCache   // tty and terminal of discovered claude processes
	answers   answerCache // each session's last answer (discover.go)
	sessDir   string      // Claude Code's session files (discover.go)
	ws        *workspace.Manager
	claude    *claude.Runner
	commands  *command.Runner
	limiter   *router.Limiter
	log       *slog.Logger
	logFile   *logSink
	dataDir   string
	socket    string
	startedAt time.Time

	mu          sync.Mutex // routing and scheduling decisions
	slots       *scheduler.Slots
	running     map[string]*activeRun // run id -> run
	directLocks map[string]string     // repo -> run id
	waiters     map[string][]chan struct{}

	capsMu   sync.RWMutex
	caps     *claude.Caps
	claudeOK bool

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type activeRun struct {
	id, msgID, agent, handler, repo string
	cancel                          context.CancelCauseFunc
	runToken                        string
}

// errShutdown is the cancel cause used on daemon stop.
var errShutdown = errors.New("daemon shutting down")

// errCancelled is the cancel cause used by message.cancel.
var errCancelled = errors.New("cancelled by the human")

// New opens state and prepares the daemon. Call Start to serve.
func New(o Options) (*Daemon, error) {
	if o.DataDir == "" {
		o.DataDir = paths.DataDir()
	}
	if o.Socket == "" {
		o.Socket = paths.Socket()
	}
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"", "blobs", "transcripts", "worktrees", "agents", "audit", "logs", "runs"} {
		if err := os.MkdirAll(filepath.Join(o.DataDir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	// One daemon per data dir: a second one (started by the UI while the
	// service starts too) stops here, before it opens the store.
	lock, err := os.OpenFile(filepath.Join(o.DataDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another daemon runs with the data dir %s", o.DataDir)
	}
	d := &Daemon{opts: o, dataDir: o.DataDir, socket: o.Socket, startedAt: time.Now(), lock: lock,
		running: map[string]*activeRun{}, directLocks: map[string]string{}, waiters: map[string][]chan struct{}{},
		wake: make(chan struct{}, 1)}
	d.cfg.Store(cfg)

	lf, err := openLogSink(filepath.Join(o.DataDir, "logs", "smifd.jsonl"))
	if err != nil {
		return nil, err
	}
	d.logFile = lf
	var w io.Writer = lf
	if o.LogWriter != nil {
		w = io.MultiWriter(lf, o.LogWriter)
	}
	d.reg = auth.NewRegistry()
	d.log = slog.New(slog.NewJSONHandler(redactWriter{w: w, r: d.reg}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := d.reg.Load(cfg.AgentIDs()); err != nil {
		return nil, fmt.Errorf("tokens: %w", err)
	}
	st, err := store.Open(o.DataDir)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	d.store = st
	d.audit, err = audit.New(filepath.Join(o.DataDir, "audit"), d.reg.Redact)
	if err != nil {
		return nil, err
	}
	st.OnAudit = d.audit.Write
	d.blobs = blob.New(o.DataDir)
	d.ws = &workspace.Manager{Root: filepath.Join(o.DataDir, "worktrees")}
	d.notifier = o.Notifier
	if d.notifier == nil {
		d.notifier = notify.New(cfg.Daemon.Notify)
	}
	d.terminal = newTerminal(cfg.Daemon.Terminal)
	d.sessDir = claudeSessionsDir()
	d.slots = scheduler.NewSlots(cfg.Daemon.MaxWorkers, cfg.Daemon.CommandPool)
	d.limiter = router.NewLimiter(cfg.Daemon.RateLimitPerMinute)
	exe, _ := os.Executable()
	d.claude = &claude.Runner{Command: cfg.Defaults.ClaudeCommand, MaxOutput: cfg.Defaults.MaxOutputBytes, SmifBinary: exe, Socket: o.Socket,
		Protected: []string{paths.ConfigDir(), o.DataDir}}
	d.commands = &command.Runner{}
	d.srv = api.NewServer()
	d.srv.Auth = d.authenticate
	d.srv.OnDenied = d.onDenied
	d.registerMethods()
	return d, nil
}

type redactWriter struct {
	w io.Writer
	r *auth.Registry
}

func (rw redactWriter) Write(p []byte) (int, error) {
	if _, err := rw.w.Write([]byte(rw.r.Redact(string(p)))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Config returns the current config.
func (d *Daemon) Config() *config.Config { return d.cfg.Load() }

// Store exposes the store (tests).
func (d *Daemon) Store() *store.Store { return d.store }

// Start reconciles state, probes claude, binds the socket and starts the
// dispatcher. It returns once the socket accepts connections.
func (d *Daemon) Start() error {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	if err := d.reconcile(); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	d.probeClaude()
	if err := d.srv.Listen(d.socket); err != nil {
		return err
	}
	d.wg.Add(4)
	go func() { defer d.wg.Done(); _ = d.srv.Serve() }()
	go func() { defer d.wg.Done(); d.dispatchLoop() }()
	go func() { defer d.wg.Done(); d.housekeepingLoop() }()
	go func() { defer d.wg.Done(); d.sessionWatchLoop() }()
	d.log.Info("daemon started", "version", Version, "socket", d.socket, "data_dir", d.dataDir, "pid", os.Getpid(),
		"agents", d.Config().AgentIDs(), "claude_ok", d.isClaudeOK())
	d.kick()
	return nil
}

// Stop cancels running work (their messages go back to the queue without
// counting an attempt), closes the socket and the store.
func (d *Daemon) Stop() {
	d.log.Info("daemon stopping")
	d.mu.Lock()
	for _, r := range d.running {
		r.cancel(errShutdown)
	}
	d.mu.Unlock()
	d.srv.Close()
	d.cancel()
	d.wg.Wait()
	d.store.Close()
	d.logFile.Close()
	d.lock.Close()
}

// Wait blocks until the daemon stops.
func (d *Daemon) Wait() { <-d.ctx.Done() }

func (d *Daemon) kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// reconcile handles runs that were running when the daemon died: any
// recorded process group still alive is killed, the attempt is marked
// failed and a retry is scheduled (RFC 4.2, 8.4).
func (d *Daemon) reconcile() error {
	runs, err := d.store.Runs(`state=?`, store.RunRunning)
	if err != nil {
		return err
	}
	cfg := d.Config()
	for _, r := range runs {
		killed := false
		if r.PGID > 1 && supervisor.GroupAlive(r.PGID) {
			supervisor.KillGroup(r.PGID)
			killed = true
		}
		err := d.store.Update(func(tx *store.Tx) error {
			if err := tx.FinishRun(r.ID, store.RunEnd{State: store.RunOrphaned, Error: fmt.Sprintf("daemon restarted during the run (process group killed: %v)", killed)}); err != nil {
				return err
			}
			m, err := tx.GetMessage(r.MessageID)
			if err != nil {
				return err
			}
			if m.State != store.Running {
				return nil
			}
			return d.failAttemptTx(tx, cfg, m, "orphaned: daemon restarted during the run", false)
		})
		if err != nil {
			return err
		}
		d.log.Warn("orphaned run recovered", "run_id", r.ID, "message_id", r.MessageID, "agent", r.Agent, "killed_pgid", killed)
	}
	// Messages left in running without a run row (crash between commits).
	ms, err := d.store.Messages(`state='running' AND id NOT IN (SELECT message_id FROM runs WHERE state='running')`)
	if err != nil {
		return err
	}
	for _, m := range ms {
		_ = d.store.Update(func(tx *store.Tx) error {
			return d.failAttemptTx(tx, cfg, m, "orphaned: no live run after restart", false)
		})
	}
	// Ingest that crashed between accept and routing.
	ms, err = d.store.Messages(`state='accepted' ORDER BY created_at`)
	if err != nil {
		return err
	}
	for _, m := range ms {
		var notes []func()
		_ = d.store.Update(func(tx *store.Tx) error {
			var err error
			notes, err = d.routeTx(tx, cfg, m)
			return err
		})
		for _, n := range notes {
			n()
		}
	}
	return nil
}

func (d *Daemon) probeClaude() {
	cfg := d.Config()
	var caps *claude.Caps
	if d.opts.SkipProbe {
		caps = claude.AssumedCaps()
	} else {
		caps = claude.Probe(context.Background(), cfg.Defaults.ClaudeCommand)
	}
	d.capsMu.Lock()
	wasOK := d.claudeOK
	d.caps = caps
	d.claudeOK = caps.OK()
	d.claude.Caps = caps
	d.capsMu.Unlock()
	if !caps.OK() {
		d.log.Error("claude unavailable; worker runs are held", "problems", caps.Problems)
		d.notifyOnce("claude_unavailable", "", "Claude Code is unavailable; worker runs are held. Run `smif doctor`.", "claude-down")
	} else if !wasOK {
		d.clearOnce("claude-down")
		d.kick()
	}
}

func (d *Daemon) isClaudeOK() bool {
	d.capsMu.RLock()
	defer d.capsMu.RUnlock()
	return d.claudeOK
}

func (d *Daemon) setClaudeDown(reason string) {
	d.capsMu.Lock()
	d.claudeOK = false
	if d.caps != nil {
		d.caps.Problems = append(d.caps.Problems, reason)
	}
	d.capsMu.Unlock()
	d.log.Error("claude unavailable; worker runs are held", "reason", reason)
	d.notifyOnce("claude_unavailable", "", "Claude Code is unavailable ("+reason+"); worker runs are held. Run `smif doctor`.", "claude-down")
}

// notifyOnce sends a notification at most once per key until clearOnce.
func (d *Daemon) notifyOnce(kind, agent, summary, key string) {
	if d.store.GetKV("notified:"+key) != "" {
		return
	}
	_ = d.store.Update(func(tx *store.Tx) error { return tx.SetKV("notified:"+key, time.Now().Format(time.RFC3339)) })
	d.notify(kind, agent, summary, "")
}

func (d *Daemon) clearOnce(key string) {
	_ = d.store.Update(func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM kv WHERE k=?`, "notified:"+key)
		return err
	})
}

func (d *Daemon) notify(kind, agent, summary, msgID string) {
	ev := notify.Event{Kind: kind, Agent: agent, Summary: notify.Clip(summary, 280), MessageID: msgID, At: time.Now()}
	if err := d.notifier.Notify(ev); err != nil {
		d.log.Warn("notification failed", "error", err.Error(), "agent", agent, "message_id", msgID)
	}
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("notify."+kind, agent, msgID, "", "", map[string]any{"summary": ev.Summary})
	})
}

// Reload re-reads the config. An invalid config keeps the old one.
func (d *Daemon) Reload() error {
	old := d.Config()
	cfg, err := config.Load(old.Path)
	if err != nil {
		_ = d.store.Update(func(tx *store.Tx) error {
			return tx.Audit("config.reload_failed", "", "", "", "", map[string]any{"error": err.Error()})
		})
		return err
	}
	if err := d.reg.Load(cfg.AgentIDs()); err != nil {
		return err
	}
	d.mu.Lock()
	d.cfg.Store(cfg)
	d.slots.MaxWorkers, d.slots.CommandPool = cfg.Daemon.MaxWorkers, cfg.Daemon.CommandPool
	d.limiter.SetRate(cfg.Daemon.RateLimitPerMinute)
	d.claude.Command = cfg.Defaults.ClaudeCommand
	d.claude.MaxOutput = cfg.Defaults.MaxOutputBytes
	d.mu.Unlock()
	if d.opts.Notifier == nil {
		d.notifier = notify.New(cfg.Daemon.Notify)
	}
	_ = d.store.Update(func(tx *store.Tx) error {
		return tx.Audit("config.reloaded", "", "", "", "", map[string]any{"agents": cfg.AgentIDs()})
	})
	d.probeClaude()
	d.kick()
	return nil
}

func (d *Daemon) housekeepingLoop() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	lastProbe := time.Now()
	lastGC := time.Now().Add(-24*time.Hour + time.Minute) // first pass a minute after start
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-tick.C:
			d.expire()
			d.kick()
			if !d.isClaudeOK() && time.Since(lastProbe) > 5*time.Minute {
				lastProbe = time.Now()
				d.probeClaude()
			}
			if time.Since(lastGC) > 24*time.Hour {
				lastGC = time.Now()
				if _, err := d.GC(); err != nil {
					d.log.Warn("gc failed", "error", err.Error())
				}
			}
		}
	}
}
