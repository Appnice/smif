package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/auth"
	"github.com/appnice/smif/internal/harness"
	"github.com/appnice/smif/internal/store"
)

const bgSession = "bbbbbbbb-0000-4000-8000-000000000002"

// bgFake is a provider with background rows. It has hooks too, like Claude
// Code, so that the list must win over a hook row.
type bgFake struct{ *fakeProvider }

func (bgFake) HookEvents() []string { return nil }

func (bgFake) State(s harness.Session) (string, time.Time) { return s.Status, s.StatusAt }

func bgDaemon(t *testing.T, rows ...harness.Session) *Daemon {
	t.Helper()
	d, _ := testDaemon(t)
	d.harnesses = harness.NewRegistry(bgFake{&fakeProvider{name: harness.ClaudeName, verified: true, sessions: rows}})
	return d
}

func bgRow(id, turn string, dir string) harness.Session {
	return harness.Session{SessionID: id, AttachID: id[:8], Cwd: dir, Name: "nightly", Status: turn, StatusAt: time.Now()}
}

func bgView(t *testing.T, d *Daemon, id string) *api.SessionView {
	t.Helper()
	ss, err := d.sessionList()
	if err != nil {
		t.Fatal(err)
	}
	for i := range ss {
		if ss[i].SessionID == id {
			return &ss[i]
		}
	}
	return nil
}

// A background process that runs must not get a second one on its transcript.
func TestRunningBackgroundIsLive(t *testing.T) {
	running, stopped := bgRow(bgSession, "busy", t.TempDir()), bgRow(histA, "idle", t.TempDir())
	running.PID = os.Getpid()
	live := bgDaemon(t, running, stopped).liveClaude()
	if !live[bgSession] || live[histA] {
		t.Errorf("live %v", live)
	}
}

func TestBackgroundView(t *testing.T) {
	dir := t.TempDir()
	for turn, want := range map[string]string{"waiting": "waiting", "busy": "busy", "idle": "idle", "": "unknown"} {
		d := bgDaemon(t, bgRow(bgSession, turn, dir))
		v := bgView(t, d, bgSession)
		if v == nil || !v.Background || v.Turn != want || v.CanPrompt || v.Why != backgroundWhy || v.TTY != "" || v.Title != "nightly" {
			t.Errorf("%q: %+v", turn, v)
		}
	}
}

// The list decides a background session's state, also over a newer hook
// report, and a hook row's terminal does not give it a tab.
func TestBackgroundListBeatsHooks(t *testing.T) {
	dir := t.TempDir()
	r := bgRow(bgSession, "", dir)
	r.PID = os.Getpid()
	d := bgDaemon(t, r)
	if err := d.store.Update(func(tx *store.Tx) error {
		return tx.UpsertPresence(dir, bgSession, "active", store.PresenceUpdate{Turn: "idle", TTY: "/dev/ttys009", Term: termTmux, PID: os.Getpid()})
	}); err != nil {
		t.Fatal(err)
	}
	if v := bgView(t, d, bgSession); v == nil || v.Turn != "unknown" || v.TTY != "" || v.Term != "" || v.CanPrompt {
		t.Fatalf("view: %+v", v)
	}
}

