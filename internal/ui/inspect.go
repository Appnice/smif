package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/appnice/smif/internal/api"
)

// Inspector is the read side of the thread, run and inbox screens plus
// run.apply. DaemonSource implements it with existing human-only methods.
type Inspector interface {
	// Messages is thread.list with an agent and state filter.
	Messages(ctx context.Context, p api.ListParams) ([]api.MessageView, error)
	// Run is run.get.
	Run(ctx context.Context, id string) (*api.RunView, error)
	// Diff is run.diff: the worker branch against the main checkout.
	Diff(ctx context.Context, runID string) (*DiffView, error)
	// Transcript reads the tail of the run's stream-JSON transcript.
	Transcript(ctx context.Context, r api.RunView) (*TranscriptTail, error)
	// Apply is run.apply: merge the worker branch into the main checkout.
	Apply(ctx context.Context, runID string) (*ApplyResult, error)
}

// DiffView is the run.diff result.
type DiffView struct {
	RunID  string `json:"run_id"`
	Branch string `json:"branch"`
	Repo   string `json:"repo"`
	Diff   string `json:"diff"`
}

// ApplyResult is the run.apply result.
type ApplyResult struct {
	RunID  string `json:"run_id"`
	Branch string `json:"branch"`
	Repo   string `json:"repo"`
	Head   string `json:"head"`
}

// TranscriptTail is the end of a transcript file.
type TranscriptTail struct {
	Path string
	Size int64 // the whole file
	Data []byte
	// Cut is true when Data starts after the beginning of the file.
	Cut bool
}

// TranscriptTailBytes caps how much of a transcript the run screen reads.
const TranscriptTailBytes = 256 << 10

// Messages lists messages (thread.list).
func (s *DaemonSource) Messages(ctx context.Context, p api.ListParams) ([]api.MessageView, error) {
	var ms []api.MessageView
	if err := s.call(ctx, "thread.list", p, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// Run reads one run record (run.get).
func (s *DaemonSource) Run(ctx context.Context, id string) (*api.RunView, error) {
	var r api.RunView
	if err := s.call(ctx, "run.get", api.IDParams{ID: id}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Diff reads the worker branch diff (run.diff). git can take a while on a
// big repo, so the call gets 30 s.
func (s *DaemonSource) Diff(ctx context.Context, runID string) (*DiffView, error) {
	var d DiffView
	if err := s.callT(ctx, "run.diff", api.IDParams{ID: runID}, &d, 30*time.Second); err != nil {
		return nil, err
	}
	return &d, nil
}

// Apply merges the worker branch (run.apply). The daemon refuses on a
// dirty tree. The call gets 60 s, like `smif apply`.
func (s *DaemonSource) Apply(ctx context.Context, runID string) (*ApplyResult, error) {
	var a ApplyResult
	if err := s.callT(ctx, "run.apply", api.IDParams{ID: runID}, &a, 60*time.Second); err != nil {
		return nil, err
	}
	return &a, nil
}

// Transcript reads the last TranscriptTailBytes of the transcript file
// that run.get names. The daemon and the UI share the host (the socket is
// local), so this is the same file `smif run --transcript` prints.
func (s *DaemonSource) Transcript(ctx context.Context, r api.RunView) (*TranscriptTail, error) {
	return ReadTranscriptTail(r.Transcript, TranscriptTailBytes)
}

// ReadTranscriptTail reads up to max bytes from the end of path, starting
// at a line boundary.
func ReadTranscriptTail(path string, max int64) (*TranscriptTail, error) {
	if path == "" {
		return nil, errors.New("this run has no transcript (a command handler, or the run did not start claude)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t := &TranscriptTail{Path: path, Size: fi.Size()}
	off := int64(0)
	if fi.Size() > max {
		off = fi.Size() - max
		t.Cut = true
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	t.Data, err = io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, err
	}
	if t.Cut {
		// Drop the partial first line.
		for i, b := range t.Data {
			if b == '\n' {
				t.Data = t.Data[i+1:]
				break
			}
		}
	}
	return t, nil
}

// rpcMessage is the daemon's message without the "(code N)" suffix.
func rpcMessage(err error) string {
	var e *api.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
