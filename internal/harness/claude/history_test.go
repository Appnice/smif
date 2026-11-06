package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	idA = "aaaaaaaa-0000-4000-8000-000000000001"
	idB = "bbbbbbbb-0000-4000-8000-000000000002"
	idC = "cccccccc-0000-4000-8000-000000000003"
)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func record(typ, entry, cwd string, at time.Time, content any) string {
	b, _ := json.Marshal(map[string]any{"type": typ, "entrypoint": entry, "cwd": cwd, "timestamp": stamp(at),
		"message": map[string]any{"role": typ, "content": content}})
	return string(b) + "\n"
}

func writeTranscript(t *testing.T, cfg, folder, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(cfg, "projects", folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The index lists interactive sessions, newest first: not an SDK run, not a
// subagent file, not a file with another name. A title beats the first
// prompt, and system text is no prompt.
func TestHistoryRows(t *testing.T) {
	cfg := t.TempDir()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	head := func(entry string) string {
		return `{"type":"session-owner","sessionId":"x"}` + "\n" + record("user", entry, "/work/a", now.Add(-time.Hour), "<command-name>/clear</command-name>")
	}
	writeTranscript(t, cfg, "-work-a", idA, head("cli"),
		record("user", "cli", "/work/a", now.Add(-time.Hour), "[Request interrupted by user]"),
		record("user", "cli", "/work/a/sub", now.Add(-time.Hour), "fix the\nlogin form"),
		record("assistant", "cli", "/work/a/sub", now.Add(-time.Minute), []map[string]any{{"type": "text", "text": "done"}}))
	writeTranscript(t, cfg, "-work-b", idB, head("cli"),
		record("user", "cli", "/work/b", now.Add(-2*time.Hour), "first prompt"),
		`{"type":"custom-title","customTitle":"shop \u001b[31mfix","sessionId":"x"}`+"\n")
	writeTranscript(t, cfg, "-work-b", idC, head("sdk-cli"), record("user", "sdk-cli", "/work/b", now, "an sdk run"))
	writeTranscript(t, cfg, "-work-b", "agent-a1b2c3", head("cli"), record("user", "cli", "/work/b", now, "old subagent"))
	writeTranscript(t, cfg, filepath.Join("-work-b", idB, "subagents"), "dddddddd-0000-4000-8000-000000000004", head("cli"), record("user", "cli", "/work/b", now, "a subagent"))

	rows := New(cfg).History()
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	a, b := rows[0], rows[1]
	if a.SessionID != idA || a.Dir != "/work/a" || a.Name != "fix the login form" || !a.At.Equal(now.Add(-time.Minute)) {
		t.Errorf("first row: %+v", a)
	}
	if b.SessionID != idB || b.Name != "shop [31mfix" {
		t.Errorf("second row: %+v", b)
	}
}

// Only the head and the tail of a file are read: a title in the middle
// does not show, one at the end does, and the bytes read are counted. A
// file that did not change is not read again.
func TestHistoryReadsHeadAndTail(t *testing.T) {
	cfg := t.TempDir()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	filler := record("assistant", "cli", "/work/a", now.Add(-time.Hour), []map[string]any{{"type": "text", "text": strings.Repeat("x", 60<<10)}})
	lines := []string{record("user", "cli", "/work/a", now.Add(-2*time.Hour), "the first prompt")}
	for range 100 { // about 6 MB
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"custom-title","customTitle":"from the middle"}`+"\n")
	for range 100 {
		lines = append(lines, filler)
	}
	lines = append(lines, record("assistant", "cli", "/work/a", now, []map[string]any{{"type": "text", "text": "end"}}))
	path := writeTranscript(t, cfg, "-work-a", idA, lines...)
	st, _ := os.Stat(path)
	if st.Size() < 10<<20 {
		t.Fatalf("the fixture has %d bytes", st.Size())
	}
	p := New(cfg)
	rows := p.History()
	if len(rows) != 1 || rows[0].Name != "the first prompt" || !rows[0].At.Equal(now) {
		t.Fatalf("rows: %+v", rows)
	}
	read := p.histRead.Load()
	if read > histHead+histTail || read == 0 {
		t.Errorf("read %d of %d bytes", read, st.Size())
	}
	p.History()
	if p.histRead.Load() != read {
		t.Errorf("an unchanged file was read again")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintln(f, `{"type":"custom-title","customTitle":"a new name"}`)
	f.Close()
	if rows := p.History(); rows[0].Name != "a new name" {
		t.Errorf("a changed file: %+v", rows)
	}
	// An SDK run stops at its first record.
	sdk := New(cfg)
	writeTranscript(t, cfg, "-work-a", idA, record("user", "sdk-cli", "/work/a", now, "x"), strings.Repeat(filler, 20))
	if rows := sdk.History(); len(rows) != 0 || sdk.histRead.Load() > 128<<10 {
		t.Errorf("sdk: %+v, %d bytes", rows, sdk.histRead.Load())
	}
}

// A search returns only text of user and assistant records, in any case:
// never a tool call, a tool result, thinking, a sidechain, a meta record
// or system text.
func TestSearchOnlySpokenText(t *testing.T) {
	cfg := t.TempDir()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	side := `{"type":"user","isSidechain":true,"cwd":"/w","message":{"content":"needle in a sidechain"}}` + "\n"
	meta := `{"type":"user","isMeta":true,"cwd":"/w","message":{"content":"needle in a meta record"}}` + "\n"
	sys := `{"type":"system","cwd":"/w","content":"needle in a system record"}` + "\n"
	attach := `{"type":"attachment","cwd":"/w","attachment":{"text":"needle in an attachment"}}` + "\n"
	writeTranscript(t, cfg, "-w", idA,
		record("user", "cli", "/w", now, "where is the NEEDLE now?"),
		record("assistant", "cli", "/w", now.Add(time.Second), []map[string]any{
			{"type": "thinking", "thinking": "a needle in thinking"},
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "grep needle"}},
			{"type": "text", "text": "Żółw and a Needle \"quoted\" here"}}),
		record("user", "cli", "/w", now.Add(2*time.Second), []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "needle in a result"}}),
		record("user", "cli", "/w", now.Add(3*time.Second), "<system-reminder>needle in a reminder</system-reminder>"),
		side, meta, sys, attach)
	p := New(cfg)
	hits := p.Search(context.Background(), "needle", 50)
	if len(hits) != 2 {
		t.Fatalf("hits: %+v", hits)
	}
	if h := hits[0]; h.Role != "you" || h.Text != "where is the NEEDLE now?" || h.SessionID != idA || h.Dir != "/w" || !h.At.Equal(now) {
		t.Errorf("first hit: %+v", h)
	}
	if h := hits[1]; h.Role != "claude" || !strings.HasPrefix(h.Text, "Żółw") {
		t.Errorf("second hit: %+v", h)
	}
	for _, q := range []string{`needle "quoted"`, "ŻÓŁW"} {
		if hits := p.Search(context.Background(), q, 50); len(hits) != 1 {
			t.Errorf("%q: %+v", q, hits)
		}
	}
}

// A search goes through the newest file first, leaves out SDK runs, and
// stops at the limit of hits and when its context ends.
func TestSearchLimits(t *testing.T) {
	cfg := t.TempDir()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var many []string
	for i := range 80 {
		many = append(many, record("user", "cli", "/w", now.Add(time.Duration(i)*time.Second), fmt.Sprintf("needle %d", i)))
	}
	writeTranscript(t, cfg, "-w", idA, many...)
	writeTranscript(t, cfg, "-w", idB, record("user", "cli", "/w", now.Add(time.Hour), "the newest needle"))
	writeTranscript(t, cfg, "-w", idC, record("user", "sdk-cli", "/w", now.Add(2*time.Hour), "needle of a run"))
	p := New(cfg)
	hits := p.Search(context.Background(), "needle", 50)
	if len(hits) != 50 || hits[0].SessionID != idB || hits[1].SessionID != idA {
		t.Fatalf("%d hits: %+v", len(hits), hits[:2])
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hits := p.Search(ctx, "needle", 50); len(hits) != 0 {
		t.Errorf("a stopped search found %d", len(hits))
	}
}

// A very long record is passed over and the rest of the file still counts.
func TestEachLineLongRecord(t *testing.T) {
	long := strings.Repeat("x", histMaxLine+1)
	var got []int
	eachLine(strings.NewReader("a\n"+long+"\nbb\nccc"), func(l []byte) bool {
		got = append(got, len(l))
		return true
	})
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Errorf("lines: %v", got)
	}
}
