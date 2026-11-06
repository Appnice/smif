package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/api"
)

// historySample is what the fake daemon lists. The first row is also on
// the restore list of closedSnapshot.
func historySample() []api.HistoryRow {
	return []api.HistoryRow{
		{SessionID: "aaaaaaaa-0000-4000-8000-000000000001", Dir: "/work/shop", Name: "fix the checkout", At: t0.Add(-5 * time.Minute)},
		{SessionID: "dddddddd-0000-4000-8000-000000000004", Dir: "/work/shop", Name: "login form and a very long first prompt that goes on and on past the end of any small terminal, and then some more words to be sure of it", At: t0.Add(-time.Hour), Open: true},
		{SessionID: "eeeeeeee-0000-4000-8000-000000000005", Dir: "/work/blog", Name: "", At: t0.Add(-26 * time.Hour)},
		{SessionID: "ffffffff-0000-4000-8000-000000000006", Dir: "/work/blog", Name: "add an RSS feed", At: t0.Add(-72 * time.Hour)},
		{SessionID: "99999999-0000-4000-8000-000000000007", Dir: "/work/api", Name: "rate limits", At: t0.Add(-240 * time.Hour)},
	}
}

func closedSnapshot() Snapshot {
	s := sample()
	s.Closed.Sessions = []api.ClosedSession{
		{SessionID: "aaaaaaaa-0000-4000-8000-000000000001", Dir: "/work/shop", Name: "fix the checkout", ClosedAt: t0.Add(-time.Minute), DiedWorking: true},
		{SessionID: "bbbbbbbb-0000-4000-8000-000000000002", Dir: "/work/api", Name: "rename the client", ClosedAt: t0.Add(-time.Minute)},
		{SessionID: "cccccccc-0000-4000-8000-000000000003", Dir: "/work/blog", Name: "", ClosedAt: t0.Add(-time.Minute)},
	}
	return s
}

func (f *fakeDigest) SessionHistory(ctx context.Context) ([]api.HistoryRow, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	if f.noHistory {
		return nil, nil
	}
	return historySample(), nil
}

func (f *fakeDigest) SearchSessions(ctx context.Context, q string) (*api.SessionSearchResult, error) {
	f.searched = append(f.searched, q)
	if q == "nothing" {
		return &api.SessionSearchResult{}, nil
	}
	row := historySample()[1]
	return &api.SessionSearchResult{Stopped: "time", Hits: []api.SearchHit{
		{HistoryRow: api.HistoryRow{SessionID: row.SessionID, Dir: row.Dir, Name: row.Name, At: t0.Add(-time.Hour)}, Role: "you", Text: "the login form breaks on a long name"},
		{HistoryRow: api.HistoryRow{SessionID: row.SessionID, Dir: row.Dir, Name: row.Name, At: t0.Add(-59 * time.Minute)}, Role: "claude", Text: "…the login form had a fixed width, so I changed it to wrap, and the test in login_test.go passes now and for a long name too…"},
	}}, nil
}

func (f *fakeDigest) ResumeSession(ctx context.Context, id, term string) error {
	f.resumed = append(f.resumed, id)
	return f.resumeErr
}

func (f *fakeDigest) ReopenSessions(ctx context.Context, term string) (*api.SessionReopenResult, error) {
	f.reopened++
	return &api.SessionReopenResult{Opening: 20, Left: 3}, nil
}

// R lists the closed sessions first, each once, with its mark in text.
// enter reopens the row and A the list.
func TestHistoryScreenKeys(t *testing.T) {
	m, f := newFullModel(t, closedSnapshot(), 120, 30)
	key(m, "v", "R")
	v := view(m)
	first := strings.Index(v, "died working")
	second := strings.Index(v, "rename the client")
	third := strings.Index(v, "login form")
	if first < 0 || second < first || third < second {
		t.Fatalf("order:\n%s", v)
	}
	if strings.Count(v, "fix the checkout") != 1 {
		t.Errorf("a closed session shows twice:\n%s", v)
	}
	if !strings.Contains(v, "open") {
		t.Errorf("no open mark:\n%s", v)
	}
	key(m, "down", "enter")
	if len(f.resumed) != 1 || f.resumed[0] != "bbbbbbbb-0000-4000-8000-000000000002" {
		t.Fatalf("resumed: %v", f.resumed)
	}
	if !strings.Contains(m.status, "Opened rename the client") {
		t.Errorf("status: %q", m.status)
	}
	f.resumeErr = errors.New("the session's folder /work/api is gone")
	key(m, "enter")
	if !strings.Contains(m.status, "Not opened") || !strings.Contains(m.status, "gone") || !m.statusBad {
		t.Errorf("status: %q", m.status)
	}
	key(m, "A")
	if f.reopened != 1 || !strings.Contains(m.status, "Reopening 20 sessions. 3 stay on the list: A again") {
		t.Errorf("A: %d, %q", f.reopened, m.status)
	}
	key(m, "esc")
	if m.history != nil {
		t.Errorf("esc did not close the screen")
	}
}

