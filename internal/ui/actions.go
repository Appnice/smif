package ui

import (
	"context"
	"errors"
	"time"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

// Actions are the human-only writes and the thread read the item screens
// use. DaemonSource implements them; a Source without them gives a
// read-only UI.
type Actions interface {
	// Thread is thread.get for a message or thread id.
	Thread(ctx context.Context, id string) (*api.ThreadView, error)
	// Reply is message.reply on a needs_human item.
	Reply(ctx context.Context, messageID, text string) (*api.SendResult, error)
	// Cancel is message.cancel. The result holds the state after the
	// call: rejected at once, or cancelling while a worker stops.
	Cancel(ctx context.Context, messageID string) (*api.CancelResult, error)
	// Retry is message.retry on a dead letter.
	Retry(ctx context.Context, messageID string) error
}

// Thread reads one thread with bodies (thread.get).
func (s *DaemonSource) Thread(ctx context.Context, id string) (*api.ThreadView, error) {
	var tv api.ThreadView
	if err := s.call(ctx, "thread.get", api.IDParams{ID: id}, &tv); err != nil {
		return nil, err
	}
	return &tv, nil
}

// Reply answers a needs_human item (message.reply).
func (s *DaemonSource) Reply(ctx context.Context, messageID, text string) (*api.SendResult, error) {
	var res api.SendResult
	if err := s.call(ctx, "message.reply", api.HumanReplyParams{MessageID: messageID, Text: text}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Cancel cancels a queued, waiting, needs_human or running item.
func (s *DaemonSource) Cancel(ctx context.Context, messageID string) (*api.CancelResult, error) {
	var res api.CancelResult
	if err := s.call(ctx, "message.cancel", api.IDParams{ID: messageID}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Send sends an ask or a tell as the human (the `:` prompt).
func (s *DaemonSource) Send(ctx context.Context, method string, d envelope.Draft) (*api.SendResult, error) {
	var res api.SendResult
	if err := s.call(ctx, method, api.SendParams{Draft: d}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Prompter types the human's prompt into an idle session (session.prompt,
// ADR-0012). DaemonSource implements it.
type Prompter interface {
	Prompt(ctx context.Context, sessionID, text string) (*api.SessionPromptResult, error)
}

// SessionSender writes to a session: typed in when idle, else to a worker
// with its context (session.send, ADR-0013). DaemonSource implements it.
type SessionSender interface {
	SendSession(ctx context.Context, sessionID, text string) (*api.SessionSendResult, error)
}

// Send writes text to a session.
func (s *DaemonSource) SendSession(ctx context.Context, sessionID, text string) (*api.SessionSendResult, error) {
	var res api.SessionSendResult
	if err := s.callT(ctx, "session.send", api.SessionSendParams{SessionID: sessionID, Text: text}, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}

// SessionTailer reads a session's conversation (session.tail).
// DaemonSource implements it.
type SessionTailer interface {
	SessionTail(ctx context.Context, sessionID string) (*api.SessionTail, error)
}

// SessionTail reads the last turns of one session.
func (s *DaemonSource) SessionTail(ctx context.Context, sessionID string) (*api.SessionTail, error) {
	var res api.SessionTail
	if err := s.callT(ctx, "session.tail", api.SessionTailParams{SessionID: sessionID, Limit: 30}, &res, 5*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}

// Prompt types text into an idle session and presses Enter.
func (s *DaemonSource) Prompt(ctx context.Context, sessionID, text string) (*api.SessionPromptResult, error) {
	var res api.SessionPromptResult
	if err := s.callT(ctx, "session.prompt", api.SessionPromptParams{SessionID: sessionID, Text: text}, &res, 30*time.Second); err != nil {
		return nil, err
	}
	return &res, nil
}

// Retry requeues a dead letter.
func (s *DaemonSource) Retry(ctx context.Context, messageID string) error {
	return s.call(ctx, "message.retry", api.IDParams{ID: messageID}, nil)
}

// call makes one RPC on the shared connection. It never sends a write
// twice: after a transport error it drops the connection and reports the
// error; the next poll redials. An RPC error keeps the connection.
func (s *DaemonSource) call(ctx context.Context, method string, params, out any) error {
	return s.callT(ctx, method, params, out, 10*time.Second)
}

// callT is call with another cap on the call time than 10 s. A sooner
// context deadline still wins.
func (s *DaemonSource) callT(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.c == nil {
		c, err := s.Dial()
		if err != nil {
			return err
		}
		s.c = c
	}
	if d, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(d))
	}
	err := s.c.Call(method, params, out, timeout)
	var rpcErr *api.Error
	if err != nil && !errors.As(err, &rpcErr) {
		s.c.Close()
		s.c = nil
	}
	return err
}
