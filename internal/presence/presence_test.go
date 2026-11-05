package presence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptReadsNewestTitleAndPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"type":"custom-title","customTitle":"old","sessionId":"s"}`,
		`{"type":"last-prompt","lastPrompt":"first","sessionId":"s"}`,
		`not json with "custom-title"`,
		`{"type":"user","message":{"content":"mentions \"custom-title\" in text"}}`,
		`{"type":"custom-title","customTitle":"rfc","sessionId":"s"}`,
		`{"type":"last-prompt","lastPrompt":"run the tests","sessionId":"s"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if title, prompt := Transcript(path); title != "rfc" || prompt != "run the tests" {
		t.Fatalf("got %q %q", title, prompt)
	}
	// Only the end of a long transcript is read; the newest records are there.
	big := strings.Repeat(`{"type":"assistant","x":"`+strings.Repeat("a", 1000)+`"}`+"\n", 1500)
	if err := os.WriteFile(path, []byte(`{"type":"custom-title","customTitle":"lost"}`+"\n"+big+lines[4]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if title, _ := Transcript(path); title != "rfc" {
		t.Fatalf("long transcript title %q", title)
	}
	for _, p := range []string{"", filepath.Join(dir, "missing.jsonl"), filepath.Join(dir, "x.txt")} {
		if a, b := Transcript(p); a != "" || b != "" {
			t.Errorf("%q gave %q %q", p, a, b)
		}
	}
}