// / filters the rows, ctrl+s searches for the same words, and esc goes
// back a step at a time. A hit reopens its session.
func TestHistoryFilterAndSearch(t *testing.T) {
	m, f := newFullModel(t, closedSnapshot(), 120, 30)
	key(m, "v", "R", "/", "blog")
	v := view(m)
	if strings.Contains(v, "fix the checkout") || !strings.Contains(v, "add an RSS feed") || !strings.Contains(v, "/blog") {
		t.Fatalf("filter:\n%s", v)
	}
	key(m, "enter")
	if m.history.filtering {
		t.Fatal("enter did not keep the filter")
	}
	key(m, "ctrl+s")
	if len(f.searched) != 1 || f.searched[0] != "blog" {
		t.Fatalf("searched: %v", f.searched)
	}
	if v := view(m); !strings.Contains(v, `SEARCH "blog" · 2 hits`) || !strings.Contains(v, "the login form breaks") {
		t.Fatalf("hits:\n%s", v)
	}
	key(m, "enter")
	if len(f.resumed) != 1 || f.resumed[0] != historySample()[1].SessionID {
		t.Fatalf("resumed: %v", f.resumed)
	}
	key(m, "esc")
	if v := view(m); strings.Contains(v, "SEARCH") || !strings.Contains(v, "/blog") {
		t.Fatalf("esc after a search:\n%s", v)
	}
	key(m, "esc")
	if v := view(m); !strings.Contains(v, "fix the checkout") {
		t.Fatalf("esc after a filter:\n%s", v)
	}
	// ctrl+s with no words asks for them.
	key(m, "ctrl+s")
	if !m.history.filtering || !strings.Contains(m.status, "Type the words") {
		t.Errorf("empty search: %v %q", m.history.filtering, m.status)
	}
	key(m, "nothing", "ctrl+s")
	if v := view(m); !strings.Contains(v, "No session text holds these words") {
		t.Errorf("no hits:\n%s", v)
	}
}

// The screen without rows, without a list, and with a failing daemon.
func TestHistoryEmptyAndErrors(t *testing.T) {
	m, f := newFullModel(t, sample(), 120, 30)
	f.noHistory = true
	key(m, "v", "R")
	if v := view(m); !strings.Contains(v, "No past sessions") || strings.Contains(v, "A reopen all") {
		t.Errorf("empty:\n%s", v)
	}
	key(m, "A")
	if f.reopened != 0 || !strings.Contains(m.status, "No closed sessions") {
		t.Errorf("A with no list: %d %q", f.reopened, m.status)
	}
	key(m, "enter") // no row: no call
	if len(f.resumed) != 0 {
		t.Errorf("resumed: %v", f.resumed)
	}
	m, f = newFullModel(t, sample(), 120, 30)
	f.historyErr = errors.New("no daemon")
	key(m, "v", "R")
	if v := view(m); !strings.Contains(v, "cannot read the history: no daemon") {
		t.Errorf("error:\n%s", v)
	}
}

// A long list scrolls with the cursor, and the home line counts in words.
func TestHistoryScrollsAndBanner(t *testing.T) {
	m, _ := newFullModel(t, closedSnapshot(), 80, 24)
	s := closedSnapshot()
	s.Closed.Sessions = s.Closed.Sessions[:1]
	m.snap = &s
	key(m, "v")
	if v := view(m); !strings.Contains(v, " 1 session closed · R reopens") {
		t.Errorf("one:\n%s", v)
	}
	s.Closed.Reopening = true
	if v := view(m); !strings.Contains(v, "closed · reopening…") {
		t.Errorf("reopening:\n%s", v)
	}
	s.Closed.Sessions, s.Closed.Reopening = nil, false
	if v := view(m); strings.Contains(v, "session closed") {
		t.Errorf("no list:\n%s", v)
	}
	key(m, "R")
	m.history.rows = nil
	for i := range 60 {
		m.history.rows = append(m.history.rows, api.HistoryRow{SessionID: fmt.Sprint("id-", i), Dir: "/w", Name: fmt.Sprint("name ", i), At: t0})
	}
	key(m, "G")
	if v := view(m); !strings.Contains(v, "name 59") {
		t.Errorf("G:\n%s", v)
	}
	key(m, "g")
	if v := view(m); !strings.Contains(v, "name 0") {
		t.Errorf("g:\n%s", v)
	}
}