// Done rows leave after 24 h, and an unread answer keeps them.
func TestBackgroundDoneRule(t *testing.T) {
	now := time.Now()
	const day = 24 * time.Hour
	for name, tc := range map[string]struct {
		ended, answer time.Time
		mark          *time.Time
		shown         bool
	}{
		"new":                 {ended: now.Add(-23 * time.Hour), shown: true},
		"old, never shown":    {ended: now.Add(-25 * time.Hour), answer: now.Add(-25 * time.Hour)},
		"old, read":           {ended: now.Add(-2 * day), answer: now.Add(-2 * day), mark: ptr(now.Add(-2 * day))},
		"old, unread":         {ended: now.Add(-2 * day), answer: now.Add(-2 * day), mark: ptr(now.Add(-3 * day)), shown: true},
		"old, never answered": {ended: now.Add(-2 * day), mark: ptr(time.Time{})},
	} {
		t.Run(name, func(t *testing.T) {
			r := bgRow(bgSession, "idle", t.TempDir())
			r.EndedAt, r.LastAnswerAt = tc.ended, tc.answer
			d := bgDaemon(t, r)
			if tc.mark != nil {
				b, _ := json.Marshal(map[string]time.Time{bgSession: *tc.mark})
				if err := os.WriteFile(filepath.Join(d.dataDir, "ui-seen.json"), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if shown := bgView(t, d, bgSession) != nil; shown != tc.shown {
				t.Errorf("shown = %v", shown)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

// A background row takes no typing, no queue item and no fork. Each
// refusal gives the reason, and nothing reaches the terminal.
func TestBackgroundRefusesWriting(t *testing.T) {
	dir := t.TempDir()
	for _, turn := range []string{"idle", "busy", "waiting", ""} {
		d := bgDaemon(t, bgRow(bgSession, turn, dir))
		if _, err := d.sessionPrompt(context.Background(), api.SessionPromptParams{SessionID: bgSession, Text: "go"}); !bgRefused(err) {
			t.Errorf("%q: session.prompt: %v", turn, err)
		}
		if _, err := d.sessionQueue(api.SessionQueueParams{SessionID: bgSession, Text: "go"}); !bgRefused(err) {
			t.Errorf("%q: session.queue: %v", turn, err)
		}
		if _, err := d.sessionSend(context.Background(), auth.Principal{}, api.SessionSendParams{SessionID: bgSession, Text: "go"}); !bgRefused(err) {
			t.Errorf("%q: session.send: %v", turn, err)
		}
		if _, err := d.sessionFocus(context.Background(), api.SessionFocusParams{SessionID: bgSession}); !bgRefused(err) {
			t.Errorf("%q: session.focus: %v", turn, err)
		}
		if _, err := os.Stat(typedPath(d)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q: the terminal log exists: %v", turn, err)
		}
	}
}

func bgRefused(err error) bool {
	return api.IsCode(err, api.CodeRejected) && strings.Contains(err.Error(), "background session")
}

func typedPath(d *Daemon) string {
	return strings.TrimPrefix(d.Config().Daemon.Terminal, "log:")
}

// session.attach builds argv from the config and the list. The call names
// a session and nothing else reaches argv.
func TestSessionAttachArgv(t *testing.T) {
	dir := t.TempDir()
	d := bgDaemon(t, bgRow(bgSession, "waiting", dir))
	d.harnesses.Register(bgFake{&fakeProvider{name: "other", verified: true, sessions: []harness.Session{
		{SessionID: "cccccccc-0000-4000-8000-000000000003", Cwd: dir, Status: "idle"}}}})
	ctx := context.Background()
	if _, err := d.sessionAttach(ctx, api.SessionAttachParams{SessionID: bgSession, Term: termTmux}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(typedPath(d))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Open bool
		Dir  string
		Term string
		Argv []string
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := d.Config().Defaults.ClaudeCommand + " attach " + bgSession[:8]
	if !got.Open || got.Dir != dir || got.Term != termTmux || strings.Join(got.Argv, " ") != want {
		t.Fatalf("open: %+v", got)
	}
	for _, id := range []string{"", "cccccccc-0000-4000-8000-000000000003", bgSession[:8], "x; rm -rf /", "--help"} {
		if _, err := d.sessionAttach(ctx, api.SessionAttachParams{SessionID: id}); !api.IsCode(err, api.CodeNotFound) {
			t.Errorf("id %q: %v", id, err)
		}
	}
	if _, err := d.sessionAttach(ctx, api.SessionAttachParams{SessionID: bgSession, Term: "xterm"}); !api.IsCode(err, api.CodeParams) {
		t.Errorf("term: %v", err)
	}
	if after, _ := os.ReadFile(typedPath(d)); len(after) != len(b) {
		t.Errorf("a refused call reached the terminal: %s", after)
	}
	// A field the API does not have changes nothing.
	var p api.SessionAttachParams
	raw, _ := json.Marshal(map[string]any{"session_id": bgSession, "command": "/bin/sh", "argv": []string{"/bin/sh", "-c", "id"}, "id": "x; id"})
	if err := decode(raw, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sessionAttach(ctx, p); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(typedPath(d))
	if last := after[len(b):]; !strings.Contains(string(last), `"argv":["`+d.Config().Defaults.ClaudeCommand+`","attach","`+bgSession[:8]+`"]`) {
		t.Errorf("extra fields reached argv: %s", last)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sessionAttach(ctx, api.SessionAttachParams{SessionID: bgSession}); !api.IsCode(err, api.CodeRejected) {
		t.Errorf("gone folder: %v", err)
	}
}

// Notifications treat a background session like any other, and name no tab.
func TestBackgroundNotifies(t *testing.T) {
	v := api.SessionView{Background: true}
	if s := waitText(v); strings.Contains(s, "tab") {
		t.Errorf("wait text: %s", s)
	}
	dir := t.TempDir()
	d := bgDaemon(t, bgRow(bgSession, "busy", dir))
	seen := d.watchSessions(map[string]watchedSession{}, time.Now())
	if seen[bgSession].turn != "busy" {
		t.Fatalf("seen: %+v", seen)
	}
}
